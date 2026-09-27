package dauth

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// params is the parsed request body, whichever encoding it arrived in.
type params url.Values

// Get returns one parameter.
func (p params) Get(key string) string { return url.Values(p).Get(key) }

// maxBodyBytes caps a form-encoded dauth body. The largest real request is a
// token request carrying a system version digest and a MAC; 128 KiB is generous
// and bounds what a malformed client can make the server allocate.
const maxBodyBytes = 128 << 10

// parseParams reads a dauth request body.
//
// Up to API v7 the body is form-encoded. From v8, the same endpoints also
// accept JSON. Accepting either here means the version-specific handlers do not
// each have to branch on the content type.
func parseParams(r *http.Request) (params, error) {
	ct := r.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}

	if strings.TrimSpace(ct) == "application/json" {
		var raw map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes)).Decode(&raw); err != nil {
			return nil, err
		}
		values := url.Values{}
		for k, v := range raw {
			switch v := v.(type) {
			case string:
				values.Set(k, v)
			case bool:
				if v {
					values.Set(k, "true")
				} else {
					values.Set(k, "false")
				}
			}
		}
		return params(values), nil
	}

	r.Body = http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	return params(r.PostForm), nil
}

// Nintendo's dauth error codes.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/DAuth-Server#known-errors
//
// Only the codes GRID0+ can actually produce are listed. The rest describe
// checks GRID0+ does not perform — banned devices, invalid certificates,
// expired challenges — and inventing responses for them would be misleading.
var (
	errInternal         = apiError{Code: "0009", Message: "Internal Server Error."}
	errInvalidParameter = apiError{Code: "0014", Message: "Invalid parameter in request."}
	errAPINotFound      = apiError{Code: "0012", Message: "API not found."}
)

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	Errors []apiError `json:"errors"`
}

func writeError(w http.ResponseWriter, status int, errs ...apiError) {
	writeJSON(w, status, errorResponse{Errors: errs})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	buf, err := json.Marshal(body)
	if err != nil {
		http.Error(w, `{"errors":[{"code":"0009","message":"Internal Server Error."}]}`,
			http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(status)
	_, _ = w.Write(buf)
}

// NotFound answers an unrouted path the way Nintendo's server does, so a
// console (or a capture diff) sees a dauth-shaped error rather than Go's
// default plain-text 404.
func NotFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusBadRequest, errAPINotFound)
}
