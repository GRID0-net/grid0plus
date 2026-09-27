package identity_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/identity"
	sjwt "github.com/n-popescu/grid0plus/internal/jwt"
)

type fixture struct {
	t      *testing.T
	store  *db.Memory
	tokens *identity.IDTokens
	issuer *sjwt.Issuer
	access *sjwt.Issuer
}

func newFixture(t *testing.T) *fixture {
	ring := sjwt.NewKeyring(filepath.Join(t.TempDir(), "keys"), true)
	idKey, err := ring.Key(sjwt.IssuerBAASID)
	if err != nil {
		t.Fatal(err)
	}
	accessKey, err := ring.Key(sjwt.IssuerBAASAccess)
	if err != nil {
		t.Fatal(err)
	}
	store := db.NewMemory()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &fixture{t: t, store: store,
		tokens: identity.NewIDTokens(idKey, store, log),
		issuer: sjwt.NewIssuer(idKey, "", true),
		access: sjwt.NewIssuer(accessKey, "", true)}
}

func (f *fixture) user(id, da string) {
	f.t.Helper()
	if err := f.store.CreateUser(context.Background(),
		db.User{ID: id, Nickname: id},
		db.DeviceAccount{ID: da, UserID: id, PasswordSHA256: "x"}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) token(iss *sjwt.Issuer, typ, sub, da string, iat time.Time) string {
	f.t.Helper()
	tok, err := iss.Sign(sjwt.BAASTokenClaims{Subject: sub, DeviceAccountID: da, Type: typ,
		IssuedAt: iat.Unix(), Expires: iat.Add(time.Hour).Unix(), TokenID: sjwt.NewTokenID()})
	if err != nil {
		f.t.Fatal(err)
	}
	return tok
}

func TestAnIDTokenResolvesToItsSubjectAndConsole(t *testing.T) {
	f := newFixture(t)
	f.user("0123456789abcdef", "00000000000000da")
	c, err := f.tokens.Resolve(context.Background(),
		f.token(f.issuer, "id_token", "0123456789abcdef", "00000000000000da", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if c.UserID != "0123456789abcdef" || c.DeviceAccountID != "00000000000000da" {
		t.Fatalf("resolved %+v", c)
	}
}

func TestATokenForAUserThatDoesNotExistIsRefused(t *testing.T) {
	f := newFixture(t)
	if _, err := f.tokens.Resolve(context.Background(),
		f.token(f.issuer, "id_token", "0123456789abcdef", "", time.Now())); err == nil {
		t.Fatal("a token for an unknown user resolved")
	}
}

func TestATokenWhoseSubjectDoesNotOwnItsDeviceAccountIsRefused(t *testing.T) {
	f := newFixture(t)
	f.user("aaaaaaaaaaaaaaaa", "00000000000000a1")
	f.user("bbbbbbbbbbbbbbbb", "00000000000000b1")
	if _, err := f.tokens.Resolve(context.Background(),
		f.token(f.issuer, "id_token", "aaaaaaaaaaaaaaaa", "00000000000000b1", time.Now())); err == nil {
		t.Fatal("a token naming someone else's console resolved")
	}
}

func TestATokenStopsWorkingWhenItsDeviceAccountIsDeleted(t *testing.T) {
	f := newFixture(t)
	f.user("aaaaaaaaaaaaaaaa", "00000000000000a1")
	tok := f.token(f.issuer, "id_token", "aaaaaaaaaaaaaaaa", "00000000000000a1", time.Now())
	if err := f.store.DeleteDeviceAccount(context.Background(), "aaaaaaaaaaaaaaaa", "00000000000000a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tokens.Resolve(context.Background(), tok); err == nil {
		t.Fatal("a token outlived its device account")
	}
}

func TestADeletedUserIsRefused(t *testing.T) {
	f := newFixture(t)
	f.user("aaaaaaaaaaaaaaaa", "00000000000000a1")
	deleted := true
	if _, err := f.store.UpdateUser(context.Background(), "aaaaaaaaaaaaaaaa", db.UserPatch{Deleted: &deleted}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tokens.Resolve(context.Background(),
		f.token(f.issuer, "id_token", "aaaaaaaaaaaaaaaa", "00000000000000a1", time.Now())); err == nil {
		t.Fatal("a deleted user resolved")
	}
}

func TestATokenMintedBeforeARevocationIsRefused(t *testing.T) {
	f := newFixture(t)
	f.user("aaaaaaaaaaaaaaaa", "00000000000000a1")
	old := f.token(f.issuer, "id_token", "aaaaaaaaaaaaaaaa", "00000000000000a1", time.Now().Add(-time.Minute))
	if err := f.store.RevokeDeviceAccountTokens(context.Background(), "00000000000000a1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tokens.Resolve(context.Background(), old); err == nil {
		t.Fatal("a revoked token resolved")
	}
}

func TestAnAccessTokenIsNotAnIDToken(t *testing.T) {
	f := newFixture(t)
	f.user("aaaaaaaaaaaaaaaa", "00000000000000a1")
	// Signed with the id key but typed as an access token...
	if _, err := f.tokens.Resolve(context.Background(),
		f.token(f.issuer, "token", "aaaaaaaaaaaaaaaa", "00000000000000a1", time.Now())); err == nil {
		t.Fatal("typ=token accepted")
	}
	// ...and a real access token, signed with the other key.
	if _, err := f.tokens.Resolve(context.Background(),
		f.token(f.access, "id_token", "aaaaaaaaaaaaaaaa", "00000000000000a1", time.Now())); err == nil {
		t.Fatal("a token signed with the access key accepted")
	}
}
