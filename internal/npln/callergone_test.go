package npln

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestAConsoleHangingUpIsNotLoggedAsAServerFault: a wrapped context
// cancellation is the console leaving, a real store error is not.
func TestAConsoleHangingUpIsNotLoggedAsAServerFault(t *testing.T) {
	if !callerGone(fmt.Errorf("query: %w", context.Canceled)) {
		t.Error("a wrapped cancellation was treated as a server fault")
	}
	if callerGone(errors.New("connection refused")) {
		t.Error("a real store error was treated as the caller leaving")
	}
}
