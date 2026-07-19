//go:build windows

package main

import "testing"

func TestAdapterTargetUsesPublicArchitectureNames(t *testing.T) {
	for architecture, want := range map[string]string{
		"amd64": "win32-x64",
		"arm64": "win32-arm64",
		"386":   "",
	} {
		if got := adapterTarget(architecture); got != want {
			t.Fatalf("adapterTarget(%q) = %q, want %q", architecture, got, want)
		}
	}
}
