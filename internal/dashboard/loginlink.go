package dashboard

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"
)

// LoginLinkMaxAge caps how far ahead a login link may expire. A link pasted
// into Discord stays in the channel's history; a short life is what keeps a
// scrolled-back message from being a permanent key to the Splatfest
// controls, which handing out ?token= links would be.
const LoginLinkMaxAge = 15 * time.Minute

// LoginLinkSignature is the signature a /login/link URL carries for an
// expiry. It is keyed by the dashboard's own shared token, so whoever can
// sign a link -- the Discord bot, given the token in its config -- could
// already open the dashboard; the link adds expiry, not a new credential.
func LoginLinkSignature(token string, expires int64) string {
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte("grid0plus-dashboard-login:" + strconv.FormatInt(expires, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

// handleLoginLink answers GET /login/link?exp=<unix>&sig=<hex>: a valid,
// unexpired link sets the same cookie /login's form does. Anything else
// goes to the form, never to the dashboard.
func (s *Server) handleLoginLink(w http.ResponseWriter, r *http.Request) {
	if s.token == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	exp, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	now := s.now()
	if err != nil || !hmac.Equal([]byte(r.URL.Query().Get("sig")), []byte(LoginLinkSignature(s.token, exp))) {
		http.Redirect(w, r, "/login?failed=1", http.StatusFound)
		return
	}
	// A far-future expiry is refused even with a valid signature, so a
	// signer bug cannot mint a link that never dies.
	if until := time.Unix(exp, 0).Sub(now); until <= 0 || until > LoginLinkMaxAge {
		http.Redirect(w, r, "/login?failed=1", http.StatusFound)
		return
	}
	s.setAuthCookie(w, r, s.token)
	http.Redirect(w, r, "/", http.StatusFound)
}
