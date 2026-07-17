package unassigned

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

const (
	inboxSchema     = "pulse.unassigned_inbox.v1"
	candidateSchema = "pulse.unassigned_candidate.v1"
	maxInboxBytes   = 512 << 10
	maxItems        = 50
	maxReceipts     = 100
)

var (
	errUnsafe              = errors.New("unassigned inbox is unsafe")
	errBusy                = errors.New("unassigned inbox is busy")
	ErrDestinationConflict = errors.New("unassigned assignment destination changed")
)

type Destination struct {
	BindingDigest string
	RepositoryID  string
	StoreID       string
}

type assignmentIntent struct {
	Schema        string `json:"schema"`
	BindingDigest string `json:"binding_digest"`
	RepositoryID  string `json:"repository_id"`
	StoreID       string `json:"store_id"`
	CreatedAt     string `json:"created_at"`
}

type candidateEnvelope struct {
	Kind    string              `json:"kind"`
	Capsule store.MemoryCapsule `json:"capsule"`
}

type inboxItem struct {
	Schema         string            `json:"schema"`
	ItemID         string            `json:"item_id"`
	ContentDigest  string            `json:"content_digest"`
	CreatedAt      string            `json:"created_at"`
	Host           string            `json:"host"`
	IdempotencyKey string            `json:"idempotency_key"`
	Candidate      json.RawMessage   `json:"candidate"`
	Assignment     *assignmentIntent `json:"assignment,omitempty"`
}

type receipt struct {
	ReceiptID     string `json:"receipt_id"`
	ItemID        string `json:"item_id"`
	ContentDigest string `json:"content_digest"`
	Action        string `json:"action"`
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at"`
	BindingDigest string `json:"binding_digest,omitempty"`
	RepositoryID  string `json:"repository_id,omitempty"`
	StoreID       string `json:"store_id,omitempty"`
}

type inboxFile struct {
	Schema   string      `json:"schema"`
	Items    []inboxItem `json:"items"`
	Receipts []receipt   `json:"receipts"`
}

type Card struct {
	ItemID        string
	ContentDigest string
	CreatedAt     string
	Host          string
	Kind          string
	Summary       string
	Candidate     store.PrivateMemoryCandidate
}

type Activity struct {
	ReceiptID     string
	ItemID        string
	ContentDigest string
	Action        string
	Status        string
	CreatedAt     string
	BindingDigest string
	RepositoryID  string
	StoreID       string
}

type Snapshot struct {
	Cards    []Card
	Activity []Activity
}

func privateOwner(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func inspectPrivateDirectory(path string, missingOK bool) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && missingOK {
		return false, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || !privateOwner(info) {
		return false, errUnsafe
	}
	return true, nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return errUnsafe
	}
	_, err := inspectPrivateDirectory(path, false)
	return err
}

func strictDecode(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func validHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validTypedID(value, prefix string, hexSize int) bool {
	return strings.HasPrefix(value, prefix) && validHex(strings.TrimPrefix(value, prefix), hexSize)
}

func validStoreIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 256 {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') ||
			(index > 0 && strings.ContainsRune("._:-", char)) {
			continue
		}
		return false
	}
	return true
}

func validDestination(value Destination) bool {
	return validHex(value.BindingDigest, 64) && validStoreIdentifier(value.RepositoryID) && validStoreIdentifier(value.StoreID)
}

func destinationFromIntent(value *assignmentIntent) Destination {
	if value == nil {
		return Destination{}
	}
	return Destination{BindingDigest: value.BindingDigest, RepositoryID: value.RepositoryID, StoreID: value.StoreID}
}

func sameDestination(left, right Destination) bool {
	return left.BindingDigest == right.BindingDigest && left.RepositoryID == right.RepositoryID && left.StoreID == right.StoreID
}

func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte("\n")), nil
}

func candidateDigest(raw json.RawMessage) (string, error) {
	canonical, err := canonicalJSON(raw)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("pulse-unassigned-candidate-v1"))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(canonical)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validateReceipt(value receipt) bool {
	if !validTypedID(value.ReceiptID, "unassigned_receipt_", 32) ||
		!validTypedID(value.ItemID, "unassigned_", 32) || !validHex(value.ContentDigest, 64) {
		return false
	}
	switch value.Action + ":" + value.Status {
	case "stage:staged", "delete:deleted":
		if value.BindingDigest != "" || value.RepositoryID != "" || value.StoreID != "" {
			return false
		}
	case "assign:assigning", "assign:assigned":
		if !validDestination(Destination{
			BindingDigest: value.BindingDigest, RepositoryID: value.RepositoryID, StoreID: value.StoreID,
		}) {
			return false
		}
	default:
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value.CreatedAt)
	return err == nil
}

func decodeCandidate(item inboxItem) (store.PrivateMemoryCandidate, error) {
	var envelope candidateEnvelope
	if err := strictDecode(item.Candidate, &envelope); err != nil || envelope.Kind != store.PrivateMemoryCandidateCapsule {
		return store.PrivateMemoryCandidate{}, errUnsafe
	}
	digest, err := candidateDigest(item.Candidate)
	if err != nil || digest != item.ContentDigest {
		return store.PrivateMemoryCandidate{}, errUnsafe
	}
	if envelope.Capsule.Schema != store.MemoryCapsuleSchema || envelope.Capsule.RawInputIncluded ||
		envelope.Capsule.Source.Host != item.Host || len(envelope.Capsule.Items) < 1 || len(envelope.Capsule.Items) > 20 {
		return store.PrivateMemoryCandidate{}, errUnsafe
	}
	return store.PrivateMemoryCandidate{Kind: store.PrivateMemoryCandidateCapsule, Capsule: &envelope.Capsule}, nil
}

func readUnlocked(path string) (inboxFile, error) {
	empty := inboxFile{Schema: inboxSchema, Items: []inboxItem{}, Receipts: []receipt{}}
	ok, err := inspectPrivateDirectory(filepath.Dir(path), true)
	if err != nil || !ok {
		return empty, err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
		return empty, nil
	}
	if err != nil {
		return empty, errUnsafe
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !privateOwner(info) || info.Size() < 1 || info.Size() > maxInboxBytes {
		return empty, errUnsafe
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return empty, errUnsafe
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxInboxBytes+1))
	if err != nil || len(raw) > maxInboxBytes {
		return empty, errUnsafe
	}
	var inbox inboxFile
	if err := strictDecode(raw, &inbox); err != nil || inbox.Schema != inboxSchema ||
		len(inbox.Items) > maxItems || len(inbox.Receipts) > maxReceipts {
		return empty, errUnsafe
	}
	for _, item := range inbox.Items {
		if item.Schema != candidateSchema || !validTypedID(item.ItemID, "unassigned_", 32) ||
			!validHex(item.ContentDigest, 64) || strings.TrimSpace(item.IdempotencyKey) == "" {
			return empty, errUnsafe
		}
		if _, err := time.Parse(time.RFC3339Nano, item.CreatedAt); err != nil {
			return empty, errUnsafe
		}
		if item.Assignment != nil {
			if item.Assignment.Schema != "pulse.unassigned_assignment_intent.v1" ||
				!validDestination(destinationFromIntent(item.Assignment)) {
				return empty, errUnsafe
			}
			if _, err := time.Parse(time.RFC3339Nano, item.Assignment.CreatedAt); err != nil {
				return empty, errUnsafe
			}
		}
		if _, err := decodeCandidate(item); err != nil {
			return empty, err
		}
	}
	for _, terminal := range inbox.Receipts {
		if !validateReceipt(terminal) {
			return empty, errUnsafe
		}
	}
	return inbox, nil
}

func ReadSnapshot(path string) (Snapshot, error) {
	if !filepath.IsAbs(path) {
		return Snapshot{}, errUnsafe
	}
	inbox, err := readUnlocked(filepath.Clean(path))
	if err != nil {
		return Snapshot{}, err
	}
	cards := make([]Card, 0, len(inbox.Items))
	for _, item := range inbox.Items {
		candidate, err := decodeCandidate(item)
		if err != nil {
			return Snapshot{}, err
		}
		summary := candidate.Capsule.Items[0].RedactedSummary
		kind := candidate.Capsule.Items[0].Kind
		if len(candidate.Capsule.Items) > 1 {
			summary = fmt.Sprintf("%s (+%d more)", summary, len(candidate.Capsule.Items)-1)
		}
		cards = append(cards, Card{
			ItemID: item.ItemID, ContentDigest: item.ContentDigest, CreatedAt: item.CreatedAt,
			Host: item.Host, Kind: kind, Summary: summary, Candidate: candidate,
		})
	}
	activity := make([]Activity, 0, 20)
	for index := len(inbox.Receipts) - 1; index >= 0 && len(activity) < 20; index-- {
		value := inbox.Receipts[index]
		if value.Action == "stage" || value.Status == "assigning" {
			continue
		}
		activity = append(activity, Activity{
			ReceiptID: value.ReceiptID, ItemID: value.ItemID, ContentDigest: value.ContentDigest,
			Action: value.Action, Status: value.Status, CreatedAt: value.CreatedAt,
			BindingDigest: value.BindingDigest, RepositoryID: value.RepositoryID, StoreID: value.StoreID,
		})
	}
	return Snapshot{Cards: cards, Activity: activity}, nil
}

func List(path string) ([]Card, error) {
	snapshot, err := ReadSnapshot(path)
	return snapshot.Cards, err
}

type heldLock struct {
	file *os.File
	path string
	dev  uint64
	ino  uint64
}

func (value *heldLock) release() {
	if value == nil {
		return
	}
	_ = value.file.Close()
	info, err := os.Lstat(value.path)
	if err != nil {
		return
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok && uint64(stat.Dev) == value.dev && stat.Ino == value.ino {
		_ = os.Remove(value.path)
	}
}

func processAlive(pid int) bool {
	if pid < 1 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func readLockHolder(path string, expected os.FileInfo) (int, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return 0, err
	}
	expectedStat, expectedOK := expected.Sys().(*syscall.Stat_t)
	openedStat, openedOK := opened.Sys().(*syscall.Stat_t)
	if !expectedOK || !openedOK || expectedStat.Dev != openedStat.Dev || expectedStat.Ino != openedStat.Ino {
		return 0, os.ErrNotExist
	}
	raw, err := io.ReadAll(io.LimitReader(file, 128))
	if err != nil {
		return 0, err
	}
	var pid int
	var token string
	if _, err := fmt.Sscanf(string(raw), "%d %s", &pid, &token); err != nil || !validHex(token, 32) {
		return 0, nil
	}
	return pid, nil
}

func acquireLock(path string) (*heldLock, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
		if err == nil {
			file := os.NewFile(uintptr(fd), path)
			info, statErr := file.Stat()
			if statErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, errUnsafe
			}
			stat, statOK := info.Sys().(*syscall.Stat_t)
			if !statOK {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, errUnsafe
			}
			lock := &heldLock{file: file, path: path, dev: uint64(stat.Dev), ino: stat.Ino}
			var token [16]byte
			if _, err := rand.Read(token[:]); err != nil {
				lock.release()
				return nil, errUnsafe
			}
			if _, err := fmt.Fprintf(file, "%d %s\n", os.Getpid(), hex.EncodeToString(token[:])); err != nil {
				lock.release()
				return nil, errUnsafe
			}
			if err := file.Sync(); err != nil {
				lock.release()
				return nil, errUnsafe
			}
			return lock, nil
		}
		if !errors.Is(err, syscall.EEXIST) {
			return nil, errUnsafe
		}
		info, inspectErr := os.Lstat(path)
		if inspectErr == nil {
			stat, statOK := info.Sys().(*syscall.Stat_t)
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 ||
				!privateOwner(info) || !statOK || stat.Nlink != 1 {
				return nil, errUnsafe
			}
			holderPID, readErr := readLockHolder(path, info)
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				return nil, errUnsafe
			}
			if time.Since(info.ModTime()) > 30*time.Second && !processAlive(holderPID) {
				current, currentErr := os.Lstat(path)
				if currentErr == nil {
					currentStat, currentOK := current.Sys().(*syscall.Stat_t)
					if currentOK && currentStat.Dev == stat.Dev && currentStat.Ino == stat.Ino {
						_ = os.Remove(path)
						continue
					}
				}
			}
		} else if errors.Is(inspectErr, os.ErrNotExist) {
			continue
		} else {
			return nil, errUnsafe
		}
		if time.Now().After(deadline) {
			return nil, errBusy
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func atomicWrite(path string, inbox inboxFile) error {
	raw, err := json.Marshal(inbox)
	if err != nil || len(raw)+1 > maxInboxBytes {
		return errUnsafe
	}
	temporary := fmt.Sprintf("%s.%d.%d.new", path, os.Getpid(), time.Now().UnixNano())
	fd, err := syscall.Open(temporary, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temporary)
	defer os.Remove(temporary)
	if _, err = file.Write(append(raw, '\n')); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporary, path)
	}
	if err == nil {
		directory, openErr := os.Open(filepath.Dir(path))
		if openErr == nil {
			err = directory.Sync()
			_ = directory.Close()
		}
	}
	return err
}

func assignmentReceipt(status string, item inboxItem, destination Destination, now time.Time) receipt {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"pulse-unassigned-assignment-receipt-v1", status, item.ItemID, item.ContentDigest,
		destination.BindingDigest, destination.RepositoryID, destination.StoreID,
	}, "\x00")))
	return receipt{
		ReceiptID: "unassigned_receipt_" + hex.EncodeToString(digest[:16]), ItemID: item.ItemID,
		ContentDigest: item.ContentDigest, Action: "assign", Status: status, CreatedAt: now.UTC().Format(time.RFC3339Nano),
		BindingDigest: destination.BindingDigest, RepositoryID: destination.RepositoryID, StoreID: destination.StoreID,
	}
}

func deleteReceipt(item inboxItem, now time.Time) receipt {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"pulse-unassigned-terminal-receipt-v1", "delete", item.ItemID, item.ContentDigest,
	}, "\x00")))
	return receipt{
		ReceiptID: "unassigned_receipt_" + hex.EncodeToString(digest[:16]), ItemID: item.ItemID,
		ContentDigest: item.ContentDigest, Action: "delete", Status: "deleted", CreatedAt: now.UTC().Format(time.RFC3339Nano),
	}
}

func mutate(
	path, itemID, contentDigest, action string,
	destination Destination,
	now time.Time,
	beforeRemove func(store.PrivateMemoryCandidate) error,
) error {
	if !filepath.IsAbs(path) || !validTypedID(itemID, "unassigned_", 32) || !validHex(contentDigest, 64) {
		return errUnsafe
	}
	if action == "assign" && !validDestination(destination) {
		return errUnsafe
	}
	path = filepath.Clean(path)
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	lockPath := path + ".lock"
	lock, err := acquireLock(lockPath)
	if err != nil {
		return err
	}
	defer lock.release()
	inbox, err := readUnlocked(path)
	if err != nil {
		return err
	}
	index := -1
	for candidateIndex, item := range inbox.Items {
		if item.ItemID == itemID {
			if item.ContentDigest != contentDigest {
				return errors.New("unassigned item changed")
			}
			index = candidateIndex
			break
		}
	}
	if index < 0 {
		for _, prior := range inbox.Receipts {
			if prior.ItemID == itemID && prior.ContentDigest == contentDigest && prior.Action == action {
				if action == "assign" && !sameDestination(destination, Destination{
					BindingDigest: prior.BindingDigest, RepositoryID: prior.RepositoryID, StoreID: prior.StoreID,
				}) {
					return ErrDestinationConflict
				}
				return nil
			}
		}
		return errors.New("unassigned item not found")
	}
	item := inbox.Items[index]
	if action == "assign" {
		if item.Assignment != nil && !sameDestination(destination, destinationFromIntent(item.Assignment)) {
			return ErrDestinationConflict
		}
		if item.Assignment == nil {
			item.Assignment = &assignmentIntent{
				Schema: "pulse.unassigned_assignment_intent.v1", BindingDigest: destination.BindingDigest,
				RepositoryID: destination.RepositoryID, StoreID: destination.StoreID,
				CreatedAt: now.UTC().Format(time.RFC3339Nano),
			}
			inbox.Items[index] = item
			inbox.Receipts = append(inbox.Receipts, assignmentReceipt("assigning", item, destination, now))
			if len(inbox.Receipts) > maxReceipts {
				inbox.Receipts = inbox.Receipts[len(inbox.Receipts)-maxReceipts:]
			}
			if err := atomicWrite(path, inbox); err != nil {
				return err
			}
		}
	}
	candidate, err := decodeCandidate(item)
	if err != nil {
		return err
	}
	if beforeRemove != nil {
		if err := beforeRemove(candidate); err != nil {
			return err
		}
	}
	inbox.Items = append(inbox.Items[:index], inbox.Items[index+1:]...)
	if action == "assign" {
		inbox.Receipts = append(inbox.Receipts, assignmentReceipt("assigned", item, destination, now))
	} else {
		inbox.Receipts = append(inbox.Receipts, deleteReceipt(item, now))
	}
	if len(inbox.Receipts) > maxReceipts {
		inbox.Receipts = inbox.Receipts[len(inbox.Receipts)-maxReceipts:]
	}
	return atomicWrite(path, inbox)
}

func Assign(
	path, itemID, contentDigest string,
	destination Destination,
	now time.Time,
	assign func(store.PrivateMemoryCandidate) error,
) error {
	if assign == nil {
		return errors.New("unassigned assignment is unavailable")
	}
	return mutate(path, itemID, contentDigest, "assign", destination, now, assign)
}

func Delete(path, itemID, contentDigest string, now time.Time) error {
	return mutate(path, itemID, contentDigest, "delete", Destination{}, now, nil)
}
