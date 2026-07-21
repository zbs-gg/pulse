// Package teamauth contains transport-independent team identity primitives.
// External claims are converted to opaque, domain-separated keys before they
// cross the store boundary.
package teamauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"strings"
)

const SchemaVersion = 44

var ErrIncompleteBootstrapRoot = errors.New("bootstrap root requires issuer, subject, and admin client binding")

// BootstrapRoot is deployment-pinned configuration, never a value discovered
// from the first request that reaches an empty store.
type BootstrapRoot struct {
	Issuer        string
	Subject       string
	AdminClientID string
}

func (r BootstrapRoot) Validate() error {
	if !isExactIdentityValue(r.Issuer) || !isExactIdentityValue(r.Subject) || !isExactIdentityValue(r.AdminClientID) {
		return ErrIncompleteBootstrapRoot
	}
	return nil
}

func isExactIdentityValue(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}

func (r BootstrapRoot) Matches(other BootstrapRoot) bool {
	a, errA := r.Fingerprint()
	b, errB := other.Fingerprint()
	return errA == nil && errB == nil && hmac.Equal([]byte(a), []byte(b))
}

func (r BootstrapRoot) Fingerprint() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	return opaqueKey("bootstrap-root-v1", r.Issuer, r.Subject, r.AdminClientID), nil
}

func HumanIdentityKey(issuer, subject string) string {
	return opaqueKey("human-identity-v1", issuer, subject)
}

func OAuthClientKey(issuer, clientID string) string {
	return opaqueKey("oauth-issuer-client-v1", issuer, clientID)
}

func AgentBindingKey(issuer, subject, clientID string) string {
	return opaqueKey("agent-binding-v1", issuer, subject, clientID)
}

func ServiceIdentityKey(issuer, clientID string) string {
	return opaqueKey("service-identity-v1", issuer, clientID)
}

func opaqueKey(namespace string, parts ...string) string {
	h := sha256.New()
	writePart(h, namespace)
	for _, part := range parts {
		writePart(h, part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writePart(h hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write([]byte(value))
}
