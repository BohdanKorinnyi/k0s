package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// maxBody is the largest request body accepted. Bodies here are small JSON
// documents; the largest is a host with three PEM certificates.
const maxBody = 1 << 20

// writeJSON sends v as the response body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("write response", "error", err)
	}
}

// writeError sends the API's one error shape: a message for people and a code
// for the frontend to branch on.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": message, "code": code})
}

// readJSON decodes the request body into v. When it fails it has already sent
// the error response and returns false.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "the request is too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid", "the request body is not valid JSON")
		return false
	}
	return true
}
