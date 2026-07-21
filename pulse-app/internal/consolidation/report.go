// Package consolidation owns the portable, content-free consolidation report
// contract. Source inspection is deliberately implemented in later adapters;
// this file owns authority, lifecycle, leases, and durable checkpoints.
package consolidation

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ReportSchema       = "pulse.consolidation.report.v1"
	ProtocolVersion    = 1
	maxCheckpointBytes = 10 << 20
)

type Phase string

const (
	PhasePlanned             Phase = "planned"
	PhaseInventory           Phase = "inventory"
	PhaseDeterministicDedupe Phase = "deterministic_dedupe"
	PhaseReportReady         Phase = "report_ready"
	PhasePartial             Phase = "partial"
	PhaseStale               Phase = "stale"
	PhaseCancelRequested     Phase = "cancel_requested"
	PhaseCanceled            Phase = "canceled"
)

var (
	ErrInvalidAuthority    = errors.New("consolidation: invalid destination authority")
	ErrReportNotFound      = errors.New("consolidation: report not found")
	ErrStaleInvocation     = errors.New("consolidation: stale invocation")
	ErrReportNotResumable  = errors.New("consolidation: report is not resumable")
	ErrCheckpointIntegrity = errors.New("consolidation: checkpoint integrity failure")

	hexDigestPattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	reportIDPattern   = regexp.MustCompile(`^report_[a-zA-Z0-9._-]{1,128}$`)
	storeIDPattern    = regexp.MustCompile(`^store_[a-z0-9][a-z0-9_]{2,127}$`)
	repositoryPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{2,255}$`)
	codePattern       = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)
)

type Destination struct {
	StoreKind     string `json:"store_kind"`
	StoreID       string `json:"store_id"`
	BindingDigest string `json:"binding_digest"`
	RepositoryID  string `json:"repository_id"`
}

type Totals struct {
	AlreadyRepresented int64 `json:"already_represented"`
	Unique             int64 `json:"unique"`
	Ambiguous          int64 `json:"ambiguous"`
	Excluded           int64 `json:"excluded"`
}

type Source struct {
	Alias          string           `json:"alias"`
	Classification string           `json:"classification"`
	ReasonCode     string           `json:"reason_code"`
	Counts         map[string]int64 `json:"counts,omitempty"`
}

type Report struct {
	Schema          string      `json:"schema"`
	ProtocolVersion int         `json:"protocol_version"`
	InvocationID    string      `json:"invocation_id"`
	Phase           Phase       `json:"phase"`
	InputDigest     string      `json:"input_digest"`
	ReportDigest    string      `json:"report_digest"`
	InventoryDigest string      `json:"inventory_digest,omitempty"`
	Generation      uint64      `json:"generation"`
	Destination     Destination `json:"destination"`
	Totals          Totals      `json:"totals"`
	Sources         []Source    `json:"sources"`
	Blockers        []string    `json:"blockers"`
	ReasonCodes     []string    `json:"reason_codes,omitempty"`
	NextAction      string      `json:"next_action"`
	CreatedAt       string      `json:"created_at"`
	UpdatedAt       string      `json:"updated_at"`
}

type ManagerConfig struct {
	RootDir string
	Key     []byte
	Clock   func() time.Time
	NewID   func() string
}

type checkpointEnvelope struct {
	Report    Report `json:"report"`
	Integrity string `json:"integrity"`
}

type Manager struct {
	mu             sync.Mutex
	rootDir        string
	key            []byte
	clock          func() time.Time
	newID          func() string
	latest         map[string]Report
	byInvocation   map[string]Report
	nextGeneration uint64
}

func NewManager(cfg ManagerConfig) (*Manager, error) {
	if !filepath.IsAbs(cfg.RootDir) || len(cfg.Key) < 32 {
		return nil, errors.New("consolidation: absolute root and 32-byte key required")
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.NewID == nil {
		cfg.NewID = randomReportID
	}
	if err := ensurePrivateDirectory(cfg.RootDir); err != nil {
		return nil, err
	}
	m := &Manager{
		rootDir:        cfg.RootDir,
		key:            append([]byte(nil), cfg.Key...),
		clock:          cfg.Clock,
		newID:          cfg.NewID,
		latest:         make(map[string]Report),
		byInvocation:   make(map[string]Report),
		nextGeneration: 1,
	}
	if err := m.loadCheckpoints(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) RootDir() string { return m.rootDir }

func (m *Manager) Start(destination Destination) (Report, bool, error) {
	if err := validateDestination(destination); err != nil {
		return Report{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	inputDigest := m.inputDigest(destination)
	if current, ok := m.latest[inputDigest]; ok && reusablePhase(current.Phase) {
		return cloneReport(current), true, nil
	}
	invocationID := m.newID()
	if !reportIDPattern.MatchString(invocationID) {
		return Report{}, false, errors.New("consolidation: generated invalid report id")
	}
	report := m.newReport(destination, inputDigest, invocationID)
	if err := m.commitLocked(&report); err != nil {
		return Report{}, false, err
	}
	return cloneReport(report), false, nil
}

func (m *Manager) Get(invocationID string) (Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	report, ok := m.byInvocation[invocationID]
	if !ok {
		return Report{}, ErrReportNotFound
	}
	return cloneReport(report), nil
}

func (m *Manager) Latest(destination Destination) (Report, error) {
	if err := validateDestination(destination); err != nil {
		return Report{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	report, ok := m.latest[m.inputDigest(destination)]
	if !ok {
		return Report{}, ErrReportNotFound
	}
	return cloneReport(report), nil
}

func (m *Manager) Cancel(invocationID string) (Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	report, ok := m.byInvocation[invocationID]
	if !ok {
		return Report{}, ErrReportNotFound
	}
	if latest := m.latest[report.InputDigest]; latest.InvocationID != invocationID {
		return Report{}, ErrStaleInvocation
	}
	if report.Phase == PhaseCanceled {
		return cloneReport(report), nil
	}
	if report.Phase == PhaseReportReady || report.Phase == PhaseStale {
		return Report{}, ErrReportNotResumable
	}
	report.Phase = PhaseCanceled
	report.ReasonCodes = []string{"user_canceled"}
	report.NextAction = "Resume the report when you are ready."
	if err := m.commitLocked(&report); err != nil {
		return Report{}, err
	}
	return cloneReport(report), nil
}

func (m *Manager) Resume(invocationID string, destination Destination) (Report, error) {
	if err := validateDestination(destination); err != nil {
		return Report{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	previous, ok := m.byInvocation[invocationID]
	if !ok {
		return Report{}, ErrReportNotFound
	}
	inputDigest := m.inputDigest(destination)
	if previous.InputDigest != inputDigest || m.latest[inputDigest].InvocationID != invocationID {
		return Report{}, ErrStaleInvocation
	}
	if previous.Phase != PhaseCanceled && previous.Phase != PhasePartial {
		return Report{}, ErrReportNotResumable
	}
	newInvocationID := m.newID()
	if !reportIDPattern.MatchString(newInvocationID) {
		return Report{}, errors.New("consolidation: generated invalid report id")
	}
	report := m.newReport(destination, inputDigest, newInvocationID)
	if err := m.commitLocked(&report); err != nil {
		return Report{}, err
	}
	return cloneReport(report), nil
}

// Advance is the daemon-owned extension seam used by the inventory engine.
// It never accepts destination authority from a caller.
func (m *Manager) Advance(invocationID string, phase Phase, totals Totals, sources []Source, blockers, reasons []string, nextAction, inventoryDigest string) (Report, error) {
	if !validPhase(phase) || !validTotals(totals) || !validSources(sources) ||
		!validCodes(blockers, 32) || !validCodes(reasons, 32) || !portableText(nextAction, 4096) ||
		(inventoryDigest != "" && !hexDigestPattern.MatchString(inventoryDigest)) {
		return Report{}, errors.New("consolidation: invalid report update")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	report, ok := m.byInvocation[invocationID]
	if !ok {
		return Report{}, ErrReportNotFound
	}
	if m.latest[report.InputDigest].InvocationID != invocationID {
		return Report{}, ErrStaleInvocation
	}
	if report.Phase == PhaseCanceled || report.Phase == PhaseStale || report.Phase == PhaseReportReady {
		return Report{}, ErrReportNotResumable
	}
	report.Phase = phase
	report.Totals = totals
	report.Sources = cloneSources(sources)
	report.Blockers = append([]string(nil), blockers...)
	report.ReasonCodes = append([]string(nil), reasons...)
	report.NextAction = nextAction
	report.InventoryDigest = inventoryDigest
	if err := m.commitLocked(&report); err != nil {
		return Report{}, err
	}
	return cloneReport(report), nil
}

func (m *Manager) MarkStale(invocationID, reason string) (Report, error) {
	if !codePattern.MatchString(reason) {
		return Report{}, errors.New("consolidation: invalid stale reason")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	report, ok := m.byInvocation[invocationID]
	if !ok {
		return Report{}, ErrReportNotFound
	}
	if m.latest[report.InputDigest].InvocationID != invocationID {
		return Report{}, ErrStaleInvocation
	}
	if report.Phase == PhaseStale {
		return cloneReport(report), nil
	}
	if report.Phase != PhaseReportReady && report.Phase != PhasePartial {
		return Report{}, ErrReportNotResumable
	}
	report.Phase = PhaseStale
	report.ReasonCodes = uniqueSortedCodes(append(report.ReasonCodes, reason))
	report.Blockers = uniqueSortedCodes(append(report.Blockers, "source_state_changed"))
	report.NextAction = "Start a fresh report because a source or policy changed."
	if err := m.commitLocked(&report); err != nil {
		return Report{}, err
	}
	return cloneReport(report), nil
}

func (m *Manager) newReport(destination Destination, inputDigest, invocationID string) Report {
	now := m.clock().UTC().Format(time.RFC3339Nano)
	return Report{
		Schema: ReportSchema, ProtocolVersion: ProtocolVersion,
		InvocationID: invocationID, Phase: PhasePlanned, InputDigest: inputDigest,
		Destination: destination, Sources: []Source{}, Blockers: []string{},
		NextAction: "Wait for inventory to finish.", CreatedAt: now, UpdatedAt: now,
	}
}

func (m *Manager) commitLocked(report *Report) error {
	report.Generation = m.nextGeneration
	m.nextGeneration++
	report.UpdatedAt = m.clock().UTC().Format(time.RFC3339Nano)
	report.ReportDigest = ""
	payload, err := json.Marshal(report)
	if err != nil {
		return err
	}
	report.ReportDigest = sha256Hex(payload)
	payload, err = json.Marshal(report)
	if err != nil {
		return err
	}
	envelope := checkpointEnvelope{Report: *report, Integrity: m.mac(payload)}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if len(encoded) > maxCheckpointBytes {
		return errors.New("consolidation: checkpoint too large")
	}
	name := fmt.Sprintf("checkpoint-%020d.json", report.Generation)
	if err := atomicPrivateWrite(m.rootDir, name, encoded); err != nil {
		return err
	}
	m.latest[report.InputDigest] = cloneReport(*report)
	m.byInvocation[report.InvocationID] = cloneReport(*report)
	return nil
}

func (m *Manager) loadCheckpoints() error {
	entries, err := os.ReadDir(m.rootDir)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "checkpoint-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > maxCheckpointBytes {
			return ErrCheckpointIntegrity
		}
		file, err := os.Open(filepath.Join(m.rootDir, entry.Name()))
		if err != nil {
			return ErrCheckpointIntegrity
		}
		limited := io.LimitReader(file, maxCheckpointBytes+1)
		encoded, readErr := io.ReadAll(limited)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(encoded) > maxCheckpointBytes {
			return ErrCheckpointIntegrity
		}
		var envelope checkpointEnvelope
		if err := json.Unmarshal(encoded, &envelope); err != nil {
			return ErrCheckpointIntegrity
		}
		payload, err := json.Marshal(envelope.Report)
		if err != nil || !hmac.Equal([]byte(envelope.Integrity), []byte(m.mac(payload))) || validateLoadedReport(envelope.Report) != nil {
			return ErrCheckpointIntegrity
		}
		if current, ok := m.latest[envelope.Report.InputDigest]; !ok || current.Generation < envelope.Report.Generation {
			m.latest[envelope.Report.InputDigest] = cloneReport(envelope.Report)
		}
		if current, ok := m.byInvocation[envelope.Report.InvocationID]; !ok || current.Generation < envelope.Report.Generation {
			m.byInvocation[envelope.Report.InvocationID] = cloneReport(envelope.Report)
		}
		if envelope.Report.Generation >= m.nextGeneration {
			m.nextGeneration = envelope.Report.Generation + 1
		}
	}
	return nil
}

func (m *Manager) inputDigest(destination Destination) string {
	payload, _ := json.Marshal(struct {
		Schema      string      `json:"schema"`
		Destination Destination `json:"destination"`
	}{ReportSchema, destination})
	return m.mac(payload)
}

func (m *Manager) mac(payload []byte) string {
	h := hmac.New(sha256.New, m.key)
	_, _ = h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

func validateDestination(destination Destination) error {
	if destination.StoreKind != "personal" && destination.StoreKind != "desk" {
		return ErrInvalidAuthority
	}
	if !storeIDPattern.MatchString(destination.StoreID) || !hexDigestPattern.MatchString(destination.BindingDigest) || !repositoryPattern.MatchString(destination.RepositoryID) {
		return ErrInvalidAuthority
	}
	return nil
}

func validateLoadedReport(report Report) error {
	if report.Schema != ReportSchema || report.ProtocolVersion != ProtocolVersion || !reportIDPattern.MatchString(report.InvocationID) ||
		!hexDigestPattern.MatchString(report.InputDigest) || !hexDigestPattern.MatchString(report.ReportDigest) ||
		(report.InventoryDigest != "" && !hexDigestPattern.MatchString(report.InventoryDigest)) ||
		report.Generation == 0 || !validPhase(report.Phase) || !validTotals(report.Totals) ||
		!validSources(report.Sources) || !validCodes(report.Blockers, 32) ||
		!validCodes(report.ReasonCodes, 32) || !portableText(report.NextAction, 4096) {
		return ErrCheckpointIntegrity
	}
	return validateDestination(report.Destination)
}

type aliasSidecar struct {
	Schema       string            `json:"schema"`
	InvocationID string            `json:"invocation_id"`
	Aliases      map[string]string `json:"aliases"`
	States       map[string]string `json:"states"`
	PolicyDigest string            `json:"policy_digest"`
	Integrity    string            `json:"integrity"`
}

// WriteAliasSidecar stores owner-local path resolution separately from the
// portable report. Paths never enter Report, logs, MCP, or terminal output.
func (m *Manager) WriteAliasSidecar(invocationID string, aliases map[string]string) error {
	states := make(map[string]string, len(aliases))
	for alias, path := range aliases {
		states[alias] = m.mac([]byte("untracked-source-v1\x1f" + filepath.Clean(path)))
	}
	return m.WriteSourceSidecar(invocationID, aliases, states, m.mac([]byte("report-policy-v1")))
}

// WriteSourceSidecar binds each owner-only alias to a content-free local file
// state digest. It allows a ready report to become stale without rescanning or
// exposing a path through the portable contract.
func (m *Manager) WriteSourceSidecar(invocationID string, aliases, states map[string]string, policyDigest string) error {
	if !reportIDPattern.MatchString(invocationID) || len(aliases) > 512 || len(states) != len(aliases) ||
		!hexDigestPattern.MatchString(policyDigest) {
		return errors.New("consolidation: invalid alias sidecar")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byInvocation[invocationID]; !ok {
		return ErrReportNotFound
	}
	clean := make(map[string]string, len(aliases))
	for alias, path := range aliases {
		if !codePattern.MatchString(alias) || !filepath.IsAbs(path) || !hexDigestPattern.MatchString(states[alias]) {
			return errors.New("consolidation: invalid alias sidecar")
		}
		clean[alias] = filepath.Clean(path)
	}
	payload, err := json.Marshal(struct {
		Schema       string            `json:"schema"`
		InvocationID string            `json:"invocation_id"`
		Aliases      map[string]string `json:"aliases"`
		States       map[string]string `json:"states"`
		PolicyDigest string            `json:"policy_digest"`
	}{"pulse.consolidation.aliases.v2", invocationID, clean, states, policyDigest})
	if err != nil {
		return err
	}
	sidecar := aliasSidecar{
		Schema: "pulse.consolidation.aliases.v2", InvocationID: invocationID,
		Aliases: clean, States: states, PolicyDigest: policyDigest, Integrity: m.mac(payload),
	}
	encoded, err := json.Marshal(sidecar)
	if err != nil || len(encoded) > maxCheckpointBytes {
		return errors.New("consolidation: alias sidecar too large")
	}
	name := "aliases-" + invocationID + ".json"
	path := filepath.Join(m.rootDir, name)
	if existing, readErr := os.ReadFile(path); readErr == nil {
		info, statErr := os.Lstat(path)
		var current aliasSidecar
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
			len(existing) > maxCheckpointBytes || json.Unmarshal(existing, &current) != nil {
			return ErrCheckpointIntegrity
		}
		currentPayload, marshalErr := json.Marshal(struct {
			Schema       string            `json:"schema"`
			InvocationID string            `json:"invocation_id"`
			Aliases      map[string]string `json:"aliases"`
			States       map[string]string `json:"states"`
			PolicyDigest string            `json:"policy_digest"`
		}{current.Schema, current.InvocationID, current.Aliases, current.States, current.PolicyDigest})
		if marshalErr != nil || current.Schema != sidecar.Schema || current.InvocationID != sidecar.InvocationID ||
			!hmac.Equal([]byte(current.Integrity), []byte(m.mac(currentPayload))) || string(currentPayload) != string(payload) {
			return ErrCheckpointIntegrity
		}
		return nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	return atomicPrivateWrite(m.rootDir, name, encoded)
}

func (m *Manager) ReadSourceSidecar(invocationID string) (map[string]string, map[string]string, string, error) {
	if !reportIDPattern.MatchString(invocationID) {
		return nil, nil, "", ErrReportNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byInvocation[invocationID]; !ok {
		return nil, nil, "", ErrReportNotFound
	}
	path := filepath.Join(m.rootDir, "aliases-"+invocationID+".json")
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, "", ErrCheckpointIntegrity
	}
	info, err := os.Lstat(path)
	var sidecar aliasSidecar
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || len(encoded) > maxCheckpointBytes ||
		json.Unmarshal(encoded, &sidecar) != nil || sidecar.Schema != "pulse.consolidation.aliases.v2" ||
		sidecar.InvocationID != invocationID || len(sidecar.Aliases) != len(sidecar.States) ||
		!hexDigestPattern.MatchString(sidecar.PolicyDigest) {
		return nil, nil, "", ErrCheckpointIntegrity
	}
	payload, err := json.Marshal(struct {
		Schema       string            `json:"schema"`
		InvocationID string            `json:"invocation_id"`
		Aliases      map[string]string `json:"aliases"`
		States       map[string]string `json:"states"`
		PolicyDigest string            `json:"policy_digest"`
	}{sidecar.Schema, sidecar.InvocationID, sidecar.Aliases, sidecar.States, sidecar.PolicyDigest})
	if err != nil || !hmac.Equal([]byte(sidecar.Integrity), []byte(m.mac(payload))) {
		return nil, nil, "", ErrCheckpointIntegrity
	}
	aliases := make(map[string]string, len(sidecar.Aliases))
	states := make(map[string]string, len(sidecar.States))
	for alias, path := range sidecar.Aliases {
		if !codePattern.MatchString(alias) || !filepath.IsAbs(path) || !hexDigestPattern.MatchString(sidecar.States[alias]) {
			return nil, nil, "", ErrCheckpointIntegrity
		}
		aliases[alias] = path
		states[alias] = sidecar.States[alias]
	}
	return aliases, states, sidecar.PolicyDigest, nil
}

func validTotals(totals Totals) bool {
	return totals.AlreadyRepresented >= 0 && totals.Unique >= 0 && totals.Ambiguous >= 0 && totals.Excluded >= 0
}

func validSources(sources []Source) bool {
	if len(sources) > 512 {
		return false
	}
	for _, source := range sources {
		if !codePattern.MatchString(source.Alias) || !codePattern.MatchString(source.Classification) ||
			!codePattern.MatchString(source.ReasonCode) || len(source.Counts) > 32 {
			return false
		}
		for key, count := range source.Counts {
			if !codePattern.MatchString(key) || count < 0 {
				return false
			}
		}
	}
	return true
}

func validCodes(values []string, maximum int) bool {
	if len(values) > maximum {
		return false
	}
	for _, value := range values {
		if !codePattern.MatchString(value) {
			return false
		}
	}
	return true
}

func portableText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.ContainsRune(value, '\x00') {
		return false
	}
	lower := strings.ToLower(value)
	for _, forbidden := range []string{
		"/users/", "/home/", "/var/", "/private/", "/volumes/", "/workspace/",
		"token=", "api_key", "api-key", "authorization:", "begin private key", "ghp_", "xoxb-", "xoxp-",
	} {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	return true
}

func cloneReport(report Report) Report {
	report.Sources = cloneSources(report.Sources)
	report.Blockers = append([]string(nil), report.Blockers...)
	report.ReasonCodes = append([]string(nil), report.ReasonCodes...)
	return report
}

func cloneSources(sources []Source) []Source {
	cloned := make([]Source, len(sources))
	for index, source := range sources {
		cloned[index] = source
		if source.Counts != nil {
			cloned[index].Counts = make(map[string]int64, len(source.Counts))
			for key, value := range source.Counts {
				cloned[index].Counts[key] = value
			}
		}
	}
	return cloned
}

func validPhase(phase Phase) bool {
	switch phase {
	case PhasePlanned, PhaseInventory, PhaseDeterministicDedupe, PhaseReportReady, PhasePartial, PhaseStale, PhaseCancelRequested, PhaseCanceled:
		return true
	default:
		return false
	}
}

func reusablePhase(phase Phase) bool {
	return phase == PhasePlanned || phase == PhaseInventory || phase == PhaseDeterministicDedupe || phase == PhaseCancelRequested || phase == PhaseReportReady
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("consolidation: report root is not a private directory")
	}
	return os.Chmod(path, 0o700)
}

func atomicPrivateWrite(root, name string, payload []byte) error {
	tmp := filepath.Join(root, "."+name+".tmp-"+randomSuffix())
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := file.Write(payload); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	final := filepath.Join(root, name)
	if _, err := os.Lstat(final); err == nil {
		return errors.New("consolidation: checkpoint generation already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	remove = false
	if dir, err := os.Open(root); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func randomReportID() string { return "report_" + randomSuffix() }

func randomSuffix() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("consolidation: random source unavailable")
	}
	return hex.EncodeToString(value[:])
}

func sha256Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
