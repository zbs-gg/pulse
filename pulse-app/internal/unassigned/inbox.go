package unassigned

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/platform"
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

func inspectPrivateDirectory(path string, missingOK bool) (bool, error) {
	ok, err := platform.InspectPrivateDirectory(path, missingOK)
	if err != nil {
		return false, errUnsafe
	}
	return ok, nil
}

func ensurePrivateDirectory(path string) error {
	if exists, err := platform.InspectPrivateDirectory(path, true); err != nil {
		return errUnsafe
	} else if exists {
		return nil
	}
	if err := platform.EnsurePrivateDirectory(path); err != nil {
		return errUnsafe
	}
	return nil
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
	raw, err := platform.ReadPrivateFile(path, platform.FilePolicy{
		MinimumBytes: 1, MaximumBytes: maxInboxBytes, RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
	})
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
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
	lock *platform.Lock
}

func (value *heldLock) release() {
	if value == nil {
		return
	}
	_ = value.lock.Release()
}

func acquireLock(path string) (*heldLock, error) {
	lock, err := platform.AcquireLock(path, 5*time.Second, 30*time.Second)
	if errors.Is(err, platform.ErrLockBusy) {
		return nil, errBusy
	}
	if err != nil {
		return nil, errUnsafe
	}
	return &heldLock{lock: lock}, nil
}

func atomicWrite(path string, inbox inboxFile) error {
	raw, err := json.Marshal(inbox)
	if err != nil || len(raw)+1 > maxInboxBytes {
		return errUnsafe
	}
	return platform.AtomicWritePrivateFile(path, append(raw, '\n'))
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
