package dashboard

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOnlyAFreshSignedLoginLinkOpensTheDashboard(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	srv, _ := newTestServer(t, "s3cret", now)
	h := srv.Handler()
	link := func(exp int64, sig string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", fmt.Sprintf("/login/link?exp=%d&sig=%s", exp, sig), nil))
		return rec
	}
	good := now.Add(5 * time.Minute).Unix()
	cases := []struct {
		name string
		exp  int64
		sig  string
		ok   bool
	}{
		{"fresh", good, LoginLinkSignature("s3cret", good), true},
		{"expired", now.Add(-time.Second).Unix(), LoginLinkSignature("s3cret", now.Add(-time.Second).Unix()), false},
		{"too far ahead", now.Add(time.Hour).Unix(), LoginLinkSignature("s3cret", now.Add(time.Hour).Unix()), false},
		{"wrong key", good, LoginLinkSignature("other", good), false},
		{"exp changed", good + 1, LoginLinkSignature("s3cret", good), false},
	}
	for _, c := range cases {
		rec := link(c.exp, c.sig)
		got := rec.Header().Get("Location") == "/" && len(rec.Result().Cookies()) == 1
		if rec.Code != http.StatusFound || got != c.ok {
			t.Errorf("%s: %d to %q with %d cookies, want ok=%v", c.name, rec.Code, rec.Header().Get("Location"), len(rec.Result().Cookies()), c.ok)
		}
	}
}
