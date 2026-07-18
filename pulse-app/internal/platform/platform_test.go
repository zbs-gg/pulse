package platform

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrivateFileContractRejectsLinksAndPermissiveState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pulse state привет")
	if err := EnsurePrivateDirectory(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "state.json")
	if _, err := CreatePrivateFileExclusive(path, []byte("private")); err != nil {
		t.Fatal(err)
	}
	policy := FilePolicy{
		MaximumBytes:        64,
		RequireCurrentOwner: true,
		OwnerOnly:           true,
		SingleLink:          true,
	}
	data, err := ReadPrivateFile(path, policy)
	if err != nil || string(data) != "private" {
		t.Fatalf("read private file: data=%q err=%v", data, err)
	}

	symlink := filepath.Join(root, "state-link.json")
	if err := os.Symlink(path, symlink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ReadPrivateFile(symlink, policy); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("symlink error=%v, want %v", err, ErrUnsafe)
	}

	hardlink := filepath.Join(root, "state-hardlink.json")
	if err := os.Link(path, hardlink); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if _, err := ReadPrivateFile(path, policy); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("hardlink error=%v, want %v", err, ErrUnsafe)
	}
	if err := os.Remove(hardlink); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivateFile(path, policy); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("permissive file error=%v, want %v", err, ErrUnsafe)
	}
}

func TestAtomicPrivateWriteAndExactProcessIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	if err := EnsurePrivateDirectory(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "state.json")
	if err := AtomicWritePrivateFile(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWritePrivateFile(path, []byte("two")); err != nil {
		t.Fatal(err)
	}
	data, err := ReadPrivateFile(path, FilePolicy{
		MaximumBytes: 64, RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
	})
	if err != nil || string(data) != "two" {
		t.Fatalf("atomic result=%q err=%v", data, err)
	}

	identity, err := ProcessIdentity(os.Getpid())
	if err != nil || identity == "" {
		t.Fatalf("current process identity=%q err=%v", identity, err)
	}
	if !ProcessAlive(os.Getpid(), identity) {
		t.Fatal("current exact process instance reported dead")
	}
	if ProcessAlive(os.Getpid(), identity+"-reused") {
		t.Fatal("mismatched process instance reported alive")
	}
}

func TestPortableLockSerializesAndReleases(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	if err := EnsurePrivateDirectory(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "state.lock")
	first, err := AcquireLock(path, 50*time.Millisecond, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(path, 25*time.Millisecond, 30*time.Second); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("contended lock error=%v, want %v", err, ErrLockBusy)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireLock(path, 50*time.Millisecond, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}
