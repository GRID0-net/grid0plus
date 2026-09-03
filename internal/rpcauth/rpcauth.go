// Package rpcauth carries an authenticated NPLN caller's identity across a
// package boundary a gRPC handler cannot avoid.
//
// internal/npln's auth interceptor resolves who is calling, once, for every
// registered service on its *grpc.Server — including internal/toyohr's
// FestService, which is a separate package registered on that same server
// (internal/npln/services.go). But internal/npln already imports
// internal/toyohr to register it, so internal/toyohr cannot import
// internal/npln back to read its interceptor's own context key: Go's import
// graph does not allow the cycle. This package is the seam: npln's
// interceptor writes to it, toyohr's handlers read from it, and neither
// imports the other.
//
// Nothing here authenticates anything. It only carries a value npln's
// interceptor already computed after checking a token against the store —
// see internal/npln/server.go's enter. A package that wrote to this context
// key without that check having run first would forge an identity, which is
// exactly the fallback CLAUDE.md's third ground rule (identity is
// fail-closed) forbids. That is why With is documented as npln's alone to
// call, not enforced by the type system — Go has no way to restrict a
// context.Context to one writer.
package rpcauth

import "context"

// Caller is the authenticated identity behind one NPLN gRPC call.
type Caller struct {
	UserID          string
	DeviceAccountID string
}

type key struct{}

// With returns a context carrying c.
//
// Call this only from internal/npln's auth interceptor, after it has
// resolved c the same way it resolves its own Caller — see the package
// comment for why nothing enforces that beyond this note.
func With(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, key{}, c)
}

// From returns the authenticated caller, if the RPC required one.
func From(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(key{}).(Caller)
	return c, ok
}
