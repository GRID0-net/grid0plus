// Package stunresponder answers STUN (RFC 5389) Binding requests.
//
// Splatoon 3 sends these to whatever host ListLatencyMeasurementServers names,
// before matchmaking starts: each is a 20-byte Binding Request with RFC 5389's
// magic cookie (2112a442) at byte offset 4. If nothing answers them (ICMP
// port-unreachable every time, since nothing listens on that port), the console
// gives up a few seconds later -- the hall then shows "A communication error has
// occurred" and reloads.
//
// STUN itself is a generic, publicly documented internet standard (RFC 5389), not
// a Nintendo internal: answering it is implementing a known protocol, the same
// category as internal/natcheck's NCS responder. Only Binding is implemented --
// the one message type the console sends. Everything else (fragmentation,
// authentication, other STUN usages) is out of scope for what one client
// actually asks this server for.
package stunresponder

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
)

const (
	// magicCookie is fixed by RFC 5389 5.1 -- present in every STUN message,
	// request or response, and how a STUN message is told apart from
	// anything else that might land on the same port.
	magicCookie uint32 = 0x2112A442

	typeBindingRequest  uint16 = 0x0001
	typeBindingResponse uint16 = 0x0101

	attrXorMappedAddress uint16 = 0x0020

	headerSize = 20 // type(2) + length(2) + cookie(4) + transaction id(12)
	familyIPv4 = 0x01
)

// Listen opens the UDP socket. Separate from Serve so a caller can learn the
// bound address (addr may end in ":0") before traffic starts, the same split
// internal/natcheck's own Listen/Serve has.
func Listen(addr string) (net.PacketConn, error) {
	return net.ListenPacket("udp", addr)
}

// Serve answers STUN Binding requests on conn until ctx is cancelled. It
// never returns an error for a malformed or non-STUN packet -- only for
// something wrong with the socket itself, the same tolerance
// internal/natcheck's own responder has for traffic that is not its
// protocol.
func Serve(ctx context.Context, conn net.PacketConn, log *slog.Logger) error {
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	buf := make([]byte, 1500)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var netErr net.Error
			if errors.As(err, &netErr) {
				continue
			}
			return err
		}

		resp, ok := bindingResponse(buf[:n], from)
		if !ok {
			continue
		}
		if _, err := conn.WriteTo(resp, from); err != nil {
			log.Debug("stunresponder: write", "error", err)
		}
	}
}

// bindingResponse builds a Binding Success Response for req, if req is a
// Binding Request STUN can actually answer (IPv4 source only -- the console
// this was built for has no path to reach this server over IPv6).
func bindingResponse(req []byte, from net.Addr) ([]byte, bool) {
	if len(req) < headerSize {
		return nil, false
	}
	msgType := binary.BigEndian.Uint16(req[0:2])
	cookie := binary.BigEndian.Uint32(req[4:8])
	if msgType != typeBindingRequest || cookie != magicCookie {
		return nil, false
	}
	transactionID := req[8:headerSize]

	udpAddr, ok := from.(*net.UDPAddr)
	if !ok {
		return nil, false
	}
	ip4 := udpAddr.IP.To4()
	if ip4 == nil {
		return nil, false
	}

	// XOR-MAPPED-ADDRESS, RFC 5389 15.2: reserved(1)=0, family(1)=0x01,
	// X-Port = port XOR (cookie's high 16 bits), X-Address = address XOR
	// cookie, both applied big-endian.
	attrValue := make([]byte, 8)
	attrValue[1] = familyIPv4
	xport := uint16(udpAddr.Port) ^ uint16(cookie>>16)
	binary.BigEndian.PutUint16(attrValue[2:4], xport)
	var cookieBytes [4]byte
	binary.BigEndian.PutUint32(cookieBytes[:], cookie)
	for i := 0; i < 4; i++ {
		attrValue[4+i] = ip4[i] ^ cookieBytes[i]
	}

	out := make([]byte, 0, headerSize+4+len(attrValue))
	out = binary.BigEndian.AppendUint16(out, typeBindingResponse)
	out = binary.BigEndian.AppendUint16(out, uint16(4+len(attrValue)))
	out = binary.BigEndian.AppendUint32(out, cookie)
	out = append(out, transactionID...)
	out = binary.BigEndian.AppendUint16(out, attrXorMappedAddress)
	out = binary.BigEndian.AppendUint16(out, uint16(len(attrValue)))
	out = append(out, attrValue...)
	return out, true
}
