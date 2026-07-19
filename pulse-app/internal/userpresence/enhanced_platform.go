package userpresence

import (
	"context"
	"io"
	"time"
)

type platformEnhancedPresenceInspection func(context.Context) error

func newInspectedPlatformEnhancedAuthorizer(
	ctx context.Context,
	inspect platformEnhancedPresenceInspection,
	prover Prover,
	now func() time.Time,
	randomSource io.Reader,
) EnhancedPresenceAuthorizer {
	unavailable := NewUnavailableAuthorizer("enhanced_presence_unavailable")
	if inspect == nil || prover == nil {
		return unavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := inspect(ctx); err != nil {
		return unavailable
	}
	gate, err := NewGate(prover, now)
	if err != nil {
		return unavailable
	}
	authorizer, err := NewSynchronousGateAuthorizer(gate, now, randomSource)
	if err != nil {
		return unavailable
	}
	return authorizer
}
