package server

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrContextLeaseStale           = errors.New("context lease is stale")
	ErrDeskUnavailable             = errors.New("bound desk is unavailable")
	ErrMandatoryContextUnavailable = errors.New("mandatory commons context is unavailable")
)

// ContextLease is a short-lived capability for one already-resolved binding.
// It contains no caller-selectable team, vault, role, or audience fields.
type ContextLease struct {
	BindingID     string    `json:"binding_id"`
	ResolverEpoch uint64    `json:"resolver_epoch"`
	ExpiresAt     time.Time `json:"expires_at"`
}

type BoundContextRequest struct {
	DeskQuery    string       `json:"desk_query,omitempty"`
	CommonsQuery string       `json:"commons_query,omitempty"`
	Mandatory    bool         `json:"mandatory"`
	Lease        ContextLease `json:"lease"`
}

type BoundContextItem struct {
	ObjectID string `json:"object_id"`
	StoreID  string `json:"store_id"`
	Text     string `json:"text,omitempty"`
}

// BoundContextReader is constructed with one fixed store. No read call can
// name another principal, vault, team, audience, or visibility target.
type BoundContextReader interface {
	StoreID() string
	ReadBoundContext(context.Context, BoundContextRequest) ([]BoundContextItem, error)
}

type LocalCompositorConfig struct {
	BindingID      string
	ResolverEpoch  uint64
	DeskStoreID    string
	CommonsStoreID string
	Desk           BoundContextReader
	Commons        BoundContextReader
	Now            func() time.Time
}

type CompositeContext struct {
	BindingID       string             `json:"binding_id"`
	ResolverEpoch   uint64             `json:"resolver_epoch"`
	Desk            []BoundContextItem `json:"desk"`
	Commons         []BoundContextItem `json:"commons"`
	DegradedReasons []string           `json:"degraded_reasons"`
	Fallback        bool               `json:"fallback"`
}

type LocalCompositor struct {
	bindingID      string
	resolverEpoch  uint64
	deskStoreID    string
	commonsStoreID string
	desk           BoundContextReader
	commons        BoundContextReader
	now            func() time.Time
}

func NewLocalCompositor(cfg LocalCompositorConfig) (*LocalCompositor, error) {
	if cfg.BindingID == "" || cfg.ResolverEpoch < 1 || cfg.DeskStoreID == "" || cfg.CommonsStoreID == "" ||
		cfg.DeskStoreID == cfg.CommonsStoreID || cfg.Desk == nil || cfg.Commons == nil {
		return nil, errors.New("local compositor requires one exact Desk and one exact Commons binding")
	}
	if cfg.Desk.StoreID() != cfg.DeskStoreID || cfg.Commons.StoreID() != cfg.CommonsStoreID {
		return nil, errors.New("local compositor reader store identity mismatch")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &LocalCompositor{
		bindingID: cfg.BindingID, resolverEpoch: cfg.ResolverEpoch,
		deskStoreID: cfg.DeskStoreID, commonsStoreID: cfg.CommonsStoreID,
		desk: cfg.Desk, commons: cfg.Commons, now: cfg.Now,
	}, nil
}

func (c *LocalCompositor) Query(ctx context.Context, request BoundContextRequest) (CompositeContext, error) {
	result := CompositeContext{
		BindingID: c.bindingID, ResolverEpoch: c.resolverEpoch,
		Desk: []BoundContextItem{}, Commons: []BoundContextItem{},
		DegradedReasons: []string{}, Fallback: false,
	}
	if request.Lease.BindingID != c.bindingID || request.Lease.ResolverEpoch != c.resolverEpoch ||
		request.Lease.ExpiresAt.IsZero() || !request.Lease.ExpiresAt.After(c.now()) {
		return result, ErrContextLeaseStale
	}

	deskRequest := request
	deskRequest.CommonsQuery = ""
	deskItems, err := c.desk.ReadBoundContext(ctx, deskRequest)
	if err != nil {
		return result, fmt.Errorf("%w", ErrDeskUnavailable)
	}
	if err := validateBoundItems(deskItems, c.deskStoreID); err != nil {
		return result, err
	}
	result.Desk = deskItems

	commonsRequest := request
	commonsRequest.DeskQuery = ""
	commonsItems, err := c.commons.ReadBoundContext(ctx, commonsRequest)
	if err != nil {
		if request.Mandatory {
			return CompositeContext{
				BindingID: c.bindingID, ResolverEpoch: c.resolverEpoch,
				Desk: []BoundContextItem{}, Commons: []BoundContextItem{},
				DegradedReasons: []string{"commons_unavailable"}, Fallback: false,
			}, ErrMandatoryContextUnavailable
		}
		result.DegradedReasons = append(result.DegradedReasons, "commons_unavailable")
		return result, nil
	}
	if err := validateBoundItems(commonsItems, c.commonsStoreID); err != nil {
		return CompositeContext{}, err
	}
	result.Commons = commonsItems
	return result, nil
}

func validateBoundItems(items []BoundContextItem, expectedStoreID string) error {
	for _, item := range items {
		if item.ObjectID == "" || item.StoreID != expectedStoreID {
			return errors.New("bound context reader returned foreign or unidentified content")
		}
	}
	return nil
}
