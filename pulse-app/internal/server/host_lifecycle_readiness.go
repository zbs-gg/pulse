package server

import (
	"net/http"
	"slices"
)

const supportedHostLifecycleReadinessSchema = "pulse.supported_host_lifecycle_readiness.v1"

var supportedProductHosts = []string{"claude-code", "codex", "cursor"}

type supportedHostLifecycleState struct {
	Host           string   `json:"host"`
	State          string   `json:"state"`
	LifecycleReady bool     `json:"lifecycle_ready"`
	Milestones     []string `json:"milestones"`
	ObjectID       string   `json:"object_id,omitempty"`
	ContextID      string   `json:"context_id,omitempty"`
}

type supportedHostLifecycleReadiness struct {
	Schema string                        `json:"schema"`
	Hosts  []supportedHostLifecycleState `json:"hosts"`
}

func projectSupportedHostLifecycleReadiness(
	memories []TerminalMemoryReadinessFact,
	deliveries []ContextDeliveryReadinessFact,
) supportedHostLifecycleReadiness {
	result := supportedHostLifecycleReadiness{
		Schema: supportedHostLifecycleReadinessSchema,
		Hosts:  make([]supportedHostLifecycleState, 0, len(supportedProductHosts)),
	}
	for _, host := range supportedProductHosts {
		hostMemories := make([]TerminalMemoryReadinessFact, 0)
		for _, memory := range memories {
			if memory.Host == host {
				hostMemories = append(hostMemories, memory)
			}
		}
		hostDeliveries := make([]ContextDeliveryReadinessFact, 0)
		for _, delivery := range deliveries {
			if delivery.Host == host {
				hostDeliveries = append(hostDeliveries, delivery)
			}
		}
		projected := ProjectReadinessLifecycleInputs(hostMemories, hostDeliveries)
		state := supportedHostLifecycleState{Host: host, State: projected.State, Milestones: []string{}}
		if projected.TerminalMemory != nil {
			state.Milestones = append(state.Milestones, "write_receipt")
			state.ObjectID = projected.TerminalMemory.ObjectID
		}
		if projected.OfferedToHost != nil {
			state.Milestones = append(state.Milestones, "session_context")
			state.ContextID = projected.OfferedToHost.ContextID
		}
		if projected.HostObserved != nil {
			state.Milestones = append(state.Milestones, "prompt_context")
		}
		state.LifecycleReady = projected.State == "ready"
		result.Hosts = append(result.Hosts, state)
	}
	return result
}

func (s *Server) handleSupportedHostLifecycleReadiness(w http.ResponseWriter, r *http.Request) {
	bindingDigest, repositoryID, ok := s.cfg.Store.ProductRuntimeBoundary()
	if !ok {
		http.Error(w, "product lifecycle boundary unavailable", http.StatusServiceUnavailable)
		return
	}
	memories, err := s.cfg.Store.TerminalMemoryReadinessFacts(repositoryID, bindingDigest, 100)
	if err != nil {
		http.Error(w, "product lifecycle memory facts unavailable", http.StatusServiceUnavailable)
		return
	}
	deliveryFacts, err := s.cfg.Store.ReadMemoryHomeDeliveryFacts(repositoryID, bindingDigest, 100)
	if err != nil {
		http.Error(w, "product lifecycle delivery facts unavailable", http.StatusServiceUnavailable)
		return
	}
	deliveries := make([]ContextDeliveryReadinessFact, 0, len(deliveryFacts))
	for _, fact := range deliveryFacts {
		if !slices.Contains(supportedProductHosts, fact.Host) {
			continue
		}
		deliveries = append(deliveries, ContextDeliveryReadinessFact{
			ContextID: fact.ContextID, Acknowledgement: fact.Acknowledgement, Purpose: fact.Purpose,
			ObjectIDs: append([]string(nil), fact.ObjectIDs...), EvidenceIDs: append([]string(nil), fact.EvidenceIDs...),
			PayloadDigest: fact.PayloadDigest, BindingDigest: fact.BindingDigest,
			RepositoryID: fact.RepositoryID, Host: fact.Host, SessionRef: fact.SessionRef,
			CreatedAt: fact.CreatedAt,
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, projectSupportedHostLifecycleReadiness(memories, deliveries))
}
