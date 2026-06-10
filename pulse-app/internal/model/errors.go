package model

import "errors"

var (
	ErrAliasNotFound         = errors.New("model: alias not found")
	ErrPolicyViolation       = errors.New("model: policy violation")
	ErrUnsafeBaseURL         = errors.New("model: unsafe base url")
	ErrProviderUnavailable   = errors.New("model: provider unavailable")
	ErrDORequiredUnavailable = errors.New("model: DigitalOcean is required for this model and unavailable")
)
