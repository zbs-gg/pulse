package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

const openCodeFunFactCandidatesSchema = "pulse.opencode_fun_fact_candidates.v1"

type openCodeFunFactCandidate struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type openCodeFunFactCandidates struct {
	Schema          string                     `json:"schema"`
	Candidates      []openCodeFunFactCandidate `json:"candidates"`
	CandidateDigest string                     `json:"candidate_digest"`
}

func openCodeCandidateID(text string) string {
	digest := sha256.Sum256([]byte("pulse-opencode-fun-fact-v1\x1f" + text))
	return "fact_" + hex.EncodeToString(digest[:12])
}

func projectOpenCodeFunFactCandidates(values []string) openCodeFunFactCandidates {
	if len(values) > 6 {
		values = values[:6]
	}
	candidates := make([]openCodeFunFactCandidate, 0, len(values))
	digestInput := make([]string, 0, len(values))
	for _, text := range values {
		id := openCodeCandidateID(text)
		candidates = append(candidates, openCodeFunFactCandidate{ID: id, Text: text})
		digestInput = append(digestInput, id+"\x1f"+text)
	}
	digest := sha256.Sum256([]byte(strings.Join(digestInput, "\x1e")))
	return openCodeFunFactCandidates{
		Schema: openCodeFunFactCandidatesSchema, Candidates: candidates,
		CandidateDigest: hex.EncodeToString(digest[:]),
	}
}

func (s *Server) handleOpenCodeFunFactCandidates(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireProductBindingAuthority(w, r); !ok {
		return
	}
	host, ok := exactSingleHeader(r, "X-Pulse-Product-Host")
	if !ok || host != "opencode" {
		http.Error(w, "product host is invalid", http.StatusBadRequest)
		return
	}
	values, err := s.cfg.Store.FunFactCandidates(6)
	if err != nil {
		http.Error(w, "fun fact candidates are unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, projectOpenCodeFunFactCandidates(values))
}
