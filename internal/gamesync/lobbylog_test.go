package gamesync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLobbyLoggingFollowsItsFlagFileWithoutARestart(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "lobby-log")
	srv, err := New(Options{
		VerifyIDToken: func(string) (string, error) { return "", nil },
		LobbyLogFlag:  flag,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.WatchLobbyLogFlag(ctx, 10*time.Millisecond)

	waitFor := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for srv.lobbyLog.Load() != want {
			if time.Now().After(deadline) {
				t.Fatalf("lobby logging = %v, want %v", !want, want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitFor(false)
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(true)
	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	waitFor(false)
}
