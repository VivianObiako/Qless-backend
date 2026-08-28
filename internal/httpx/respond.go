// Package httpx holds the transport plumbing shared by every handler: JSON
// encoding, request decoding with sane limits, and the middleware chain.
package httpx

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

const maxRequestBytes = 16 * 1024

// JSON writes a typed payload. Encoding failures are logged rather than
// returned: by the time encoding runs the status line is already on the wire.
func JSON[T any](w http.ResponseWriter, status int, payload T) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("write json response", "error", err)
	}
}

func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// ErrorBody is the single error shape the frontend has to understand. The
// message is always written for a person; driver and constraint detail stays
// in the server log.
type ErrorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func WriteError(w http.ResponseWriter, status int, code, message string) {
	JSON(w, status, ErrorBody{Error: code, Message: message})
}

// DecodeJSON reads a size-limited body and rejects unknown fields so typos in
// a client payload surface immediately instead of being silently dropped.
func DecodeJSON[T any](w http.ResponseWriter, r *http.Request, dst *T) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("decode request body: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("request body must contain a single JSON object")
	}
	return nil
}
