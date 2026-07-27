package server

import (
	"encoding/base64"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	productWorkspaceHeader     = "X-Pulse-Product-Workspace"
	productBindingHeader       = "X-Pulse-Product-Binding"
	productRepositoryHeader    = "X-Pulse-Product-Repository"
	productResolverEpochHeader = "X-Pulse-Product-Resolver-Epoch"
	maxProductWorkspaceBytes   = 4096
)

type productBindingAuthority struct {
	Workspace     string
	BindingDigest string
	RepositoryID  string
	ResolverEpoch int64
}

func validProductBindingAuthority(authority productBindingAuthority) bool {
	if !filepath.IsAbs(authority.Workspace) || filepath.Clean(authority.Workspace) != authority.Workspace ||
		len(authority.BindingDigest) != 64 || !strings.HasPrefix(authority.RepositoryID, "repository_") ||
		len(authority.RepositoryID) > 255 || authority.ResolverEpoch < 1 {
		return false
	}
	for _, character := range authority.BindingDigest {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	for _, character := range authority.RepositoryID {
		if character <= 0x20 || character == 0x7f || strings.ContainsRune(`/\\`, character) {
			return false
		}
	}
	return true
}

func exactSingleHeader(r *http.Request, name string) (string, bool) {
	values := r.Header.Values(name)
	if len(values) != 1 || values[0] == "" || strings.TrimSpace(values[0]) != values[0] {
		return "", false
	}
	return values[0], true
}

func decodeProductBindingAuthority(r *http.Request) (productBindingAuthority, error) {
	workspaceEncoded, ok := exactSingleHeader(r, productWorkspaceHeader)
	if !ok || len(workspaceEncoded) > base64.RawURLEncoding.EncodedLen(maxProductWorkspaceBytes) {
		return productBindingAuthority{}, errors.New("product workspace authority is invalid")
	}
	workspaceBytes, err := base64.RawURLEncoding.DecodeString(workspaceEncoded)
	if err != nil || len(workspaceBytes) < 1 || len(workspaceBytes) > maxProductWorkspaceBytes ||
		strings.ContainsRune(string(workspaceBytes), 0) {
		return productBindingAuthority{}, errors.New("product workspace authority is invalid")
	}
	workspace := string(workspaceBytes)
	if !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace {
		return productBindingAuthority{}, errors.New("product workspace authority is invalid")
	}
	bindingDigest, bindingOK := exactSingleHeader(r, productBindingHeader)
	repositoryID, repositoryOK := exactSingleHeader(r, productRepositoryHeader)
	epochText, epochOK := exactSingleHeader(r, productResolverEpochHeader)
	epoch, epochErr := strconv.ParseInt(epochText, 10, 64)
	if !bindingOK || !repositoryOK || !epochOK || epochErr != nil || epoch < 1 {
		return productBindingAuthority{}, errors.New("product binding authority is invalid")
	}
	authority := productBindingAuthority{
		Workspace: workspace, BindingDigest: bindingDigest, RepositoryID: repositoryID,
		ResolverEpoch: epoch,
	}
	if !validProductBindingAuthority(authority) {
		return productBindingAuthority{}, errors.New("product binding authority is invalid")
	}
	return authority, nil
}

func (s *Server) requireProductBindingAuthority(
	w http.ResponseWriter,
	r *http.Request,
) (productBindingAuthority, bool) {
	authority, err := decodeProductBindingAuthority(r)
	if err == nil && s != nil && s.cfg.ProductBindingVerifier != nil {
		err = s.cfg.ProductBindingVerifier.VerifyBinding(
			r.Context(), authority.Workspace, authority.BindingDigest,
			authority.RepositoryID, authority.ResolverEpoch,
		)
	}
	if err != nil || s == nil || s.cfg.ProductBindingVerifier == nil {
		http.Error(w, "product binding authority mismatch", http.StatusForbidden)
		return productBindingAuthority{}, false
	}
	return authority, true
}
