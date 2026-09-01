package server

import (
	"fmt"
	"net/http"
	"slices"
)

const supportedHostLifecycleReadinessSchema = "pulse.supported_host_lifecycle_readiness.v1"

var supportedProductHosts = []string{"claude-code", "codex", "cursor", "opencode"}

type supportedHostLifecycleState struct {
	Host           string   `json:"host"`
	State          string   `json:"state"`
	LifecycleReady bool     `json:"lifecycle_ready"`
	Milestones     []string `json:"milestones"`
	ObjectID       string   `json:"object_id,omitempty"`
	ContextID      string   `json:"context_id,omitempty"`
	LastWriteAt    string   `json:"last_write_at,omitempty"`
	LastRecallAt   string   `json:"last_recall_at,omitempty"`
	RecallCount    int      `json:"recall_count"`
	DeliveryProof  string   `json:"delivery_proof"`
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
		for _, memory := range hostMemories {
			if memory.CreatedAt > state.LastWriteAt {
				state.LastWriteAt = memory.CreatedAt
			}
		}
		for _, delivery := range hostDeliveries {
			if delivery.CreatedAt > state.LastRecallAt {
				state.LastRecallAt = delivery.CreatedAt
				state.RecallCount = len(delivery.ObjectIDs)
				state.DeliveryProof = delivery.Acknowledgement
			}
		}
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

func (s *Server) supportedHostLifecycleReadiness() (supportedHostLifecycleReadiness, error) {
	bindingDigest, repositoryID, ok := s.cfg.Store.ProductRuntimeBoundary()
	if !ok {
		return supportedHostLifecycleReadiness{}, fmt.Errorf("product lifecycle boundary unavailable")
	}
	memories, err := s.cfg.Store.TerminalMemoryReadinessFacts(repositoryID, bindingDigest, 100)
	if err != nil {
		return supportedHostLifecycleReadiness{}, fmt.Errorf("product lifecycle memory facts unavailable")
	}
	deliveryFacts, err := s.cfg.Store.ReadMemoryHomeDeliveryFacts(repositoryID, bindingDigest, 100)
	if err != nil {
		return supportedHostLifecycleReadiness{}, fmt.Errorf("product lifecycle delivery facts unavailable")
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
	result := projectSupportedHostLifecycleReadiness(memories, deliveries)
	activity, err := readMemoryActivity(s.cfg.MemoryActivityPath)
	if err != nil {
		return supportedHostLifecycleReadiness{}, fmt.Errorf("product memory activity unavailable")
	}
	for index := range result.Hosts {
		host := &result.Hosts[index]
		recall, ok := activity.Hosts[host.Host]
		if ok && recall.RepositoryID == repositoryID && recall.RecalledAt > host.LastRecallAt {
			host.LastRecallAt = recall.RecalledAt
			host.RecallCount = recall.ResultCount
			host.DeliveryProof = "pulse_delivery_receipt"
			host.Milestones = append(host.Milestones, "automatic_recall")
		}
		host.LifecycleReady = host.LastWriteAt != "" && host.LastRecallAt != ""
		if host.LifecycleReady {
			host.State = "ready"
		} else if host.LastWriteAt != "" {
			host.State = "automatic_recall_pending"
		} else if host.LastRecallAt != "" {
			host.State = "durable_write_pending"
		} else {
			host.State = "first_activity_pending"
		}
	}
	return result, nil
}

func (s *Server) handleSupportedHostLifecycleReadiness(w http.ResponseWriter, r *http.Request) {
	result, err := s.supportedHostLifecycleReadiness()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, result)
}
