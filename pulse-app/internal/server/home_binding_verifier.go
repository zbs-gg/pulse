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

type commandHomeBindingVerifier struct {
	node          string
	helper        string
	workspace     string
	resolverEpoch int64
	timeout       time.Duration
}

func NewCommandHomeBindingVerifier(
	node, helper, workspace string,
	resolverEpoch int64,
) (HomeBindingVerifier, error) {
	if !filepath.IsAbs(node) || !filepath.IsAbs(helper) || !filepath.IsAbs(workspace) || resolverEpoch < 1 {
		return nil, errors.New("Home binding verifier requires absolute product authority paths")
	}
	return &commandHomeBindingVerifier{
		node: filepath.Clean(node), helper: filepath.Clean(helper), workspace: filepath.Clean(workspace),
		resolverEpoch: resolverEpoch, timeout: 3 * time.Second,
	}, nil
}

func (value *commandHomeBindingVerifier) Verify(
	ctx context.Context,
	bindingDigest, repositoryID string,
) error {
	if value == nil {
		return errors.New("Home binding verifier is unavailable")
	}
	verifyCtx, cancel := context.WithTimeout(ctx, value.timeout)
	defer cancel()
	command := exec.CommandContext(
		verifyCtx, value.node, value.helper, value.workspace, bindingDigest, repositoryID,
		strconv.FormatInt(value.resolverEpoch, 10),
	)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return errors.New("Home product binding is no longer current")
	}
	return nil
}
