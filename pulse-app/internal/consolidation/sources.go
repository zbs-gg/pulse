package consolidation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ClassificationCanonicalVault     = "canonical_vault"
	ClassificationLegacyPulseDB      = "legacy_pulse_db"
	ClassificationPulseExport        = "pulse_export"
	ClassificationMigrationWorkspace = "migration_workspace"
	ClassificationClaudeMem          = "claude_mem"
	ClassificationCache              = "cache"
	ClassificationBackup             = "backup"
	ClassificationReleaseArtifact    = "release_artifact"
	ClassificationCodeCheckout       = "code_checkout"
	ClassificationUnknown            = "unknown"

	adapterPulseV1     = "adapter_pulse_v1"
	adapterClaudeMemV1 = "adapter_claude_mem_v1"
	normalizationV1    = "normalization_nfc_lower_v1"
	dedupeV1           = "dedupe_hmac_v1"
	scrubberV1         = "scrubber_v1"
	fingerprintKeyV1   = "fingerprint_key_v1"
)

type Limits struct {
	MaxRegistryEntries int
	MaxSources         int
	MaxRowsPerSource   int64
	MaxRowsTotal       int64
	MaxBytesPerSource  int64
	MaxBytesTotal      int64
	MaxElapsed         time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		MaxRegistryEntries: 512, MaxSources: 512,
		MaxRowsPerSource: 2_000_000, MaxRowsTotal: 5_000_000,
		MaxBytesPerSource: 8 << 30, MaxBytesTotal: 16 << 30,
		MaxElapsed: 15 * time.Minute,
	}
}

type EngineConfig struct {
	Manager       *Manager
	HomeDir       string
	CanonicalPath string
	CanonicalDB   *sql.DB
	Limits        Limits
	Clock         func() time.Time
}

type Engine struct {
	mu            sync.Mutex
	running       map[string]struct{}
	manager       *Manager
	homeDir       string
	canonicalPath string
	canonicalDB   *sql.DB
	limits        Limits
	clock         func() time.Time
}

type sourceCandidate struct {
	path      string
	hint      string
	canonical bool
	artifact  bool
}

type inventoryItem struct {
	stableKey   string
	fingerprint string
	projectKey  string
}

type inspectedSource struct {
	path           string
	alias          string
	classification string
	reasonCode     string
	counts         map[string]int64
	items          []inventoryItem
	identityDigest string
	stateDigest    string
	partialReason  string
	stale          bool
	canonical      bool
}

func NewEngine(cfg EngineConfig) (*Engine, error) {
	if cfg.Manager == nil || !filepath.IsAbs(cfg.HomeDir) || !filepath.IsAbs(cfg.CanonicalPath) {
		return nil, errors.New("consolidation: manager and absolute inventory roots required")
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Limits == (Limits{}) {
		cfg.Limits = DefaultLimits()
	}
	if cfg.Limits.MaxRegistryEntries < 1 || cfg.Limits.MaxSources < 1 ||
		cfg.Limits.MaxRowsPerSource < 1 || cfg.Limits.MaxRowsTotal < 1 ||
		cfg.Limits.MaxBytesPerSource < 1 || cfg.Limits.MaxBytesTotal < 1 || cfg.Limits.MaxElapsed <= 0 {
		return nil, errors.New("consolidation: invalid inventory limits")
	}
	return &Engine{
		running: make(map[string]struct{}),
		manager: cfg.Manager, homeDir: filepath.Clean(cfg.HomeDir),
		canonicalPath: filepath.Clean(cfg.CanonicalPath), canonicalDB: cfg.CanonicalDB,
		limits: cfg.Limits, clock: cfg.Clock,
	}, nil
}

func (e *Engine) Run(ctx context.Context, invocationID string, destination Destination) (Report, error) {
	e.mu.Lock()
	if _, running := e.running[invocationID]; running {
		e.mu.Unlock()
		return e.manager.Get(invocationID)
	}
	e.running[invocationID] = struct{}{}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.running, invocationID)
		e.mu.Unlock()
	}()
	started := e.clock()
	if _, err := e.manager.Advance(
		invocationID, PhaseInventory, Totals{}, []Source{}, nil,
		[]string{adapterPulseV1, adapterClaudeMemV1, normalizationV1, dedupeV1, scrubberV1, fingerprintKeyV1},
		"Inspecting recognized local memory sources.", "",
	); err != nil {
		return Report{}, err
	}

	candidates, discoveryPartial := e.discover()
	inspected := make([]inspectedSource, 0, len(candidates))
	var totalRows, totalBytes int64
	for _, candidate := range candidates {
		if err := e.checkContinue(ctx, invocationID, started); err != nil {
			return e.finishInterrupted(invocationID, inspected, err)
		}
		if len(inspected) >= e.limits.MaxSources {
			discoveryPartial = "resource_limit"
			break
		}
		info, err := os.Lstat(candidate.path)
		if err != nil {
			inspected = append(inspected, partialSource(candidate, "source_disappeared"))
			continue
		}
		if info.Size() > e.limits.MaxBytesPerSource || totalBytes+info.Size() > e.limits.MaxBytesTotal {
			inspected = append(inspected, partialSource(candidate, "resource_limit"))
			discoveryPartial = "resource_limit"
			break
		}
		totalBytes += info.Size()
		var source inspectedSource
		if candidate.artifact {
			source = inspectArtifact(candidate, info, e.manager)
		} else {
			source = e.inspectDatabase(ctx, candidate)
		}
		sourceRows := source.counts["source_rows"]
		if sourceRows > e.limits.MaxRowsPerSource || totalRows+sourceRows > e.limits.MaxRowsTotal {
			source = partialSource(candidate, "resource_limit")
			discoveryPartial = "resource_limit"
			inspected = append(inspected, source)
			break
		}
		totalRows += sourceRows
		inspected = append(inspected, source)
	}

	assignAliases(inspected)
	aliases := make(map[string]string, len(inspected))
	states := make(map[string]string, len(inspected))
	for _, source := range inspected {
		aliases[source.alias] = source.path
		states[source.alias] = source.stateDigest
	}
	if err := e.manager.WriteSourceSidecar(invocationID, aliases, states, e.policyDigest()); err != nil {
		return Report{}, err
	}

	if _, err := e.manager.Advance(
		invocationID, PhaseDeterministicDedupe, Totals{}, portableSources(inspected), nil,
		[]string{adapterPulseV1, adapterClaudeMemV1, normalizationV1, dedupeV1, scrubberV1, fingerprintKeyV1},
		"Comparing stable source identities and keyed normalized content.", "",
	); err != nil {
		return Report{}, err
	}

	totals := computeDeterministicOverlap(inspected)
	reasons := []string{adapterPulseV1, adapterClaudeMemV1, normalizationV1, dedupeV1, scrubberV1, fingerprintKeyV1}
	blockers := make([]string, 0)
	phase := PhaseReportReady
	nextAction := "Review unique and ambiguous source counts before any later import plan."
	for _, source := range inspected {
		if source.partialReason != "" {
			phase = PhasePartial
			blockers = append(blockers, source.alias+"_partial")
			reasons = append(reasons, source.partialReason)
		}
		if source.stale {
			phase = PhaseStale
			blockers = append(blockers, source.alias+"_stale")
			reasons = append(reasons, "source_changed")
		}
	}
	if discoveryPartial != "" {
		phase = PhasePartial
		blockers = append(blockers, "registry_partial")
		reasons = append(reasons, discoveryPartial)
	}
	if len(inspected) == 1 && inspected[0].canonical && phase == PhaseReportReady {
		nextAction = "No legacy memory sources found. Nothing to do."
	}
	sort.Strings(blockers)
	reasons = uniqueSortedCodes(reasons)
	inventoryDigest := e.inventoryDigest(destination, inspected)
	return e.manager.Advance(
		invocationID, phase, totals, portableSources(inspected), blockers, reasons, nextAction, inventoryDigest,
	)
}

func (e *Engine) checkContinue(ctx context.Context, invocationID string, started time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.clock().Sub(started) > e.limits.MaxElapsed {
		return errors.New("resource_limit")
	}
	report, err := e.manager.Get(invocationID)
	if err != nil {
		return err
	}
	if report.Phase == PhaseCanceled {
		return errors.New("report_canceled")
	}
	return nil
}

func (e *Engine) finishInterrupted(invocationID string, inspected []inspectedSource, cause error) (Report, error) {
	report, getErr := e.manager.Get(invocationID)
	if getErr == nil && report.Phase == PhaseCanceled {
		return report, nil
	}
	reason := "inventory_interrupted"
	if cause.Error() == "resource_limit" {
		reason = "resource_limit"
	}
	return e.manager.Advance(
		invocationID, PhasePartial, Totals{}, portableSources(inspected), []string{"inventory_partial"},
		[]string{reason}, "Fix the reported source issue and resume the report.", e.inventoryDigest(Destination{}, inspected),
	)
}

func (e *Engine) discover() ([]sourceCandidate, string) {
	candidates := []sourceCandidate{{path: e.canonicalPath, hint: ClassificationCanonicalVault, canonical: true}}
	entries, err := os.ReadDir(e.homeDir)
	if err != nil {
		return candidates, "registry_unavailable"
	}
	seenEntries := 0
	partial := ""
	for _, entry := range entries {
		if !recognizedRegistryRoot(entry.Name()) {
			continue
		}
		seenEntries++
		if seenEntries > e.limits.MaxRegistryEntries {
			partial = "resource_limit"
			break
		}
		path := filepath.Join(e.homeDir, entry.Name())
		info, statErr := os.Lstat(path)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !sameDevice(e.homeDir, path) {
			partial = "mount_escape"
			continue
		}
		hint := classificationHint(entry.Name(), path)
		if !info.IsDir() {
			if isSQLiteFilename(entry.Name()) {
				candidates = append(candidates, sourceCandidate{path: path, hint: hint})
			}
			continue
		}
		if isArtifactClassification(hint) {
			candidates = append(candidates, sourceCandidate{path: path, hint: hint, artifact: true})
			continue
		}
		files, hitLimit := e.registeredDatabaseFiles(path)
		for _, file := range files {
			candidates = append(candidates, sourceCandidate{path: file, hint: hint})
		}
		if hitLimit {
			partial = "resource_limit"
			break
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].canonical != candidates[j].canonical {
			return candidates[i].canonical
		}
		return candidates[i].path < candidates[j].path
	})
	return deduplicateCandidates(candidates), partial
}

func (e *Engine) registeredDatabaseFiles(root string) ([]string, bool) {
	files := make([]string, 0)
	entries, err := os.ReadDir(root)
	if err != nil {
		return files, false
	}
	seen := 0
	for _, entry := range entries {
		seen++
		if seen > e.limits.MaxRegistryEntries {
			return files, true
		}
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if isSQLiteFilename(entry.Name()) {
				files = append(files, path)
			}
			continue
		}
		if info.Mode().IsRegular() && isSQLiteFilename(entry.Name()) {
			files = append(files, path)
			continue
		}
		if !info.IsDir() {
			continue
		}
		children, err := os.ReadDir(path)
		if err != nil {
			continue
		}
		for _, child := range children {
			seen++
			if seen > e.limits.MaxRegistryEntries {
				return files, true
			}
			childPath := filepath.Join(path, child.Name())
			childInfo, err := os.Lstat(childPath)
			if err == nil && isSQLiteFilename(child.Name()) &&
				(childInfo.Mode().IsRegular() || childInfo.Mode()&os.ModeSymlink != 0) {
				files = append(files, childPath)
			}
		}
	}
	sort.Strings(files)
	return files, false
}

func recognizedRegistryRoot(name string) bool {
	lower := strings.ToLower(name)
	return lower == ".pulse" || strings.HasPrefix(lower, ".pulse-") ||
		lower == "pulse-data" || strings.HasPrefix(lower, "pulse-") ||
		lower == ".claude-mem" || strings.HasPrefix(lower, ".claude-mem-")
}

func classificationHint(name, path string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "claude-mem"):
		return ClassificationClaudeMem
	case strings.Contains(lower, "backup") || strings.Contains(lower, "bak"):
		return ClassificationBackup
	case strings.Contains(lower, "cache"):
		return ClassificationCache
	case strings.Contains(lower, "release"):
		return ClassificationReleaseArtifact
	case strings.Contains(lower, "migration") || strings.Contains(lower, "migrate"):
		return ClassificationMigrationWorkspace
	case strings.Contains(lower, "export"):
		return ClassificationPulseExport
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return ClassificationCodeCheckout
	}
	return ClassificationLegacyPulseDB
}

func isArtifactClassification(classification string) bool {
	return classification == ClassificationCache || classification == ClassificationBackup ||
		classification == ClassificationReleaseArtifact || classification == ClassificationCodeCheckout ||
		classification == ClassificationPulseExport || classification == ClassificationMigrationWorkspace
}

func isSQLiteFilename(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".db") || strings.HasSuffix(lower, ".sqlite") || strings.HasSuffix(lower, ".sqlite3")
}

func deduplicateCandidates(candidates []sourceCandidate) []sourceCandidate {
	seen := make(map[string]struct{}, len(candidates))
	out := make([]sourceCandidate, 0, len(candidates))
	identities := make([]os.FileInfo, 0, len(candidates))
	for _, candidate := range candidates {
		path := filepath.Clean(candidate.path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		info, err := os.Stat(path)
		duplicate := false
		if err == nil {
			for _, identity := range identities {
				if os.SameFile(identity, info) {
					duplicate = true
					break
				}
			}
		}
		if duplicate {
			continue
		}
		if err == nil {
			identities = append(identities, info)
		}
		candidate.path = path
		out = append(out, candidate)
	}
	return out
}

func sameDevice(root, path string) bool {
	rootInfo, rootErr := os.Stat(root)
	pathInfo, pathErr := os.Lstat(path)
	if rootErr != nil || pathErr != nil {
		return false
	}
	rootDevice, rootOK := numericStatField(rootInfo, "Dev")
	pathDevice, pathOK := numericStatField(pathInfo, "Dev")
	return !rootOK || !pathOK || rootDevice == pathDevice
}

func partialSource(candidate sourceCandidate, reason string) inspectedSource {
	classification := candidate.hint
	if candidate.canonical {
		classification = ClassificationCanonicalVault
	}
	source := inspectedSource{
		path: candidate.path, classification: classification, reasonCode: reason,
		counts: map[string]int64{"unsupported_material": 1}, partialReason: reason, canonical: candidate.canonical,
	}
	if digest, err := currentPathStateDigest(candidate.path, nil); err == nil {
		source.stateDigest = digest
	} else {
		source.stateDigest = strings.Repeat("0", 64)
	}
	return source
}

func inspectArtifact(candidate sourceCandidate, info os.FileInfo, manager *Manager) inspectedSource {
	identity := manager.mac([]byte(fmt.Sprintf("artifact-v1\x1f%s\x1f%d\x1f%d", candidate.hint, info.Size(), info.ModTime().UnixNano())))
	return inspectedSource{
		path: candidate.path, classification: candidate.hint, reasonCode: "non_memory_artifact",
		counts: map[string]int64{"excluded_material": 1}, identityDigest: identity, stateDigest: identity,
	}
}

func assignAliases(sources []inspectedSource) {
	counters := make(map[string]int)
	for index := range sources {
		class := sources[index].classification
		if !codePattern.MatchString(class) {
			class = ClassificationUnknown
		}
		counters[class]++
		sources[index].alias = fmt.Sprintf("%s_%02d", class, counters[class])
	}
}

func portableSources(inspected []inspectedSource) []Source {
	out := make([]Source, 0, len(inspected))
	for _, source := range inspected {
		out = append(out, Source{
			Alias: source.alias, Classification: source.classification,
			ReasonCode: source.reasonCode, Counts: cloneCountMap(source.counts),
		})
	}
	return out
}

func computeDeterministicOverlap(sources []inspectedSource) Totals {
	stable := make(map[string]string)
	content := make(map[string]string)
	for _, source := range sources {
		if !source.canonical {
			continue
		}
		for _, item := range source.items {
			stable[item.stableKey] = item.fingerprint
			content[item.fingerprint] = item.stableKey
		}
	}
	var totals Totals
	for sourceIndex := range sources {
		source := &sources[sourceIndex]
		totals.Excluded += source.counts["excluded_material"] + source.counts["unsupported_material"]
		if source.canonical {
			continue
		}
		for _, item := range source.items {
			if priorFingerprint, ok := stable[item.stableKey]; ok {
				if priorFingerprint == item.fingerprint {
					totals.AlreadyRepresented++
					source.counts["same_stable_source"]++
				} else {
					totals.Ambiguous++
					source.counts["changed_content"]++
					source.counts["review_required"]++
				}
				continue
			}
			if _, ok := content[item.fingerprint]; ok {
				totals.AlreadyRepresented++
				source.counts["same_normalized_content"]++
				continue
			}
			totals.Unique++
			source.counts["unique_material"]++
			stable[item.stableKey] = item.fingerprint
			content[item.fingerprint] = item.stableKey
		}
	}
	return totals
}

func (e *Engine) inventoryDigest(destination Destination, inspected []inspectedSource) string {
	identities := make([]string, 0, len(inspected))
	for _, source := range inspected {
		identities = append(identities, strings.Join([]string{
			source.alias, source.classification, source.reasonCode, source.identityDigest,
		}, "\x1f"))
	}
	payload, _ := json.Marshal(struct {
		Schema      string      `json:"schema"`
		Destination Destination `json:"destination"`
		Policies    []string    `json:"policies"`
		Sources     []string    `json:"sources"`
	}{
		"pulse.consolidation.inventory.v1", destination,
		[]string{adapterPulseV1, adapterClaudeMemV1, normalizationV1, dedupeV1, scrubberV1, fingerprintKeyV1},
		identities,
	})
	return e.manager.mac(payload)
}

func (e *Engine) policyDigest() string {
	return e.manager.mac([]byte(strings.Join([]string{
		adapterPulseV1, adapterClaudeMemV1, normalizationV1, dedupeV1, scrubberV1, fingerprintKeyV1,
	}, "\x1f")))
}

// EnsureFresh performs a bounded metadata-only validation of a ready report.
// It never reopens SQLite or reads memory bodies; any missing or changed private
// sidecar state converts the report to stale.
func (e *Engine) EnsureFresh(invocationID string) (Report, error) {
	report, err := e.manager.Get(invocationID)
	if err != nil || report.Phase != PhaseReportReady {
		return report, err
	}
	aliases, states, policyDigest, err := e.manager.ReadSourceSidecar(invocationID)
	if err != nil || policyDigest != e.policyDigest() {
		return e.manager.MarkStale(invocationID, "policy_changed")
	}
	classifications := make(map[string]string, len(report.Sources))
	for _, source := range report.Sources {
		classifications[source.Alias] = source.Classification
	}
	for alias, path := range aliases {
		classification, ok := classifications[alias]
		if !ok {
			return e.manager.MarkStale(invocationID, "source_changed")
		}
		state, stateErr := currentPathStateDigest(path, e.manager)
		if stateErr != nil {
			return e.manager.MarkStale(invocationID, "source_changed")
		}
		if isArtifactClassification(classification) {
			info, infoErr := os.Lstat(path)
			if infoErr != nil {
				return e.manager.MarkStale(invocationID, "source_changed")
			}
			state = e.manager.mac([]byte(fmt.Sprintf("artifact-v1\x1f%s\x1f%d\x1f%d", classification, info.Size(), info.ModTime().UnixNano())))
		}
		if states[alias] != state {
			return e.manager.MarkStale(invocationID, "source_changed")
		}
	}
	return report, nil
}

func uniqueSortedCodes(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if codePattern.MatchString(value) {
			seen[value] = struct{}{}
		}
	}
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func cloneCountMap(values map[string]int64) map[string]int64 {
	if values == nil {
		return map[string]int64{}
	}
	cloned := make(map[string]int64, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
