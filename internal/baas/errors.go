package baas

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// APIError is a BAAS error response.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#errors
//
// The console reads errorCode, not the HTTP status, to decide what to tell the
// user, so returning the right code matters more than the right status. A wrong
// code shows the player a generic "could not connect" where a specific message
// would have told them what to fix.
type APIError struct {
	Status    int    `json:"status"`
	ErrorCode string `json:"errorCode"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	Instance  string `json:"instance"`
	Type      string `json:"type"`
}

// Error implements error.
func (e APIError) Error() string { return e.ErrorCode + ": " + e.Title }

// errorTemplate is an error before the request path is filled in.
type errorTemplate struct {
	status int
	code   string
	title  string
	detail string
}

// at returns a concrete error for one request path.
func (t errorTemplate) at(instance string) APIError {
	return APIError{
		Status:    t.status,
		ErrorCode: t.code,
		Title:     t.title,
		Detail:    t.detail,
		Instance:  instance,
		Type:      "https://baas.nintendo.com/errors/1.0.0/" + http.StatusText(t.status),
	}
}

// The errors SwitchNet can produce.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#known-errors
var (
	errResourceNotFound = errorTemplate{404, "resource_is_not_found",
		"Specified resource is not found", "Resource is not found"}
	errMethodNotAllowed = errorTemplate{405, "method_not_allowed",
		"Method Not Allowed", "Method Not Allowed"}
	errInvalidParams = errorTemplate{400, "invalid_params",
		"Invalid Params", "invalid params"}
	errInvalidRequest = errorTemplate{400, "invalid_request",
		"Authorization header value is invalid", "Auth scheme or auth params is invalid"}
	errInvalidDeviceAccount = errorTemplate{400, "invalid_device_account",
		"Invalid Device Account", "Device Account's id or password is invalid"}
	errInvalidAppAuthNToken = errorTemplate{400, "invalid_ndas_app_authn_token",
		"Invalid NDAS App AuthN Token", ""}
	errInvalidFriendCodeFormat = errorTemplate{400, "invalid_friend_code_format",
		"Invalid Friend Code Format", "invalid friend code format"}
	errInvalidRawContent = errorTemplate{400, "invalid_raw_content",
		"Invalid Raw Content", "invalid raw content"}
	errInvalidToken = errorTemplate{401, "invalid_token",
		"Token is invalid", "The access token was invalid"}
	errInsufficientScope = errorTemplate{403, "insufficient_scope",
		"Token is insufficient", "The access token does not have sufficient scope"}
	errForbidden = errorTemplate{403, "forbidden",
		"Forbidden", "forbidden"}
	errUnavailableDeviceAccount = errorTemplate{403, "unavailable_device_account",
		"Unavailable Device Account", "Device Account is unavailable"}
	errDeletedUser = errorTemplate{404, "deleted_user",
		"Deleted User", "user is deleted"}
	errResourceAlreadyExists = errorTemplate{409, "resource_already_exists",
		"Resource Already Exists", "resource already exists"}
	errUnsupportedMediaType = errorTemplate{415, "unsupported_media_type",
		"Unsupported Media Type", "unsupported media type"}
	errFriendCodeUnregenerable = errorTemplate{422, "friend_code_unregenerable_state",
		"Friend Code Unregenerable State", ""}
	errInternalServerError = errorTemplate{500, "internal_server_error",
		"Internal Server Error", "Internal Server Error"}
)

// writeError sends a BAAS error response.
func writeError(w http.ResponseWriter, r *http.Request, t errorTemplate) {
	writeJSON(w, t.status, t.at(r.URL.Path))
}

// writeJSON sends a JSON response with the headers the console expects.
func writeJSON(w http.ResponseWriter, status int, body any) {
	buf, err := json.Marshal(body)
	if err != nil {
		http.Error(w, `{"status":500,"errorCode":"internal_server_error"}`,
			http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Account and friend data must never be cached by anything in the path: a
	// stale friend list is indistinguishable from a broken one.
	w.Header().Set("Cache-Control", "no-store, no-cache")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_, _ = w.Write(buf)
}

// writeRateLimited answers a throttled login attempt.
//
// Deliberately not one of the BAAS-shaped errors above: there is no
// Nintendo-documented errorCode for this, because a real console never
// triggers it against real Nintendo servers. Inventing a plausible-looking
// one would risk it being mistaken for a real Nintendo error code by anyone
// reading this server's behaviour later. A plain HTTP 429 is what this
// actually is: this server's own rate limit, not a BAAS error shape.
func writeRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
	http.Error(w, "too many login attempts", http.StatusTooManyRequests)
}

// writeInternalError logs the cause and returns a generic error.
//
// The cause never reaches the console: a database error message in a response
// body tells a player nothing and tells anybody else on the network too much.
func writeInternalError(log *slog.Logger, w http.ResponseWriter, r *http.Request, msg string, err error) {
	log.Error("baas: "+msg, "error", err, "path", r.URL.Path, "method", r.Method)
	writeError(w, r, errInternalServerError)
}
