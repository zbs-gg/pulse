//go:build !darwin

package userpresence

import "context"

type unsupportedProver struct{}

func NewPlatformProver() Prover { return unsupportedProver{} }

func (unsupportedProver) Prove(context.Context, Challenge) error { return ErrPresenceDenied }
