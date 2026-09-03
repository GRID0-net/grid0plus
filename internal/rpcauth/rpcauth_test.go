package rpcauth

import (
	"context"
	"testing"
)

func TestFromRoundTripsWhatWithStored(t *testing.T) {
	ctx := With(context.Background(), Caller{UserID: "alice000000000a", DeviceAccountID: "da-alice"})

	c, ok := From(ctx)
	if !ok {
		t.Fatal("From reported no caller after With")
	}
	if c.UserID != "alice000000000a" || c.DeviceAccountID != "da-alice" {
		t.Errorf("got %+v", c)
	}
}

// TestFromOfAPlainContextIsAbsent guards the case that matters most: a
// handler that forgets to check ok and reads a zero-value Caller instead of
// treating an unauthenticated context as unauthenticated.
func TestFromOfAPlainContextIsAbsent(t *testing.T) {
	if _, ok := From(context.Background()); ok {
		t.Error("From reported a caller present on a context nothing wrote to")
	}
}
