// Package dauth reimplements Nintendo's device authentication server.
//
// Endpoint: https://dauth-lp1.ndas.srv.nintendo.net
// Reference: https://github.com/kinnay/NintendoClients/wiki/DAuth-Server
//
// Nintendo's dauth server requires a valid client certificate, signed by
// Nintendo and unique to each console, and requires the client to answer a
// cryptographic challenge using a key derived from the console's master key.
// SwitchNet does neither, on purpose:
//
//   - It cannot verify a Nintendo-signed certificate without Nintendo's CA, and
//     it will not reproduce Nintendo's signing keys.
//   - It cannot check the challenge MAC without the master key, and deriving one
//     would mean shipping Nintendo key material.
//
// So the challenge is issued and echoed but never verified, and a client
// certificate is used only as an identifier when nginx passes one through. This
// is safe only because the operator's network is private and trusted; it is not
// a way to defeat Nintendo's device attestation on Nintendo's own servers.
package dauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	sjwt "github.com/n-popescu/switchnet/internal/jwt"
)

// Config is what the handler needs to run.
type Config struct {
	// Issuer is the 'iss' claim of device tokens.
	Issuer string
	// JWKSURL is the 'jku' header of device tokens.
	JWKSURL string
	// TokenTTL is 'expires_in'.
	TokenTTL time.Duration
	// ChallengeTTL is how long a challenge stays usable.
	ChallengeTTL time.Duration
	// ClientCertHeader is the request header nginx puts the console's client
	// certificate in. Empty disables per-console device ids.
	ClientCertHeader string
	// ClientCertPEMHeader is the request header nginx puts the console's full
	// TLS client certificate in, URI-escaped ($ssl_client_escaped_cert). Empty
	// leaves 'x5t#S256' off issued tokens — see DeviceInfo.X5tS256.
	ClientCertPEMHeader string
	// DeviceIDSecret salts device id derivation.
	DeviceIDSecret string
}

// DeviceSeen is called whenever a console obtains a device token, so the caller
// can record it. It must not block: a slow callback delays the console's boot.
type DeviceSeen func(id, serialNumber, systemVersion string, ist bool)

// Handler serves the dauth API.
type Handler struct {
	cfg    Config
	issuer *sjwt.Issuer
	log    *slog.Logger
	seen   DeviceSeen

	// challengeKey is the 16-byte 'data' value returned with every challenge.
	//
	// On Nintendo's server this is the AES key the console needs to compute the
	// challenge MAC, and it is constant per key generation. SwitchNet never
	// checks the MAC, so the value only has to be a stable 16 bytes; it is
	// derived from the device id secret so it is stable across restarts without
	// being a hardcoded constant.
	challengeKey []byte
}

// New returns a dauth handler.
func New(cfg Config, issuer *sjwt.Issuer, log *slog.Logger, seen DeviceSeen) *Handler {
	if seen == nil {
		seen = func(string, string, string, bool) {}
	}
	// 16 bytes, derived so it is stable but not a constant in the source.
	key := make([]byte, 16)
	copy(key, sjwt.DeriveHexID(cfg.DeviceIDSecret, 16, "dauth-challenge-data"))
	return &Handler{cfg: cfg, issuer: issuer, log: log, seen: seen, challengeKey: key}
}

// Routes registers every dauth route on mux.
//
// All API versions are registered, because which one a console uses is decided
// by its firmware and the operator does not get to choose. The v2 and v4 paths
// contain the hex strings Nintendo obfuscated them with; they are part of the
// URL, not a secret.
func (h *Handler) Routes(mux *http.ServeMux) {
	const (
		v2 = "/439528b578b74475d24ec19264097f17d2cc578c8584816b644e7b7fa93044d7"
		v3 = "/v3-59ed5fa1c25bb2aea8c4d73d74b919a94d89ed48d6865b728f63547943b17404"
		v4 = "/v4-fb411cdeda62ff6da97e57c29d6300bc12b6b709869e56906aec88cb42a299cd"
	)
	prefixes := []string{"/v1", v2, v3, v4, "/v5", "/v6", "/v7", "/v8"}

	for _, p := range prefixes {
		mux.HandleFunc("POST "+p+"/challenge", h.challenge)
		mux.HandleFunc("POST "+p+"/device_auth_token", h.deviceToken)
		mux.HandleFunc("POST "+p+"/edge_token", h.edgeToken)
		// v8 renamed the token endpoints and made them plural. Registering both
		// spellings everywhere costs nothing and means a firmware we have not
		// seen still works.
		mux.HandleFunc("POST "+p+"/device_auth_tokens", h.deviceTokens)
		mux.HandleFunc("POST "+p+"/edge_tokens", h.edgeTokens)
	}

	// The JWK set. Its 'jku' is on the dcert host, which nginx routes here.
	mux.Handle("GET /keys", sjwt.JWKSHandler(sjwt.JWKSetFor("", h.issuer.Key())))
}

// ---- challenge -----------------------------------------------------------

type challengeResponse struct {
	Challenge string `json:"challenge"`
	Data      string `json:"data"`
}

// challenge issues a challenge.
//
// The challenge Nintendo issues carries a version byte, a timestamp and 29
// unknown bytes. SwitchNet builds the same 35-byte shape with the same first
// six bytes so a capture diff is readable, and fills the rest with random
// bytes. The console treats the whole thing as opaque and echoes it back.
func (h *Handler) challenge(w http.ResponseWriter, r *http.Request) {
	if _, err := parseParams(r); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidParameter)
		return
	}

	buf := make([]byte, 35)
	buf[0] = 2
	binary.BigEndian.PutUint32(buf[1:5], uint32(time.Now().Unix()))
	buf[5] = 0
	if _, err := rand.Read(buf[6:]); err != nil {
		h.log.Error("dauth: read random bytes", "error", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}

	// Nintendo stopped adding base64 padding to these two fields at the end of
	// 2024, and uses base64url. The console never decodes them, so this only
	// matters for readability against a capture.
	writeJSON(w, http.StatusOK, challengeResponse{
		Challenge: base64.RawURLEncoding.EncodeToString(buf),
		Data:      base64.RawURLEncoding.EncodeToString(h.challengeKey),
	})
}

// ---- device tokens -------------------------------------------------------

type deviceTokenResponse struct {
	ExpiresIn       int    `json:"expires_in"`
	DeviceAuthToken string `json:"device_auth_token"`
}

type tokenResult struct {
	ClientID        string `json:"client_id"`
	DeviceAuthToken string `json:"device_auth_token,omitempty"`
	DToken          string `json:"dtoken,omitempty"`
	VendorID        string `json:"vendor_id,omitempty"`
	ExpiresIn       int    `json:"expires_in"`
}

type multiTokenResponse struct {
	Results []tokenResult `json:"results"`
}

func (h *Handler) deviceToken(w http.ResponseWriter, r *http.Request) {
	params, err := parseParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidParameter)
		return
	}
	clientID := params.Get("client_id")
	if clientID == "" {
		writeError(w, http.StatusBadRequest, errInvalidParameter)
		return
	}

	dev := h.device(r, params)
	token, err := h.sign(dev, clientID)
	if err != nil {
		h.log.Error("dauth: sign device token", "error", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	h.report(dev, params)

	writeJSON(w, http.StatusOK, deviceTokenResponse{
		ExpiresIn:       int(h.cfg.TokenTTL.Seconds()),
		DeviceAuthToken: token,
	})
}

// deviceTokens is the v8 endpoint: a JSON body asking for several tokens at once.
func (h *Handler) deviceTokens(w http.ResponseWriter, r *http.Request) {
	req, dev, ok := h.parseMulti(w, r)
	if !ok {
		return
	}

	results := make([]tokenResult, 0, len(req.TokenRequests))
	for _, tr := range req.TokenRequests {
		token, err := h.sign(dev, tr.ClientID)
		if err != nil {
			h.log.Error("dauth: sign device token", "error", err)
			writeError(w, http.StatusInternalServerError, errInternal)
			return
		}
		results = append(results, tokenResult{
			ClientID:        tr.ClientID,
			DeviceAuthToken: token,
			ExpiresIn:       int(h.cfg.TokenTTL.Seconds()),
		})
	}
	writeJSON(w, http.StatusOK, multiTokenResponse{Results: results})
}

// ---- edge tokens ---------------------------------------------------------

type edgeTokenResponse struct {
	ExpiresIn int    `json:"expires_in"`
	DToken    string `json:"dtoken"`
}

// edgeToken issues an Akamai-style edge token.
//
// Edge tokens are not JWTs. They authorise a CDN download, in the shape
// "exp=…~acl=…~data=…~hmac=…". SwitchNet does not host a CDN, so the token it
// issues is well-formed and signed with its own key but points at nothing; the
// consoles that ask for one are trying to reach Nintendo's content delivery
// network, which is not part of this stack.
func (h *Handler) edgeToken(w http.ResponseWriter, r *http.Request) {
	params, err := parseParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidParameter)
		return
	}
	dev := h.device(r, params)
	writeJSON(w, http.StatusOK, edgeTokenResponse{
		ExpiresIn: int(h.cfg.TokenTTL.Seconds()),
		DToken:    h.edgeTokenValue(dev),
	})
}

func (h *Handler) edgeTokens(w http.ResponseWriter, r *http.Request) {
	req, dev, ok := h.parseMulti(w, r)
	if !ok {
		return
	}
	results := make([]tokenResult, 0, len(req.TokenRequests))
	for _, tr := range req.TokenRequests {
		results = append(results, tokenResult{
			ClientID:  tr.ClientID,
			VendorID:  tr.VendorID,
			DToken:    h.edgeTokenValue(dev),
			ExpiresIn: int(h.cfg.TokenTTL.Seconds()),
		})
	}
	writeJSON(w, http.StatusOK, multiTokenResponse{Results: results})
}

func (h *Handler) edgeTokenValue(dev sjwt.DeviceInfo) string {
	exp := time.Now().Add(h.cfg.TokenTTL).Unix()
	data := fmt.Sprintf("sub=%s.sn=%s.id=%s",
		dev.DeviceID, dev.SerialNumber, sjwt.NewTokenID())
	mac := sjwt.DeriveHexID(h.cfg.DeviceIDSecret, 32, "edge", fmt.Sprint(exp), data)
	return fmt.Sprintf("exp=%d~acl=%%2F%%2A~data=%s~hmac=%s", exp, data, mac)
}

// ---- shared --------------------------------------------------------------

// multiTokenRequest is the v8 JSON body.
type multiTokenRequest struct {
	SystemVersion string `json:"system_version"`
	FWRevision    string `json:"fw_revision"`
	IsT           bool   `json:"ist"`
	KeyGeneration int    `json:"key_generation"`
	Challenge     string `json:"challenge"`
	MAC           string `json:"mac"`
	TokenRequests []struct {
		ClientID string `json:"client_id"`
		VendorID string `json:"vendor_id"`
	} `json:"token_requests"`
}

func (h *Handler) parseMulti(w http.ResponseWriter, r *http.Request) (multiTokenRequest, sjwt.DeviceInfo, bool) {
	var req multiTokenRequest
	// 64 KiB is far more than a token request needs and stops a malformed or
	// hostile body from being read into memory unbounded.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidParameter)
		return req, sjwt.DeviceInfo{}, false
	}
	if len(req.TokenRequests) == 0 {
		writeError(w, http.StatusBadRequest, errInvalidParameter)
		return req, sjwt.DeviceInfo{}, false
	}

	dev := h.deviceFrom(r, req.SystemVersion, req.IsT)
	h.seen(dev.DeviceID, dev.SerialNumber, req.SystemVersion, req.IsT)
	return req, dev, true
}

// device builds the device identity for a form-encoded request.
func (h *Handler) device(r *http.Request, params params) sjwt.DeviceInfo {
	return h.deviceFrom(r, params.Get("system_version"), params.Get("ist") == "true")
}

// deviceFrom derives a console's identity.
//
// The identity comes from the TLS client certificate when nginx passes one
// through, because that is the only thing in a dauth request that differs
// between two consoles. Without it, every console shares one device id.
//
// That is deliberately harmless: nothing in SwitchNet authorises anything on
// the device id. Account identity is resolved from the BAAS device account, and
// only from there. A shared device id costs the operator a useful device list on
// the dashboard, nothing else. Conflating the two is how a friends system ends
// up showing every console the same account.
func (h *Handler) deviceFrom(r *http.Request, systemVersion string, ist bool) sjwt.DeviceInfo {
	fingerprint := "shared"
	if h.cfg.ClientCertHeader != "" {
		if v := r.Header.Get(h.cfg.ClientCertHeader); v != "" {
			fingerprint = v
		}
	}

	deviceID := sjwt.DeriveHexID(h.cfg.DeviceIDSecret, 8, "device", fingerprint)
	// A synthetic serial in Nintendo's format. It is not a real serial and is
	// not claimed to be: it exists because the console and the dashboard both
	// display this field.
	serial := "XAW" + strings.ToUpper(sjwt.DeriveHexID(h.cfg.DeviceIDSecret, 6, "serial", fingerprint))[:11]

	return sjwt.DeviceInfo{
		DeviceID:     deviceID,
		SerialNumber: serial,
		ProductCode:  "HAC",
		DeviceType:   "NX Prod 1",
		IsT:          ist,
		X5tS256:      h.x5tS256(r),
	}
}

// x5tS256 reads the console's TLS client certificate from the header nginx
// puts it in, unverified, and returns its SHA-256 thumbprint — see
// DeviceInfo.X5tS256. A missing header, an unparsable value, or no header
// configured all fall through to "", which omits the claim entirely rather
// than shipping a wrong one.
func (h *Handler) x5tS256(r *http.Request) string {
	if h.cfg.ClientCertPEMHeader == "" {
		return ""
	}
	escaped := r.Header.Get(h.cfg.ClientCertPEMHeader)
	if escaped == "" {
		h.log.Debug("dauth: no client certificate on request, x5t#S256 omitted", "path", r.URL.Path)
		return ""
	}
	// nginx's $ssl_client_escaped_cert is percent-encoded, not query-encoded:
	// unescape with PathUnescape so a literal '+' in the PEM's base64 body is
	// not misread as an encoded space the way QueryUnescape would.
	raw, err := url.PathUnescape(escaped)
	if err != nil {
		h.log.Warn("dauth: could not unescape client certificate header", "error", err)
		return ""
	}
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		h.log.Warn("dauth: client certificate header did not contain a PEM block", "path", r.URL.Path)
		return ""
	}
	sum := sha256.Sum256(block.Bytes)
	x5t := base64.RawURLEncoding.EncodeToString(sum[:])
	h.log.Debug("dauth: computed x5t#S256 from client certificate", "path", r.URL.Path, "x5t#S256", x5t)
	return x5t
}

func (h *Handler) report(dev sjwt.DeviceInfo, params params) {
	h.seen(dev.DeviceID, dev.SerialNumber, params.Get("system_version"), dev.IsT)
}

// sign mints one device token for one client id.
//
// Called once per requested client_id (deviceTokens loops over several for
// one console in one request), each call minting a separate token — so Esv
// is generated fresh here, per token, not once in deviceFrom and shared:
// see NewESV's own comment for why reusing one across tokens would not match
// what a real server does.
func (h *Handler) sign(dev sjwt.DeviceInfo, clientID string) (string, error) {
	now := time.Now()
	return h.issuer.Sign(sjwt.DeviceTokenClaims{
		Subject:  dev.DeviceID,
		Audience: clientID,
		IssuedAt: now.Unix(),
		Expires:  now.Add(h.cfg.TokenTTL).Unix(),
		Issuer:   h.cfg.Issuer,
		TokenID:  sjwt.NewTokenID(),
		Nintendo: sjwt.DeviceInfo{
			SerialNumber: dev.SerialNumber,
			ProductCode:  dev.ProductCode,
			DeviceType:   dev.DeviceType,
			IsT:          dev.IsT,
			X5tS256:      dev.X5tS256,
			Esv:          sjwt.NewESV(),
			// A device token's nintendo block carries no 'di': the device id is
			// the 'sub'.
		},
	})
}
