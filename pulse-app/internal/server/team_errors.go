package server

import (
	"encoding/json"
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
