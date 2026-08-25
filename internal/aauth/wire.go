package aauth

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// maxBodyBytes caps an aauth body. A digital-title request carries an encrypted
// ticket and its wrapped key and runs to a couple of kilobytes; 256 KiB is
// generous and bounds what a malformed client can make the server allocate.
const maxBodyBytes = 256 << 10

type form url.Values

func (f form) Get(key string) string { return url.Values(f).Get(key) }

// parseForm reads a form-encoded aauth body.
//
// Up to system version 17.0.1 the console prefixes the challenge request body
// with a stray '&', so the first parameter parses as an empty key. Trimming it
// is not a workaround for a bug of ours — it is what the console sends.
func parseForm(r *http.Request) (form, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	values := r.PostForm
	delete(values, "")
	return form(values), nil
}

// Nintendo's aauth error codes.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/AAuth-Server#known-errors
//
// Only the codes SwitchNet can produce are listed. It never bans a ROM id or
// refuses an unauthorised application, because it does not check ownership.
var (
	errDeviceTokenExpired = apiError{Code: "0103", Message: "Device token expired."}
	errInternal           = apiError{Code: "0112", Message: "Internal Server Error."}
	errInvalidParameter   = apiError{Code: "0118", Message: "Invalid parameter in request."}
	errAPINotFound        = apiError{Code: "0118", Message: "Invalid parameter in request."}
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
		http.Error(w, `{"errors":[{"code":"0112","message":"Internal Server Error."}]}`,
			http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(status)
	_, _ = w.Write(buf)
}

// NotFound answers an unrouted path with an aauth-shaped error.
func NotFound(w http.ResponseWriter, r *http.Request) {
	// A trailing-slash probe is common from tooling and is not worth a warning.
	if strings.TrimSuffix(r.URL.Path, "/") == "" {
		writeError(w, http.StatusBadRequest, errAPINotFound)
		return
	}
	writeError(w, http.StatusBadRequest, errAPINotFound)
}
