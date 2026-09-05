package npln

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// TestSendNPLNHeaderSplitsHeadersFromTrailers reads the wire, not the client
// API: it counts the HTTP/2 HEADERS frames a raw connection receives for a
// unary call whose handler returns nothing but an error. sendNPLNHeader's own
// comment explains why the count matters -- grpc-go's default folds an
// error-with-nothing-sent-yet into one frame ("Trailers-Only"), and a real
// console has been measured crashing on exactly that shape for an otherwise
// correct NotFound. This proves the fix actually produces the other shape,
// not just that it compiles.
func TestSendNPLNHeaderSplitsHeadersFromTrailers(t *testing.T) {
	withFix := framesForHandler(t, func(ctx context.Context) error {
		sendNPLNHeader(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
		return status.Error(codes.NotFound, "not found")
	})
	if withFix != 2 {
		t.Fatalf("with sendNPLNHeader: %d HEADERS frame(s), want 2 (headers, then trailers)", withFix)
	}

	withoutFix := framesForHandler(t, func(context.Context) error {
		return status.Error(codes.NotFound, "not found")
	})
	if withoutFix != 1 {
		t.Fatalf("control (no header call): %d HEADERS frame(s), want 1 (Trailers-Only) -- "+
			"if this is now 2, grpc-go's default changed and the fix may no longer be doing anything", withoutFix)
	}
}

// framesForHandler runs a bare-bones unary gRPC method whose entire body is
// fn, over a real TCP connection, and returns how many HTTP/2 HEADERS frames
// the client sees.
func framesForHandler(t *testing.T, fn func(ctx context.Context) error) int {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()

	srv := grpc.NewServer()
	srv.RegisterService(&grpc.ServiceDesc{
		ServiceName: "wireframingtest.Probe",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Call",
			Handler: func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				var discard emptypb.Empty
				if err := dec(&discard); err != nil {
					return nil, err
				}
				return nil, fn(ctx)
			},
		}},
	}, struct{}{})
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := net.DialTimeout("tcp", lis.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte(http2.ClientPreface)); err != nil {
		t.Fatalf("write preface: %v", err)
	}
	fr := http2.NewFramer(conn, conn)
	if err := fr.WriteSettings(); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	var hbuf bytes.Buffer
	enc := hpack.NewEncoder(&hbuf)
	for _, kv := range [][2]string{
		{":method", "POST"}, {":scheme", "http"}, {":path", "/wireframingtest.Probe/Call"},
		{":authority", "wireframingtest"}, {"content-type", "application/grpc"}, {"te", "trailers"},
	} {
		if err := enc.WriteField(hpack.HeaderField{Name: kv[0], Value: kv[1]}); err != nil {
			t.Fatalf("hpack encode: %v", err)
		}
	}
	if err := fr.WriteHeaders(http2.HeadersFrameParam{
		StreamID: 1, BlockFragment: hbuf.Bytes(), EndHeaders: true,
	}); err != nil {
		t.Fatalf("write headers: %v", err)
	}
	// An empty gRPC message: one compression-flag byte, then a 4-byte
	// big-endian length of zero.
	msg := make([]byte, 5)
	binary.BigEndian.PutUint32(msg[1:], 0)
	if err := fr.WriteData(1, true, msg); err != nil {
		t.Fatalf("write data: %v", err)
	}

	headersFrames := 0
	for {
		f, err := fr.ReadFrame()
		if err != nil {
			break
		}
		hf, ok := f.(*http2.HeadersFrame)
		if !ok {
			continue
		}
		headersFrames++
		if hf.StreamEnded() {
			break
		}
	}
	return headersFrames
}
