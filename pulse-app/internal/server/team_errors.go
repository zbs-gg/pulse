package server

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
)

const (
	teamErrorUnauthorized            = "unauthorized"
	teamErrorNotReady                = "team_not_ready"
	teamErrorSharedMemoryUnavailable = "shared_memory_unavailable"
	teamErrorPrincipalUnavailable    = "principal_store_unavailable"
)

type teamErrorResponse struct {
	Error    string `json:"error"`
	Fallback *bool  `json:"fallback,omitempty"`
}

func writeTeamError(w http.ResponseWriter, status int, code string, includeFallback bool) {
	response := teamErrorResponse{Error: code}
	if includeFallback {
		fallback := false
		response.Fallback = &fallback
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func readTeamJSONBody(
	w http.ResponseWriter,
	r *http.Request,
	maxBytes int64,
	invalidCode string,
) ([]byte, bool) {
	if r.URL.RawQuery != "" || r.Header.Get("Content-Encoding") != "" {
		writeTeamError(w, http.StatusBadRequest, invalidCode, true)
		return nil, false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeTeamError(w, http.StatusBadRequest, invalidCode, true)
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > maxBytes {
		writeTeamError(w, http.StatusBadRequest, invalidCode, true)
		return nil, false
	}
	return raw, true
}
