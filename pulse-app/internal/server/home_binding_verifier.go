package server

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

type HomeBindingVerifier interface {
	Verify(context.Context, string, string) error
}

// ProductBindingVerifier re-reads the signed workspace registry for a
// request-selected Personal project. The daemon never trusts repository or
// namespace identifiers supplied by a caller without this verification.
type ProductBindingVerifier interface {
	VerifyBinding(context.Context, string, string, string, int64) error
}

type commandProductBindingVerifier struct {
	node    string
	helper  string
	timeout time.Duration
}

type commandHomeBindingVerifier struct {
	product       ProductBindingVerifier
	workspace     string
	resolverEpoch int64
}

func NewCommandProductBindingVerifier(node, helper string) (ProductBindingVerifier, error) {
	if !filepath.IsAbs(node) || !filepath.IsAbs(helper) {
		return nil, errors.New("Product binding verifier requires absolute authority paths")
	}
	return &commandProductBindingVerifier{
		node: filepath.Clean(node), helper: filepath.Clean(helper), timeout: 3 * time.Second,
	}, nil
}

func NewCommandHomeBindingVerifier(
	node, helper, workspace string,
	resolverEpoch int64,
) (HomeBindingVerifier, error) {
	if !filepath.IsAbs(node) || !filepath.IsAbs(helper) || !filepath.IsAbs(workspace) || resolverEpoch < 1 {
		return nil, errors.New("Home binding verifier requires absolute product authority paths")
	}
	product, err := NewCommandProductBindingVerifier(node, helper)
	if err != nil {
		return nil, err
	}
	return &commandHomeBindingVerifier{
		product: product, workspace: filepath.Clean(workspace), resolverEpoch: resolverEpoch,
	}, nil
}

func (value *commandProductBindingVerifier) VerifyBinding(
	ctx context.Context,
	workspace, bindingDigest, repositoryID string,
	resolverEpoch int64,
) error {
	if value == nil || !filepath.IsAbs(workspace) || resolverEpoch < 1 {
		return errors.New("Product binding verifier is unavailable")
	}
	verifyCtx, cancel := context.WithTimeout(ctx, value.timeout)
	defer cancel()
	command := exec.CommandContext(
		verifyCtx, value.node, value.helper, filepath.Clean(workspace), bindingDigest, repositoryID,
		strconv.FormatInt(resolverEpoch, 10),
	)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return errors.New("Product binding is no longer current")
	}
	return nil
}

func (value *commandHomeBindingVerifier) Verify(
	ctx context.Context,
	bindingDigest, repositoryID string,
) error {
	if value == nil {
		return errors.New("Home binding verifier is unavailable")
	}
	if value.product == nil || value.product.VerifyBinding(
		ctx, value.workspace, bindingDigest, repositoryID, value.resolverEpoch,
	) != nil {
		return errors.New("Home product binding is no longer current")
	}
	return nil
}
