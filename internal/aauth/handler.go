// Package aauth reimplements Nintendo's application authentication server.
//
// Endpoint: https://aauth-lp1.ndas.srv.nintendo.net (aauth.hac.lp1.ndas... from 20.0.0)
// Reference: https://github.com/kinnay/NintendoClients/wiki/AAuth-Server
//
// An application token is what proves to a game server "this console owns this
// game". On Nintendo's server that proof is a ticket or gamecard certificate
// signed by Nintendo, plus for gamecards a challenge answered by the cartridge.
//
// SwitchNet accepts the cert, ticket and challenge fields without looking at
// them, and never checks game ownership. That is the point of a private server
// for consoles the operator owns: there is nobody to prove ownership to. It is
// not a way to get an unowned game online against Nintendo's servers, because
// nothing here produces a credential Nintendo would accept.
package aauth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	sjwt "github.com/n-popescu/switchnet/internal/jwt"
)

// Config is what the handler needs to run.
type Config struct {
	Issuer   string
	JWKSURL  string
	TokenTTL time.Duration

	// PolicyFor returns the online play policy and handler for a title id.
	PolicyFor func(titleID string) (policy, handler string)

	// DeviceIDSecret salts the derivation of the 'edi' claim.
	DeviceIDSecret string
}

// Handler serves the aauth API.
type Handler struct {
	cfg      Config
	issuer   *sjwt.Issuer
	verifier *sjwt.Verifier
	log      *slog.Logger

	// challengeSeed is the 15-byte 'seed' returned with a gamecard challenge.
	// On Nintendo's server it is a constant, the same in every environment.
	// SwitchNet never verifies the reply, so it only has to be stable.
	challengeSeed []byte
}

// New returns an aauth handler. The verifier checks the device token the
// console presents, which SwitchNet's own dauth server issued.
func New(cfg Config, issuer *sjwt.Issuer, deviceTokenVerifier *sjwt.Verifier, log *slog.Logger) *Handler {
	seed := make([]byte, 15)
	copy(seed, sjwt.DeriveHexID(cfg.DeviceIDSecret, 15, "aauth-challenge-seed"))
	return &Handler{cfg: cfg, issuer: issuer, verifier: deviceTokenVerifier, log: log, challengeSeed: seed}
}

// Routes registers every aauth route on mux.
func (h *Handler) Routes(mux *http.ServeMux) {
	// v2's path is obfuscated with a hex string. It is part of the URL.
	const v2 = "/v2-44cd4221f90742b5f37a4948b37dacf024d0bb14dde86db0af20ec300a36a0fe"

	mux.HandleFunc("GET /v1/time", h.time)

	for _, p := range []string{"/v3", "/v4", "/v5"} {
		mux.HandleFunc("POST "+p+"/challenge", h.challenge)
	}
	for _, p := range []string{"/v1", v2, "/v3", "/v4", "/v5"} {
		mux.HandleFunc("POST "+p+"/application_auth_token", h.applicationToken)
	}

	mux.Handle("GET /keys", sjwt.JWKSHandler(sjwt.JWKSetFor("", h.issuer.Key())))
}

// ---- /v1/time ------------------------------------------------------------

// time returns the server time and the client's external address.
//
// This endpoint has nothing to do with application tokens; it is how a console
// up to 17.0.1 learns the time and its own public IP. Getting it wrong shows up
// as expired tokens everywhere, because the console sets its clock from here.
func (h *Handler) time(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UnixMilli()
	ip := clientIP(r)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-NINTENDO-UNIXTIME", strconv.FormatInt(now, 10))
	w.Header().Set("X-NINTENDO-GLOBAL-IP", ip)
	fmt.Fprintf(w, "%d\n%s\n", now, ip)
}

// clientIP returns the console's address as seen from outside.
//
// X-Forwarded-For is only trusted for its last entry, which is the one the
// immediately preceding proxy wrote. Trusting the first entry would let a
// console put whatever it liked in the header and have the server repeat it.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// ---- gamecard challenge --------------------------------------------------

type challengeV4Response struct {
	Value string `json:"value"`
	Seed  string `json:"seed"`
}

type challengeV5Response struct {
	Challenge    string `json:"challenge"`
	ChallengeSrc string `json:"challenge_src"`
	Seed         string `json:"seed"`
}

// challenge issues a gamecard-presence challenge.
//
// The console answers it with Lotus3's ChallengeCardExistence, which only a
// real cartridge can compute. SwitchNet never checks the answer, so the values
// here just have to be the right length.
func (h *Handler) challenge(w http.ResponseWriter, r *http.Request) {
	params, err := parseForm(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidParameter)
		return
	}
	// Up to 17.0.1 the console prefixes this parameter with a stray ampersand
	// even though it is the only one, which parseForm already tolerates.
	if params.Get("device_auth_token") == "" {
		writeError(w, http.StatusBadRequest, errInvalidParameter)
		return
	}

	seed := base64.StdEncoding.EncodeToString(h.challengeSeed)

	if strings.HasPrefix(r.URL.Path, "/v5/") {
		challenge, err := randomBase64(32)
		if err != nil {
			h.log.Error("aauth: read random bytes", "error", err)
			writeError(w, http.StatusInternalServerError, errInternal)
			return
		}
		src, err := randomBase64(9)
		if err != nil {
			h.log.Error("aauth: read random bytes", "error", err)
			writeError(w, http.StatusInternalServerError, errInternal)
			return
		}
		writeJSON(w, http.StatusOK, challengeV5Response{
			Challenge: challenge, ChallengeSrc: src, Seed: seed,
		})
		return
	}

	value, err := randomBase64(16)
	if err != nil {
		h.log.Error("aauth: read random bytes", "error", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, challengeV4Response{Value: value, Seed: seed})
}

func randomBase64(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

// ---- application token ---------------------------------------------------

type appTokenResponse struct {
	ExpiresIn            int        `json:"expires_in"`
	ApplicationAuthToken string     `json:"application_auth_token"`
	Settings             []settings `json:"settings"`
	OnlinePlayPolicy     string     `json:"online_play_policy,omitempty"`
	PolicyHandler        string     `json:"policy_handler,omitempty"`
}

// settings carries a per-title Nintendo Account client id. It is empty for
// almost every title, and SwitchNet has no Nintendo Account integration, so it
// is always an empty list.
type settings struct {
	ClientID    string `json:"nas:client_id"`
	RedirectURI string `json:"nas:redirect_uri"`
}

// System titles that may use the SYSTEM media type. An application token for
// one of these carries a device block, which regular titles do not get.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/AAuth-Server#system-titles
var systemTitles = map[string]bool{
	"010000000000002f": true, // NPNS
	"010000000000003a": true, // Migration
	"010000000000003e": true, // OLSC
	"0100000000000040": true, // ND
	"0100000000000044": true,
}

func (h *Handler) applicationToken(w http.ResponseWriter, r *http.Request) {
	params, err := parseForm(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidParameter)
		return
	}

	titleID := strings.ToLower(params.Get("application_id"))
	if len(titleID) != 16 {
		writeError(w, http.StatusBadRequest, errInvalidParameter)
		return
	}

	deviceToken := params.Get("device_auth_token")
	if deviceToken == "" {
		writeError(w, http.StatusBadRequest, errInvalidParameter)
		return
	}

	// The device token was issued by SwitchNet's own dauth server, so this is
	// verifying our own signature — not Nintendo's. It catches a console with a
	// stale token (a real condition, error 0103) and a request that skipped
	// dauth entirely.
	var claims sjwt.DeviceTokenClaims
	if err := h.verifier.Verify(deviceToken, &claims); err != nil {
		h.log.Warn("aauth: rejected device token", "title", titleID, "error", err)
		writeError(w, http.StatusBadRequest, errDeviceTokenExpired)
		return
	}

	// v5 renamed media_type to auth_type. Read whichever is present.
	mediaType := params.Get("media_type")
	if mediaType == "" {
		mediaType = params.Get("auth_type")
	}

	// application_version is 8 hex digits in the request; the token's 'av' is
	// the high half, 4 digits. A console that sends something else gets a
	// zeroed version rather than a rejection, because the version is
	// informational and refusing to sign over it would break online play for a
	// title whose version format we guessed wrong.
	appVersion := "0000"
	if v := params.Get("application_version"); len(v) >= 4 {
		appVersion = strings.ToLower(v[:4])
	}

	policy, policyHandler := h.cfg.PolicyFor(titleID)

	now := time.Now()
	info := sjwt.AppInfo{
		AppID:      titleID,
		AppVersion: appVersion,
		AppTime:    now.Unix(),
		// 'edi' is 32 hex digits, unique per (device, title). Deriving it means
		// the same console gets the same value for the same game across
		// reboots, which is what a game server keying on it would expect.
		EDI:              sjwt.DeriveHexID(h.cfg.DeviceIDSecret, 16, "edi", claims.Subject, titleID),
		OnlinePlayPolicy: policy,
	}
	if policy == "MEMBERSHIP_REQUIRED" {
		info.PolicyHandler = policyHandler
	}
	if mediaType == "SYSTEM" && systemTitles[titleID] {
		ist := claims.Nintendo.IsT
		info.DeviceID = claims.Subject
		info.SerialNumber = claims.Nintendo.SerialNumber
		info.ProductCode = claims.Nintendo.ProductCode
		info.DeviceType = claims.Nintendo.DeviceType
		info.IsT = &ist
	}

	token, err := h.issuer.Sign(sjwt.AppTokenClaims{
		Subject:  titleID,
		IssuedAt: now.Unix(),
		Expires:  now.Add(h.cfg.TokenTTL).Unix(),
		Issuer:   h.cfg.Issuer,
		TokenID:  sjwt.NewTokenID(),
		Nintendo: info,
	})
	if err != nil {
		h.log.Error("aauth: sign application token", "error", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}

	h.log.Info("aauth: issued application token",
		"title", titleID, "version", appVersion, "media_type", mediaType,
		"device", claims.Subject, "policy", policy)

	resp := appTokenResponse{
		ExpiresIn:            int(h.cfg.TokenTTL.Seconds()),
		ApplicationAuthToken: token,
		Settings:             []settings{},
		OnlinePlayPolicy:     policy,
	}
	if policy == "MEMBERSHIP_REQUIRED" {
		resp.PolicyHandler = policyHandler
	}
	writeJSON(w, http.StatusOK, resp)
}
