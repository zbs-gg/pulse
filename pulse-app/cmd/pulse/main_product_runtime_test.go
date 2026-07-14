package main

import (
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/config"
)

func TestProductLocalRuntimeRequiresLoopbackAndExactStoreIdentity(t *testing.T) {
	t.Setenv("PULSE_VAULT_STORE_ID", "store_personal_nik")
	if err := runProductLocal(t.TempDir(), "0.0.0.0:18800", config.VaultPersonal); err == nil ||
		!strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback error = %v", err)
	}

	t.Setenv("PULSE_VAULT_STORE_ID", " store_personal_nik")
	if err := runProductLocal(t.TempDir(), "127.0.0.1:18800", config.VaultPersonal); err == nil ||
		!strings.Contains(err.Error(), "exact PULSE_VAULT_STORE_ID") {
		t.Fatalf("non-exact store ID error = %v", err)
	}
}
