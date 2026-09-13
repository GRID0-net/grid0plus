package npln

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Environment selects how malformed requests are refused.
//
// Nintendo's NPLN servers answer a request whose metadata they cannot parse
// with a status that says nothing about what was wrong: Unimplemented on
// production, Unavailable on development. That is not what either code means,
// and it is deliberate on their side — a client probing the API learns only
// that it got nothing.
//
// SwitchNet reproduces it because the game reacts to the status code, and a
// helpful InvalidArgument where the game expects Unimplemented is a difference
// in behaviour for no gain. The log line on this side still says exactly what
// was wrong.
type Environment int

const (
	// EnvProduction refuses malformed metadata with Unimplemented (12).
	EnvProduction Environment = iota
	// EnvDevelopment refuses malformed metadata with Unavailable (14).
	EnvDevelopment
)

// rejectCode is the status a malformed request is refused with.
func (e Environment) rejectCode() codes.Code {
	if e == EnvDevelopment {
		return codes.Unavailable
	}
	return codes.Unimplemented
}

// errMalformed refuses a request whose metadata could not be understood.
//
// The message is deliberately empty: it goes to the console, which does not
// read it, and a descriptive one would differ from what the game sees against
// Nintendo. Callers log the real reason.
func errMalformed(env Environment) error {
	return status.Error(env.rejectCode(), "")
}

// Errors returned to a caller that authenticated but may not do what it asked.
//
// These are ordinary gRPC statuses rather than NPLN's own error details,
// because SwitchNet only produces the ones it can actually justify. Adding
// codes for checks that are not performed is how a client ends up handling an
// error the server never sends — the same rule internal/baas/errors.go follows.
var (
	// ErrUnauthenticated is returned when no usable credential was presented.
	ErrUnauthenticated = status.Error(codes.Unauthenticated, "unauthenticated")
	// ErrPermissionDenied is returned when the caller is authenticated but is
	// not the subject it is acting on.
	ErrPermissionDenied = status.Error(codes.PermissionDenied, "permission denied")
	// ErrNotFound is returned for a resource that does not exist, or that the
	// caller may not know exists.
	ErrNotFound = status.Error(codes.NotFound, "not found")
)

// errInternal refuses a call that failed for a reason on this server's own
// side -- a database error, not anything about the request. Unlike
// errMalformed it carries no meaning about what the console did, so it does
// not vary with Environment, and the real reason is logged by the caller
// rather than sent: a database error is not the console's to read.
func errInternal() error {
	return status.Error(codes.Internal, "")
}
