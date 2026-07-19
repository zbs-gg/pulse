//go:build windows

package main

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/nkkmnk/pulse/internal/platform"
)

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

func TestInspectPrivateTreeValidatesTheWholeExpectedTreeInOneOperation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-tree")
	runtimeRoot := filepath.Join(root, "runtime")
	if err := platform.EnsurePrivateDirectory(runtimeRoot); err != nil {
		t.Fatal(err)
	}
	payload := []byte("trusted runtime")
	path := filepath.Join(runtimeRoot, "index.js")
	if err := platform.AtomicWritePrivateFile(path, payload); err != nil {
		t.Fatal(err)
	}
	entry := privateTreeEntry{
		Path: "runtime/index.js", Bytes: int64(len(payload)),
		SHA256: fmt.Sprintf("%x", sha256.Sum256(payload)), Executable: false,
	}
	proof, err := inspectPrivateTree(request{
		Path: root, Entries: []privateTreeEntry{entry}, MaximumDepth: 32,
		MaximumEntries: 64, MaximumTotalBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := proof.(map[string]any)
	if result["files"] != 1 || result["bytes"] != int64(len(payload)) {
		t.Fatalf("unexpected tree proof: %#v", result)
	}
	entry.SHA256 = fmt.Sprintf("%064x", 0)
	if _, err := inspectPrivateTree(request{
		Path: root, Entries: []privateTreeEntry{entry}, MaximumDepth: 32,
		MaximumEntries: 64, MaximumTotalBytes: 1024,
	}); err == nil {
		t.Fatal("tampered digest was accepted")
	}
}

func TestDigestPrivateTreeReturnsTheCanonicalSignedTreeDigest(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-tree-digest")
	runtimeRoot := filepath.Join(root, "runtime")
	if err := platform.EnsurePrivateDirectory(runtimeRoot); err != nil {
		t.Fatal(err)
	}
	payload := []byte("trusted runtime")
	if err := platform.AtomicWritePrivateFile(filepath.Join(runtimeRoot, "index.js"), payload); err != nil {
		t.Fatal(err)
	}
	manifest := []byte("excluded manifest")
	if err := platform.AtomicWritePrivateFile(filepath.Join(root, "runtime-manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	proof, err := digestPrivateTree(request{
		Path: root, ExcludeRootFile: "runtime-manifest.json", MaximumDepth: 32,
		MaximumEntries: 64, MaximumTotalBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.New()
	want.Write([]byte("runtime/index.js"))
	want.Write([]byte{0})
	want.Write(payload)
	want.Write([]byte{0})
	result := proof.(map[string]any)
	if result["files"] != 2 || result["bytes"] != int64(len(payload)+len(manifest)) ||
		result["tree_digest"] != fmt.Sprintf("%x", want.Sum(nil)) {
		t.Fatalf("unexpected tree digest proof: %#v", result)
	}
}

func TestBatchRunsOnlyBoundedReadOnlyProofs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "batch")
	if err := platform.EnsurePrivateDirectory(root); err != nil {
		t.Fatal(err)
	}
	payload := []byte("trusted state")
	path := filepath.Join(root, "state.json")
	if err := platform.AtomicWritePrivateFile(path, payload); err != nil {
		t.Fatal(err)
	}
	proof, err := dispatch("batch", request{Requests: []batchOperation{
		{Operation: "read_private_file", Request: request{
			Path: path, MinimumBytes: 1, MaximumBytes: 1024,
		}},
		{Operation: "digest_private_tree", Request: request{
			Path: root, MaximumDepth: 4, MaximumEntries: 4, MaximumTotalBytes: 1024,
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	results := proof.(map[string]any)["results"].([]any)
	if len(results) != 2 || string(results[0].(map[string]any)["bytes_base64"].([]byte)) != string(payload) {
		t.Fatalf("unexpected batch proof: %#v", proof)
	}
	if _, err := dispatch("batch", request{Requests: []batchOperation{{
		Operation: "remove_private_file", Request: request{Path: path},
	}}}); err == nil {
		t.Fatal("mutating operation was accepted in a read-only batch")
	}
	if _, err := dispatch("batch", request{}); err == nil {
		t.Fatal("empty batch was accepted")
	}
}
