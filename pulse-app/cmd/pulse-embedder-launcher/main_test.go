package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrustedExecutableRequiresCanonicalExecutableFile(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "node")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := trustedExecutable(executable); err != nil || got != executable {
		t.Fatalf("trustedExecutable() = %q, %v", got, err)
	}
	if _, err := trustedExecutable("node"); err == nil {
		t.Fatal("relative authority path unexpectedly accepted")
	}
	alias := filepath.Join(root, "node-alias")
	if err := os.Symlink(executable, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := trustedExecutable(alias); err == nil {
		t.Fatal("symlink authority path unexpectedly accepted")
	}
}

func TestRegularFileRejectsDirectoryAndSymlink(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "runner.mjs")
	if err := os.WriteFile(file, []byte("export {};\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := regularFile(file); err != nil {
		t.Fatal(err)
	}
	if err := regularFile(root); err == nil {
		t.Fatal("directory unexpectedly accepted")
	}
	alias := filepath.Join(root, "runner-alias.mjs")
	if err := os.Symlink(file, alias); err != nil {
		t.Fatal(err)
	}
	if err := regularFile(alias); err == nil {
		t.Fatal("symlink unexpectedly accepted")
	}
}
