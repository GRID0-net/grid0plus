package ugcstore

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/n-popescu/switchnet/proto/errdetails"
)

// A real missing-document response carries a relative-path message and an
// NError detail. The two identifiers in that detail vary on every response;
// they are request markers, not Nintendo-issued credentials or fixed data.
func missingDocument(name string) error {
	path := name
	if _, rel, ok := strings.Cut(name, documentsSegment); ok {
		path = rel
	}
	message := `NotFound. reason: "Document:` + path + `" Not Found`

	markers := make([]byte, 24)
	if _, err := rand.Read(markers); err != nil {
		return status.Error(codes.Internal, "cannot prepare document error")
	}
	detail := &errdetails.NError{
		TraceId: hex.EncodeToString(markers[:16]),
		DetailCodeType: &errdetails.NError_NotFoundDetailCode_{
			NotFoundDetailCode: errdetails.NError_NOT_FOUND_GENERIC,
		},
	}
	// The generated NError schema predates the observed field 19 marker. Keep
	// that extension in protobuf unknown fields rather than inventing a new
	// checked-in proto definition just for an opaque request identifier.
	unknown := protowire.AppendTag(nil, 19, protowire.BytesType)
	unknown = protowire.AppendString(unknown, hex.EncodeToString(markers[16:]))
	detail.ProtoReflect().SetUnknown(unknown)

	st, err := status.New(codes.NotFound, message).WithDetails(detail)
	if err != nil {
		return status.Error(codes.Internal, "cannot prepare document error")
	}
	return st.Err()
}
