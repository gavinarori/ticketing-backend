// Package response defines the single JSON envelope every HTTP handler in
// this service replies with, so API consumers (Next.js web, React Native
// app) always parse the same shape regardless of endpoint.
package response

import (
	"encoding/json"
	"net/http"
)

// Envelope is the top-level shape of every JSON response.
//
//	Success: {"data": {...}, "meta": {...}}
//	Failure: {"error": {"code": "...", "message": "..."}}
type Envelope struct {
	Data  any        `json:"data,omitempty"`
	Meta  any        `json:"meta,omitempty"`
	Error *ErrorBody `json:"error,omitempty"`
}

// ErrorBody carries a stable machine-readable Code alongside a
// human-readable Message. Code is what client code should branch on;
// Message is for logs/debugging and may change wording over time.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// JSON writes data as a successful envelope with the given HTTP status.
func JSON(w http.ResponseWriter, status int, data any) {
	write(w, status, Envelope{Data: data})
}

// JSONWithMeta writes data plus pagination/meta info alongside it.
func JSONWithMeta(w http.ResponseWriter, status int, data, meta any) {
	write(w, status, Envelope{Data: data, Meta: meta})
}

// Error writes a failure envelope. code should be a stable, kebab-case
// identifier (e.g. "seat-already-reserved") that clients can switch on;
// it is deliberately distinct from the HTTP status code so we can convey
// domain-specific failure reasons without inventing new status codes.
func Error(w http.ResponseWriter, status int, code, message string) {
	write(w, status, Envelope{Error: &ErrorBody{Code: code, Message: message}})
}

func write(w http.ResponseWriter, status int, env Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// Encoding errors here are unrecoverable (headers already sent) — best
	// effort only, nothing meaningful to do but drop it. Callers should
	// never pass unencodable data (e.g. channels, funcs) into Envelope.
	_ = json.NewEncoder(w).Encode(env)
}
