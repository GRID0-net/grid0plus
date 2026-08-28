package npln

import (
	"context"
	"strings"

	"google.golang.org/grpc/metadata"
)

// The gRPC metadata keys NPLN carries on every call.
//
// gRPC lower-cases metadata keys on the wire, so these are written lower-case
// here: looking one up by the capitalisation a capture shows finds nothing.
const (
	// mdTenant identifies the title. Splatoon 3's tenant is dce9377b.
	mdTenant = "npln-tenant-id"
	// mdAuthorization carries "Bearer <NPLN access token>".
	mdAuthorization = "authorization"
	// mdUID is the caller's NPLN user id.
	//
	// It is NOT a credential and must never be treated as one. It is the
	// client saying who it believes it is; the access token is what proves it.
	// Trusting this field is precisely the shape of the bug this project is
	// built around — a request resolving to whoever the caller named.
	mdUID = "uid"
)

// callMetadata is the parsed form of one call's NPLN metadata.
type callMetadata struct {
	tenant string
	// bearer is the raw access token, with the "Bearer " prefix removed. Empty
	// when the call carried no authorization metadata, which is legitimate for
	// the token-issuing RPCs and for nothing else.
	bearer string
	// uid is what the caller claims its user id is. Advisory only: it is
	// compared against the token's subject, never used in its place.
	uid string
}

// parseMetadata reads the NPLN metadata from a call.
//
// A missing tenant is malformed; a missing authorization is not, because the
// RPCs that issue a token are called before one exists. Whether a given RPC may
// proceed unauthenticated is decided per RPC, not here.
func parseMetadata(ctx context.Context) (callMetadata, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return callMetadata{}, false
	}

	var out callMetadata
	if v := first(md, mdTenant); v != "" {
		out.tenant = v
	} else {
		return callMetadata{}, false
	}

	if v := first(md, mdAuthorization); v != "" {
		// Case-insensitive on the scheme: RFC 7235 says the scheme is
		// case-insensitive, and a client that sends "bearer" is not malformed.
		if len(v) >= 7 && strings.EqualFold(v[:7], "bearer ") {
			out.bearer = strings.TrimSpace(v[7:])
		} else {
			// An authorization header we cannot parse is malformed. Ignoring it
			// and continuing unauthenticated would turn a client bug into a
			// silent downgrade.
			return callMetadata{}, false
		}
	}

	out.uid = first(md, mdUID)
	return out, true
}

func first(md metadata.MD, key string) string {
	vs := md.Get(key)
	if len(vs) == 0 {
		return ""
	}
	return strings.TrimSpace(vs[0])
}
