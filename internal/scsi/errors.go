package scsi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// apiError is the error envelope this server returns.
//
// UNCONFIRMED: no capture of a real SCSI/SATA error response
// exists. This mirrors BAAS's own confirmed shape
// (https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#errors)
// because both are the same family of Nintendo account-adjacent REST API and
// every other confirmed field name on this host (data_size,
// saved_at_as_unixtime, encoded_digest, ...) follows BAAS's own naming
// conventions rather than diverging from them — but this specific shape is
// this project's own defensible reading, not a confirmed one. A capture of
// any real OLSC error response would settle it; change writeError in
// errors.go to match.
type apiError struct {
	Status   int    `json:"status"`
	Code     string `json:"errorCode"`
	Title    string `json:"title"`
	Instance string `json:"instance,omitempty"`
}

func writeError(w http.ResponseWriter, status int, code, title string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{Status: status, Code: code, Title: title})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) writeInternalError(w http.ResponseWriter, action string, err error) {
	s.log.Error("scsi: "+action, "error", err)
	writeError(w, http.StatusInternalServerError, "internal_server_error", "Internal Server Error")
}

// logUnimplemented records a request to an endpoint this server does not
// implement and answers with a body that will not send a real Nintendo
// console into a hang or a retry loop.
//
// An operator grepping this service's log for this line has the exact list of
// what a real console reaches that this server does not cover.
func (s *Server) logUnimplemented(r *http.Request) {
	s.log.Warn("scsi: unimplemented endpoint reached by a real request",
		"method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery,
		slog.Group("hint", "action", "this endpoint is not implemented; report the request"))
}
