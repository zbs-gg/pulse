package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
	_ "modernc.org/sqlite"
)

const LocalMergePreviewSchema = "pulse.local_store_merge_preview.v1"

type LocalMergeSource struct {
	Path   string           `json:"path"`
	Kind   string           `json:"kind"`
	SHA256 string           `json:"sha256,omitempty"`
	State  string           `json:"state_digest,omitempty"`
	Size   int64            `json:"size_bytes"`
	Counts map[string]int64 `json:"counts"`
}

type LocalMergeConflictChoice struct {
	ID      string         `json:"id"`
	Label   string         `json:"label"`
	Payload map[string]any `json:"payload"`
}

type LocalMergeConflict struct {
	ID       string                     `json:"id"`
	Kind     string                     `json:"kind"`
	Question string                     `json:"question"`
	Selected string                     `json:"selected"`
	Choices  []LocalMergeConflictChoice `json:"choices"`
}

type LocalMergeTotals struct {
	EventsCreated          int64 `json:"events_created"`
	EventsDeduplicated     int64 `json:"events_deduplicated"`
	EmotionsCreated        int64 `json:"emotions_created"`
	EmotionsDeduplicated   int64 `json:"emotions_deduplicated"`
	CapsulesCreated        int64 `json:"capsules_created"`
	CapsulesDeduplicated   int64 `json:"capsules_deduplicated"`
	ChainsCreated          int64 `json:"chains_created"`
	EmbeddingsCreated      int64 `json:"embeddings_created"`
	AssertionsCreated      int64 `json:"assertions_created"`
	AssertionsDeduplicated int64 `json:"assertions_deduplicated"`
}

type LocalMergePreview struct {
	Schema               string               `json:"schema"`
	PreviewID            string               `json:"preview_id"`
	GeneratedAt          string               `json:"generated_at"`
	StoreID              string               `json:"store_id"`
	CanonicalPath        string               `json:"canonical_path"`
	CanonicalStateDigest string               `json:"canonical_state_digest"`
	TargetPath           string               `json:"target_path"`
	TargetSHA256         string               `json:"target_sha256"`
	Sources              []LocalMergeSource   `json:"sources"`
	Totals               LocalMergeTotals     `json:"totals"`
	Conflicts            []LocalMergeConflict `json:"conflicts"`
	Status               string               `json:"status"`
	CommitConfirmation   string               `json:"commit_confirmation"`
}

type LocalMergeStatus struct {
	Schema        string             `json:"schema"`
	CanonicalPath string             `json:"canonical_path"`
	Sources       []LocalMergeSource `json:"sources"`
	Pending       []string           `json:"pending_previews"`
}

type mergeEvent struct {
	ID              int64
	Title           string
	Description     sql.NullString
	Sentiment       sql.NullFloat64
	EmotionalWeight float64
	ScorerVersion   sql.NullString
	OccurredAt      string
	BeliefClass     string
	ConfidenceFloor float64
	Archivable      int
	Provenance      sql.NullString
	Domain          string
	UserFlag        int
	SentimentLabel  sql.NullString
	BiometricJSON   sql.NullString
	Tags            sql.NullString
	AccessCount     int64
	LastAccessedAt  sql.NullString
}

type mergeEmotion struct {
	Values            [10]float64
	Tagger            string
	TaggerVersion     sql.NullString
	Confidence        float64
	UpdatedAt         string
	Derivation        string
	ObservedLabel     string
	TriggerSummary    string
	TriggerDerivation string
	TriggerConfidence float64
	TriggerConfirmed  int
	EmotionKey        string
}

type mergeAssertion struct {
	ClaimKey         string
	Predicate        string
	ObjectText       string
	Qualifiers       sql.NullString
	Confidence       float64
	ValidFrom        sql.NullString
	ValidTo          sql.NullString
	SystemFrom       string
	SourceEventIDs   sql.NullString
	ExtractorVersion sql.NullString
	ScopeType        string
	ScopeID          string
	Visibility       string
	CreatedAt        string
}

func DiscoverLocalMergeSources(home, canonicalPath string) ([]string, error) {
	if !filepath.IsAbs(home) || !filepath.IsAbs(canonicalPath) {
		return nil, errors.New("local merge paths must be absolute")
	}
	root := filepath.Join(filepath.Clean(home), ".pulse")
	candidates := []string{filepath.Join(root, "pulse.db")}
	vaults, err := filepath.Glob(filepath.Join(root, "vaults", "personal", "*", "pulse.db"))
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, vaults...)
	standalone := filepath.Join(root, "standalone", "store.json")
	if _, err := os.Lstat(standalone); err == nil {
		candidates = append(candidates, standalone)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	canonicalPath = filepath.Clean(canonicalPath)
	seen := map[string]bool{}
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if candidate == canonicalPath || seen[candidate] {
			continue
		}
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("local merge source is unsafe: %s", candidate)
		}
		seen[candidate] = true
		out = append(out, candidate)
	}
	sort.Strings(out)
	return out, nil
}

func BuildLocalMergePreview(home, canonicalPath, storeID string, now time.Time) (LocalMergePreview, error) {
	if !filepath.IsAbs(home) || !filepath.IsAbs(canonicalPath) || !productStoreIDPattern.MatchString(storeID) {
		return LocalMergePreview{}, errors.New("local merge preview input is invalid")
	}
	canonicalPath = filepath.Clean(canonicalPath)
	if err := requireRegularLocalMergeFile(canonicalPath); err != nil {
		return LocalMergePreview{}, err
	}
	canonicalDB, err := openLocalMergeReadOnly(canonicalPath)
	if err != nil {
		return LocalMergePreview{}, err
	}
	defer canonicalDB.Close()
	identity, err := readLocalMergeStoreIdentity(canonicalDB)
	if err != nil {
		return LocalMergePreview{}, err
	}
	if identity != storeID {
		return LocalMergePreview{}, ErrStoreIdentityMismatch
	}
	state, err := localMergeLogicalDigest(canonicalDB)
	if err != nil {
		return LocalMergePreview{}, err
	}
	previewID, err := newLocalMergeID()
	if err != nil {
		return LocalMergePreview{}, err
	}
	previewDir := filepath.Join(filepath.Dir(canonicalPath), "migration-previews", previewID)
	if err := os.MkdirAll(previewDir, 0o700); err != nil {
		return LocalMergePreview{}, err
	}
	if err := os.Chmod(previewDir, 0o700); err != nil {
		return LocalMergePreview{}, err
	}
	targetPath := filepath.Join(previewDir, "pulse.db")
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(previewDir)
		}
	}()
	if _, err := canonicalDB.Exec("VACUUM INTO '" + strings.ReplaceAll(targetPath, "'", "''") + "'"); err != nil {
		return LocalMergePreview{}, fmt.Errorf("create consistent Personal snapshot: %w", err)
	}
	convertedCanonical := false
	target, err := OpenVault(targetPath, StoreKindPersonal, storeID)
	if errors.Is(err, ErrUnsupportedTeamDatabase) {
		// Old mixed Team-era databases are valid read-only migration sources, but
		// Personal must never run them directly. Start a clean Personal database
		// and copy only the supported local-memory records below.
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if removeErr := os.Remove(targetPath + suffix); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return LocalMergePreview{}, fmt.Errorf("replace mixed database snapshot: %w", removeErr)
			}
		}
		target, err = OpenVault(targetPath, StoreKindPersonal, storeID)
		convertedCanonical = err == nil
	}
	if err != nil {
		return LocalMergePreview{}, fmt.Errorf("open new Personal database: %w", err)
	}

	preview := LocalMergePreview{
		Schema: LocalMergePreviewSchema, PreviewID: previewID,
		GeneratedAt: now.UTC().Format(time.RFC3339Nano), StoreID: storeID,
		CanonicalPath: canonicalPath, CanonicalStateDigest: state, TargetPath: targetPath,
		Conflicts: []LocalMergeConflict{}, CommitConfirmation: "merge local pulse memory",
	}
	canonicalInfo, _ := os.Stat(canonicalPath)
	canonicalCounts, countErr := localMergeCounts(canonicalDB)
	if countErr != nil {
		_ = target.Close()
		return LocalMergePreview{}, countErr
	}
	sources, err := DiscoverLocalMergeSources(home, canonicalPath)
	if err != nil {
		_ = target.Close()
		return LocalMergePreview{}, err
	}
	digests, err := loadTargetEventDigests(target.db)
	if err != nil {
		_ = target.Close()
		return LocalMergePreview{}, err
	}
	capsules, err := loadTargetCapsuleDigests(target.db)
	if err != nil {
		_ = target.Close()
		return LocalMergePreview{}, err
	}
	assertions, err := loadTargetAssertions(target.db)
	if err != nil {
		_ = target.Close()
		return LocalMergePreview{}, err
	}
	if convertedCanonical {
		source, mergeErr := mergeSQLiteSource(target.db, canonicalDB, canonicalPath, digests, capsules, assertions, &preview)
		if mergeErr != nil {
			_ = target.Close()
			return LocalMergePreview{}, mergeErr
		}
		source.Kind = "current_personal_conversion"
		source.State = state
		preview.Sources = append(preview.Sources, source)
	} else {
		preview.Sources = append(preview.Sources, LocalMergeSource{
			Path: canonicalPath, Kind: "current_personal", State: state,
			Size: canonicalInfo.Size(), Counts: canonicalCounts,
		})
	}
	for _, sourcePath := range sources {
		if strings.HasSuffix(sourcePath, ".json") {
			source, err := mergeStandaloneSource(target.db, sourcePath, digests, capsules, &preview)
			if err != nil {
				_ = target.Close()
				return LocalMergePreview{}, err
			}
			preview.Sources = append(preview.Sources, source)
			continue
		}
		sourceDB, err := openLocalMergeReadOnly(sourcePath)
		if err != nil {
			_ = target.Close()
			return LocalMergePreview{}, err
		}
		source, mergeErr := mergeSQLiteSource(target.db, sourceDB, sourcePath, digests, capsules, assertions, &preview)
		closeErr := sourceDB.Close()
		if mergeErr != nil {
			_ = target.Close()
			return LocalMergePreview{}, mergeErr
		}
		if closeErr != nil {
			_ = target.Close()
			return LocalMergePreview{}, closeErr
		}
		preview.Sources = append(preview.Sources, source)
	}
	if err := adoptMergedCapsules(target.db); err != nil {
		_ = target.Close()
		return LocalMergePreview{}, err
	}
	if err := validateLocalMergeDB(target.db); err != nil {
		_ = target.Close()
		return LocalMergePreview{}, err
	}
	if err := target.Close(); err != nil {
		return LocalMergePreview{}, err
	}
	preview.TargetSHA256, err = localMergeFileSHA256(targetPath)
	if err != nil {
		return LocalMergePreview{}, err
	}
	if len(preview.Conflicts) > 0 {
		preview.Status = "needs_review"
	} else {
		preview.Status = "ready"
	}
	cleanup = false
	return preview, nil
}

func WriteLocalMergePreview(path string, preview LocalMergePreview) error {
	if !filepath.IsAbs(path) || preview.Schema != LocalMergePreviewSchema {
		return errors.New("local merge preview output is invalid")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(preview, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, body, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func ReadLocalMergePreview(path string) (LocalMergePreview, error) {
	if !filepath.IsAbs(path) {
		return LocalMergePreview{}, errors.New("local merge preview path must be absolute")
	}
	if err := requireRegularLocalMergeFile(path); err != nil {
		return LocalMergePreview{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return LocalMergePreview{}, err
	}
	var preview LocalMergePreview
	if err := json.Unmarshal(body, &preview); err != nil {
		return LocalMergePreview{}, err
	}
	if preview.Schema != LocalMergePreviewSchema || preview.PreviewID == "" ||
		!filepath.IsAbs(preview.CanonicalPath) || !filepath.IsAbs(preview.TargetPath) ||
		!productStoreIDPattern.MatchString(preview.StoreID) || preview.CommitConfirmation != "merge local pulse memory" {
		return LocalMergePreview{}, errors.New("local merge preview is invalid")
	}
	return preview, nil
}

func CommitLocalMergePreview(previewPath, confirmation string, now time.Time) (string, error) {
	if confirmation != "merge local pulse memory" {
		return "", errors.New("local merge commit requires exact confirmation")
	}
	preview, err := ReadLocalMergePreview(previewPath)
	if err != nil {
		return "", err
	}
	for _, conflict := range preview.Conflicts {
		if conflict.Selected == "" {
			return "", fmt.Errorf("conflict %s still needs a choice", conflict.ID)
		}
		found := false
		for _, choice := range conflict.Choices {
			if choice.ID == conflict.Selected {
				found = true
			}
		}
		if !found {
			return "", fmt.Errorf("conflict %s has an invalid choice", conflict.ID)
		}
	}
	currentDB, err := openLocalMergeReadOnly(preview.CanonicalPath)
	if err != nil {
		return "", err
	}
	currentState, stateErr := localMergeLogicalDigest(currentDB)
	closeErr := currentDB.Close()
	if stateErr != nil {
		return "", stateErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if currentState != preview.CanonicalStateDigest {
		return "", errors.New("Personal memory changed after preview; create a fresh preview")
	}
	for _, source := range preview.Sources {
		if source.Kind == "current_personal" {
			continue
		}
		digest, err := localMergeFileSHA256(source.Path)
		if err != nil {
			return "", err
		}
		if digest != source.SHA256 {
			return "", fmt.Errorf("local memory source changed after preview: %s", source.Path)
		}
	}
	if digest, err := localMergeFileSHA256(preview.TargetPath); err != nil || digest != preview.TargetSHA256 {
		if err != nil {
			return "", err
		}
		return "", errors.New("prepared Personal database changed after preview")
	}
	target, err := OpenVault(preview.TargetPath, StoreKindPersonal, preview.StoreID)
	if err != nil {
		return "", err
	}
	if err := applyLocalMergeConflictChoices(target.db, preview.Conflicts); err != nil {
		_ = target.Close()
		return "", err
	}
	if err := validateLocalMergeDB(target.db); err != nil {
		_ = target.Close()
		return "", err
	}
	if err := target.Close(); err != nil {
		return "", err
	}
	archiveDir := filepath.Join(filepath.Dir(preview.CanonicalPath), "migration-archives",
		now.UTC().Format("20060102T150405.000000000Z")+"-"+preview.PreviewID)
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		return "", err
	}
	archiveDB := filepath.Join(archiveDir, "pulse.db")
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(preview.CanonicalPath + suffix); err == nil {
			if err := os.Rename(preview.CanonicalPath+suffix, archiveDB+suffix); err != nil {
				return "", err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	if err := os.Rename(preview.CanonicalPath, archiveDB); err != nil {
		return "", err
	}
	if err := os.Rename(preview.TargetPath, preview.CanonicalPath); err != nil {
		_ = os.Rename(archiveDB, preview.CanonicalPath)
		for _, suffix := range []string{"-wal", "-shm"} {
			_ = os.Rename(archiveDB+suffix, preview.CanonicalPath+suffix)
		}
		return "", err
	}
	return archiveDB, nil
}

func RestoreLocalMergeArchive(previewPath, archivePath string) error {
	preview, err := ReadLocalMergePreview(previewPath)
	if err != nil {
		return err
	}
	archivePath = filepath.Clean(archivePath)
	archiveRoot := filepath.Join(filepath.Dir(preview.CanonicalPath), "migration-archives")
	relative, err := filepath.Rel(archiveRoot, archivePath)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) || filepath.Base(archivePath) != "pulse.db" {
		return errors.New("local merge archive path is invalid")
	}
	if err := requireRegularLocalMergeFile(archivePath); err != nil {
		return err
	}
	if err := requireRegularLocalMergeFile(preview.CanonicalPath); err != nil {
		return err
	}
	if _, err := os.Lstat(preview.TargetPath); err == nil {
		return errors.New("local merge rollback target already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(preview.CanonicalPath, preview.TargetPath); err != nil {
		return err
	}
	movedNewSuffixes := []string{}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(preview.CanonicalPath + suffix); err == nil {
			if err := os.Rename(preview.CanonicalPath+suffix, preview.TargetPath+suffix); err != nil {
				for _, moved := range movedNewSuffixes {
					_ = os.Rename(preview.TargetPath+moved, preview.CanonicalPath+moved)
				}
				_ = os.Rename(preview.TargetPath, preview.CanonicalPath)
				return err
			}
			movedNewSuffixes = append(movedNewSuffixes, suffix)
		} else if !errors.Is(err, os.ErrNotExist) {
			for _, moved := range movedNewSuffixes {
				_ = os.Rename(preview.TargetPath+moved, preview.CanonicalPath+moved)
			}
			_ = os.Rename(preview.TargetPath, preview.CanonicalPath)
			return err
		}
	}
	if err := os.Rename(archivePath, preview.CanonicalPath); err != nil {
		for _, suffix := range movedNewSuffixes {
			_ = os.Rename(preview.TargetPath+suffix, preview.CanonicalPath+suffix)
		}
		_ = os.Rename(preview.TargetPath, preview.CanonicalPath)
		return err
	}
	movedArchiveSuffixes := []string{}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(archivePath + suffix); err == nil {
			if err := os.Rename(archivePath+suffix, preview.CanonicalPath+suffix); err != nil {
				for _, moved := range movedArchiveSuffixes {
					_ = os.Rename(preview.CanonicalPath+moved, archivePath+moved)
				}
				_ = os.Rename(preview.CanonicalPath, archivePath)
				for _, moved := range movedNewSuffixes {
					_ = os.Rename(preview.TargetPath+moved, preview.CanonicalPath+moved)
				}
				_ = os.Rename(preview.TargetPath, preview.CanonicalPath)
				return err
			}
			movedArchiveSuffixes = append(movedArchiveSuffixes, suffix)
		} else if !errors.Is(err, os.ErrNotExist) {
			for _, moved := range movedArchiveSuffixes {
				_ = os.Rename(preview.CanonicalPath+moved, archivePath+moved)
			}
			_ = os.Rename(preview.CanonicalPath, archivePath)
			for _, moved := range movedNewSuffixes {
				_ = os.Rename(preview.TargetPath+moved, preview.CanonicalPath+moved)
			}
			_ = os.Rename(preview.TargetPath, preview.CanonicalPath)
			return err
		}
	}
	return nil
}

func InspectLocalMergeStatus(home, canonicalPath string) (LocalMergeStatus, error) {
	status := LocalMergeStatus{Schema: "pulse.local_store_merge_status.v1", CanonicalPath: canonicalPath, Sources: []LocalMergeSource{}, Pending: []string{}}
	paths, err := DiscoverLocalMergeSources(home, canonicalPath)
	if err != nil {
		return status, err
	}
	paths = append([]string{canonicalPath}, paths...)
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return status, err
		}
		source := LocalMergeSource{Path: path, Size: info.Size(), Counts: map[string]int64{}}
		if path == canonicalPath {
			source.Kind = "current_personal"
		} else if strings.HasSuffix(path, ".json") {
			source.Kind = "standalone_json"
		} else {
			source.Kind = "local_sqlite"
		}
		if strings.HasSuffix(path, ".db") {
			db, err := openLocalMergeReadOnly(path)
			if err != nil {
				return status, err
			}
			source.Counts, err = localMergeCounts(db)
			_ = db.Close()
			if err != nil {
				return status, err
			}
		}
		status.Sources = append(status.Sources, source)
	}
	pending, _ := filepath.Glob(filepath.Join(filepath.Dir(canonicalPath), "migration-previews", "*", "pulse.db"))
	sort.Strings(pending)
	status.Pending = pending
	return status, nil
}

func mergeSQLiteSource(target, source *sql.DB, sourcePath string, digests map[string]int64, capsules map[string]string, assertions map[string][]mergeAssertion, preview *LocalMergePreview) (LocalMergeSource, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return LocalMergeSource{}, err
	}
	fingerprint, err := localMergeFileSHA256(sourcePath)
	if err != nil {
		return LocalMergeSource{}, err
	}
	counts, err := localMergeCounts(source)
	if err != nil {
		return LocalMergeSource{}, err
	}
	result := LocalMergeSource{Path: sourcePath, Kind: "local_sqlite", SHA256: fingerprint, Size: info.Size(), Counts: counts}
	if counts["events"] > 0 {
		if err := mergeEvents(target, source, digests, preview); err != nil {
			return LocalMergeSource{}, fmt.Errorf("merge events from %s: %w", sourcePath, err)
		}
	}
	if counts["memory_capsules"] > 0 {
		if err := mergeCapsules(target, source, capsules, preview); err != nil {
			return LocalMergeSource{}, fmt.Errorf("merge memories from %s: %w", sourcePath, err)
		}
	}
	if counts["assertions"] > 0 {
		if err := mergeAssertions(target, source, assertions, preview); err != nil {
			return LocalMergeSource{}, fmt.Errorf("merge assertions from %s: %w", sourcePath, err)
		}
	}
	return result, nil
}

func mergeAssertions(target, source *sql.DB, existing map[string][]mergeAssertion, preview *LocalMergePreview) error {
	rows, err := source.Query(`SELECT claim_key,predicate,object_text,qualifiers,confidence,valid_from,valid_to,system_from,source_event_ids,extractor_version,scope_type,scope_id,visibility,created_at
		FROM assertions WHERE status='active' AND system_to IS NULL ORDER BY claim_key,id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	tx, err := target.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for rows.Next() {
		var assertion mergeAssertion
		if err := rows.Scan(&assertion.ClaimKey, &assertion.Predicate, &assertion.ObjectText, &assertion.Qualifiers,
			&assertion.Confidence, &assertion.ValidFrom, &assertion.ValidTo, &assertion.SystemFrom,
			&assertion.SourceEventIDs, &assertion.ExtractorVersion, &assertion.ScopeType, &assertion.ScopeID,
			&assertion.Visibility, &assertion.CreatedAt); err != nil {
			return err
		}
		sanitizeMergeAssertion(&assertion)
		// Event IDs only have meaning inside their source database. Dropping
		// this optional provenance is safer than keeping a link to an unrelated
		// event after the assertion moves into the new Personal database.
		assertion.SourceEventIDs = sql.NullString{}
		current := existing[assertion.ClaimKey]
		matched := false
		for _, item := range current {
			if localMergeAssertionDigest(item) == localMergeAssertionDigest(assertion) {
				matched = true
				break
			}
		}
		if matched {
			preview.Totals.AssertionsDeduplicated++
			continue
		}
		if len(current) > 0 {
			conflict := localMergeAssertionConflict(current[0], assertion)
			if !localMergeHasConflict(preview.Conflicts, conflict.ID) {
				preview.Conflicts = append(preview.Conflicts, conflict)
			}
			continue
		}
		if err := insertMergedAssertionTx(tx, assertion); err != nil {
			return err
		}
		existing[assertion.ClaimKey] = append(existing[assertion.ClaimKey], assertion)
		preview.Totals.AssertionsCreated++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

func mergeEvents(target, source *sql.DB, digests map[string]int64, preview *LocalMergePreview) error {
	columns, err := localMergeColumns(source, "events")
	if err != nil {
		return err
	}
	expr := func(name, fallback string) string {
		if columns[name] {
			return name
		}
		return fallback
	}
	sentiment := `CASE WHEN typeof(sentiment) IN ('integer','real') THEN CAST(sentiment AS REAL) ELSE NULL END`
	sentimentLabel := `CASE WHEN typeof(sentiment)='text' THEN CAST(sentiment AS TEXT) ELSE NULL END`
	if columns["sentiment_label"] {
		sentimentLabel = `COALESCE(sentiment_label,CASE WHEN typeof(sentiment)='text' THEN CAST(sentiment AS TEXT) ELSE NULL END)`
	}
	rows, err := source.Query(`SELECT id,title,description,` + sentiment + `,emotional_weight,scorer_version,ts,` +
		expr("belief_class", "'operational'") + `,` + expr("confidence_floor", "0") + `,` + expr("archivable", "1") + `,` +
		expr("provenance", "'interactive_memory'") + `,` + expr("domain", "'real'") + `,` + expr("user_flag", "0") + `,` +
		sentimentLabel + `,` + expr("biometric_json", "NULL") + `,` + expr("tags", "NULL") + `,` +
		expr("access_count", "0") + `,` + expr("last_accessed_at", "NULL") + ` FROM events ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	tx, err := target.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	insert, err := tx.Prepare(`INSERT INTO events(
		title,description,sentiment,emotional_weight,scorer_version,ts,belief_class,confidence_floor,
		archivable,provenance,domain,user_flag,sentiment_label,biometric_json,tags,access_count,last_accessed_at,semantic_digest
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer insert.Close()
	idMap := map[int64]int64{}
	for rows.Next() {
		var event mergeEvent
		if err := rows.Scan(&event.ID, &event.Title, &event.Description, &event.Sentiment,
			&event.EmotionalWeight, &event.ScorerVersion, &event.OccurredAt, &event.BeliefClass,
			&event.ConfidenceFloor, &event.Archivable, &event.Provenance, &event.Domain, &event.UserFlag,
			&event.SentimentLabel, &event.BiometricJSON, &event.Tags, &event.AccessCount, &event.LastAccessedAt); err != nil {
			return err
		}
		sanitizeMergeEvent(&event)
		digest := localMergeEventDigest(event)
		if existing, ok := digests[digest]; ok {
			idMap[event.ID] = existing
			preview.Totals.EventsDeduplicated++
			continue
		}
		result, err := insert.Exec(event.Title, nullableValue(event.Description), nullableFloatValue(event.Sentiment),
			event.EmotionalWeight, nullableValue(event.ScorerVersion), event.OccurredAt, event.BeliefClass,
			event.ConfidenceFloor, event.Archivable, nullableValue(event.Provenance), event.Domain, event.UserFlag,
			nullableValue(event.SentimentLabel), nullableValue(event.BiometricJSON), nullableValue(event.Tags),
			event.AccessCount, nullableValue(event.LastAccessedAt), digest)
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		digests[digest] = id
		idMap[event.ID] = id
		preview.Totals.EventsCreated++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := mergeEventEmotionsTx(tx, source, idMap, preview); err != nil {
		return err
	}
	if err := mergeEventChainsTx(tx, source, idMap, preview); err != nil {
		return err
	}
	if err := mergeEventEmbeddingsTx(tx, source, idMap, preview); err != nil {
		return err
	}
	return tx.Commit()
}

func mergeEventEmotionsTx(tx *sql.Tx, source *sql.DB, idMap map[int64]int64, preview *LocalMergePreview) error {
	exists, err := localMergeTableExists(source, "event_emotions")
	if err != nil || !exists {
		return err
	}
	columns, err := localMergeColumns(source, "event_emotions")
	if err != nil {
		return err
	}
	expr := func(name, fallback string) string {
		if columns[name] {
			return name
		}
		return fallback
	}
	rows, err := source.Query(`SELECT event_id,joy,sadness,anger,fear,trust,disgust,anticipation,surprise,shame,guilt,tagger,tagger_version,confidence,updated_at,` +
		expr("derivation", "'inferred'") + `,` + expr("observed_label", "''") + `,` + expr("trigger_summary", "''") + `,` +
		expr("trigger_derivation", "''") + `,` + expr("trigger_confidence", "0") + `,` + expr("trigger_confirmed", "0") + `,` +
		expr("emotion_key", "''") + ` FROM event_emotions ORDER BY event_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sourceID int64
		var emotion mergeEmotion
		if err := rows.Scan(&sourceID, &emotion.Values[0], &emotion.Values[1], &emotion.Values[2], &emotion.Values[3],
			&emotion.Values[4], &emotion.Values[5], &emotion.Values[6], &emotion.Values[7], &emotion.Values[8],
			&emotion.Values[9], &emotion.Tagger, &emotion.TaggerVersion, &emotion.Confidence, &emotion.UpdatedAt,
			&emotion.Derivation, &emotion.ObservedLabel, &emotion.TriggerSummary, &emotion.TriggerDerivation,
			&emotion.TriggerConfidence, &emotion.TriggerConfirmed, &emotion.EmotionKey); err != nil {
			return err
		}
		targetID, ok := idMap[sourceID]
		if !ok {
			continue
		}
		sanitizeMergeEmotion(&emotion)
		existing, found, err := readTargetEmotionTx(tx, targetID)
		if err != nil {
			return err
		}
		if found {
			if localMergeEmotionDigest(existing) == localMergeEmotionDigest(emotion) {
				preview.Totals.EmotionsDeduplicated++
				continue
			}
			var eventTitle string
			if err := tx.QueryRow(`SELECT title FROM events WHERE id=?`, targetID).Scan(&eventTitle); err != nil {
				return err
			}
			preview.Conflicts = append(preview.Conflicts, localMergeEmotionConflict(targetID, eventTitle, existing, emotion))
			continue
		}
		if err := insertTargetEmotionTx(tx, targetID, emotion); err != nil {
			return err
		}
		preview.Totals.EmotionsCreated++
	}
	return rows.Err()
}

func mergeEventChainsTx(tx *sql.Tx, source *sql.DB, idMap map[int64]int64, preview *LocalMergePreview) error {
	exists, err := localMergeTableExists(source, "event_chains")
	if err != nil || !exists {
		return err
	}
	rows, err := source.Query(`SELECT parent_id,child_id,strength,kind,created_at FROM event_chains ORDER BY parent_id,child_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var parent, child int64
		var strength float64
		var kind, createdAt string
		if err := rows.Scan(&parent, &child, &strength, &kind, &createdAt); err != nil {
			return err
		}
		mappedParent, okParent := idMap[parent]
		mappedChild, okChild := idMap[child]
		if !okParent || !okChild || mappedParent == mappedChild {
			continue
		}
		result, err := tx.Exec(`INSERT OR IGNORE INTO event_chains(parent_id,child_id,strength,kind,created_at) VALUES(?,?,?,?,?)`, mappedParent, mappedChild, strength, kind, createdAt)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected > 0 {
			preview.Totals.ChainsCreated++
		}
	}
	return rows.Err()
}

func mergeEventEmbeddingsTx(tx *sql.Tx, source *sql.DB, idMap map[int64]int64, preview *LocalMergePreview) error {
	exists, err := localMergeTableExists(source, "event_embeddings")
	if err != nil || !exists {
		return err
	}
	rows, err := source.Query(`SELECT event_id,model,dim,vector_json,text_source,updated_at FROM event_embeddings ORDER BY event_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sourceID int64
		var model, vector, textSource, updatedAt string
		var dim int
		if err := rows.Scan(&sourceID, &model, &dim, &vector, &textSource, &updatedAt); err != nil {
			return err
		}
		targetID, ok := idMap[sourceID]
		if !ok {
			continue
		}
		result, err := tx.Exec(`INSERT OR IGNORE INTO event_embeddings(event_id,model,dim,vector_json,text_source,updated_at) VALUES(?,?,?,?,?,?)`, targetID, model, dim, vector, textSource, updatedAt)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected > 0 {
			preview.Totals.EmbeddingsCreated++
		}
	}
	return rows.Err()
}

func mergeCapsules(target, source *sql.DB, capsules map[string]string, preview *LocalMergePreview) error {
	columns, err := localMergeColumns(source, "memory_capsules")
	if err != nil {
		return err
	}
	expr := func(name, fallback string) string {
		if columns[name] {
			return name
		}
		return fallback
	}
	rows, err := source.Query(`SELECT id,schema_version,source_host,conversation_scope,source_timestamp,kind,redacted_summary,confidence,evidence_hint,privacy_tier,retention,tags,created_at,` +
		expr("status", "'active'") + `,` + expr("merged_into", "NULL") + `,` + expr("merged_at", "NULL") + ` FROM memory_capsules ORDER BY created_at,id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	tx, err := target.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for rows.Next() {
		var id, schema, host, scope, timestamp, kind, summary, hint, privacy, retention, tags, createdAt, status string
		var confidence float64
		var mergedInto, mergedAt sql.NullString
		if err := rows.Scan(&id, &schema, &host, &scope, &timestamp, &kind, &summary, &confidence, &hint, &privacy, &retention, &tags, &createdAt, &status, &mergedInto, &mergedAt); err != nil {
			return err
		}
		digest := localMergeCapsuleDigest(schema, kind, summary, confidence, hint, privacy, retention, tags)
		if _, ok := capsules[digest]; ok {
			preview.Totals.CapsulesDeduplicated++
			continue
		}
		newID := id
		var occupied int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM memory_capsules WHERE id=?`, newID).Scan(&occupied); err != nil {
			return err
		}
		if occupied > 0 {
			newID = "pulse:migrated:" + digest[:32]
		}
		if status == "" {
			status = "active"
		}
		_, err := tx.Exec(`INSERT INTO memory_capsules(id,schema_version,source_host,conversation_scope,source_timestamp,kind,redacted_summary,confidence,evidence_hint,privacy_tier,retention,tags,created_at,status,merged_into,merged_at,event_id,content_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?)`,
			newID, schema, host, scope, timestamp, kind, summary, confidence, hint, privacy, retention, tags, createdAt, status, nullableValue(mergedInto), nullableValue(mergedAt), digest)
		if err != nil {
			return err
		}
		capsules[digest] = newID
		preview.Totals.CapsulesCreated++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

func mergeStandaloneSource(target *sql.DB, sourcePath string, digests map[string]int64, capsules map[string]string, preview *LocalMergePreview) (LocalMergeSource, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return LocalMergeSource{}, err
	}
	body, err := os.ReadFile(sourcePath)
	if err != nil {
		return LocalMergeSource{}, err
	}
	var document struct {
		Schema string `json:"schema"`
		Items  []struct {
			ID, Schema, Kind, RedactedSummary, EvidenceHint, PrivacyTier, Retention, CreatedAt string
			Source                                                                             struct{ Host, ConversationScope, Timestamp string } `json:"source"`
			Confidence                                                                         float64                                             `json:"confidence"`
			Tags                                                                               []string                                            `json:"tags"`
		} `json:"items"`
		Graph struct {
			Events []map[string]any `json:"events"`
		} `json:"graph"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return LocalMergeSource{}, err
	}
	if document.Schema != "pulse.standalone_store.v1" {
		return LocalMergeSource{}, errors.New("unsupported standalone memory schema")
	}
	source := LocalMergeSource{Path: sourcePath, Kind: "standalone_json", SHA256: hex.EncodeToString(sha256Sum(body)), Size: info.Size(), Counts: map[string]int64{"memory_capsules": int64(len(document.Items)), "events": int64(len(document.Graph.Events))}}
	tx, err := target.Begin()
	if err != nil {
		return LocalMergeSource{}, err
	}
	defer tx.Rollback()
	for _, item := range document.Items {
		tagsBody, _ := json.Marshal(item.Tags)
		tags := string(tagsBody)
		digest := localMergeCapsuleDigest(item.Schema, item.Kind, item.RedactedSummary, item.Confidence, item.EvidenceHint, item.PrivacyTier, item.Retention, tags)
		if _, ok := capsules[digest]; ok {
			preview.Totals.CapsulesDeduplicated++
			continue
		}
		id := item.ID
		if id == "" {
			id = "pulse:migrated:" + digest[:32]
		}
		var occupied int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM memory_capsules WHERE id=?`, id).Scan(&occupied); err != nil {
			return LocalMergeSource{}, err
		}
		if occupied > 0 {
			id = "pulse:migrated:" + digest[:32]
		}
		_, err := tx.Exec(`INSERT INTO memory_capsules(id,schema_version,source_host,conversation_scope,source_timestamp,kind,redacted_summary,confidence,evidence_hint,privacy_tier,retention,tags,created_at,status,event_id,content_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'active',NULL,?)`,
			id, item.Schema, item.Source.Host, item.Source.ConversationScope, item.Source.Timestamp, item.Kind,
			item.RedactedSummary, item.Confidence, item.EvidenceHint, item.PrivacyTier, item.Retention, tags, item.CreatedAt, digest)
		if err != nil {
			return LocalMergeSource{}, err
		}
		capsules[digest] = id
		preview.Totals.CapsulesCreated++
	}
	for _, raw := range document.Graph.Events {
		title, _ := raw["title"].(string)
		summary, _ := raw["summary"].(string)
		occurred, _ := raw["occurred_at"].(string)
		if occurred == "" {
			occurred, _ = raw["created_at"].(string)
		}
		if title == "" || occurred == "" {
			continue
		}
		event := mergeEvent{Title: title, Description: sql.NullString{String: summary, Valid: summary != ""}, OccurredAt: occurred, BeliefClass: "operational", Archivable: 1, Provenance: sql.NullString{String: "interactive_memory", Valid: true}, Domain: "real"}
		digest := localMergeEventDigest(event)
		if _, ok := digests[digest]; ok {
			preview.Totals.EventsDeduplicated++
			continue
		}
		result, err := tx.Exec(`INSERT INTO events(title,description,emotional_weight,ts,belief_class,confidence_floor,archivable,provenance,domain,user_flag,access_count,semantic_digest) VALUES(?,?,0,?,'operational',0,1,'interactive_memory','real',0,0,?)`, title, nullableValue(event.Description), occurred, digest)
		if err != nil {
			return LocalMergeSource{}, err
		}
		id, _ := result.LastInsertId()
		digests[digest] = id
		preview.Totals.EventsCreated++
	}
	if err := tx.Commit(); err != nil {
		return LocalMergeSource{}, err
	}
	return source, nil
}

func adoptMergedCapsules(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := backfillPublishedPersonalCapsulesTx(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE personal_memory_scope_state SET eligibility_revision=eligibility_revision+1, updated_at=? WHERE singleton=1`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func loadTargetEventDigests(db *sql.DB) (map[string]int64, error) {
	rows, err := db.Query(`SELECT id,title,description,sentiment,emotional_weight,scorer_version,ts,belief_class,confidence_floor,archivable,provenance,domain,user_flag,sentiment_label,biometric_json,tags,access_count,last_accessed_at FROM events ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var event mergeEvent
		if err := rows.Scan(&event.ID, &event.Title, &event.Description, &event.Sentiment, &event.EmotionalWeight, &event.ScorerVersion, &event.OccurredAt, &event.BeliefClass, &event.ConfidenceFloor, &event.Archivable, &event.Provenance, &event.Domain, &event.UserFlag, &event.SentimentLabel, &event.BiometricJSON, &event.Tags, &event.AccessCount, &event.LastAccessedAt); err != nil {
			return nil, err
		}
		sanitizeMergeEvent(&event)
		digest := localMergeEventDigest(event)
		if _, exists := out[digest]; !exists {
			out[digest] = event.ID
		}
	}
	return out, rows.Err()
}

func loadTargetCapsuleDigests(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`SELECT id,schema_version,kind,redacted_summary,confidence,evidence_hint,privacy_tier,retention,tags FROM memory_capsules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, schema, kind, summary, hint, privacy, retention, tags string
		var confidence float64
		if err := rows.Scan(&id, &schema, &kind, &summary, &confidence, &hint, &privacy, &retention, &tags); err != nil {
			return nil, err
		}
		out[localMergeCapsuleDigest(schema, kind, summary, confidence, hint, privacy, retention, tags)] = id
	}
	return out, rows.Err()
}

func loadTargetAssertions(db *sql.DB) (map[string][]mergeAssertion, error) {
	rows, err := db.Query(`SELECT claim_key,predicate,object_text,qualifiers,confidence,valid_from,valid_to,system_from,source_event_ids,extractor_version,scope_type,scope_id,visibility,created_at
		FROM assertions WHERE status='active' AND system_to IS NULL ORDER BY claim_key,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]mergeAssertion{}
	for rows.Next() {
		var assertion mergeAssertion
		if err := rows.Scan(&assertion.ClaimKey, &assertion.Predicate, &assertion.ObjectText,
			&assertion.Qualifiers, &assertion.Confidence, &assertion.ValidFrom, &assertion.ValidTo,
			&assertion.SystemFrom, &assertion.SourceEventIDs, &assertion.ExtractorVersion,
			&assertion.ScopeType, &assertion.ScopeID, &assertion.Visibility, &assertion.CreatedAt); err != nil {
			return nil, err
		}
		sanitizeMergeAssertion(&assertion)
		out[assertion.ClaimKey] = append(out[assertion.ClaimKey], assertion)
	}
	return out, rows.Err()
}

func sanitizeMergeEvent(event *mergeEvent) {
	if event.BeliefClass == "" {
		event.BeliefClass = "operational"
	}
	if !map[string]bool{"axiom": true, "self_model": true, "user_model": true, "operational": true, "hypothesis": true}[event.BeliefClass] {
		event.BeliefClass = "operational"
	}
	if !map[string]bool{"real": true, "fiction_content": true, "fiction_meta": true, "meta_authorial": true}[event.Domain] {
		event.Domain = "real"
	}
	if event.ConfidenceFloor < 0 {
		event.ConfidenceFloor = 0
	}
	if event.ConfidenceFloor > 1 {
		event.ConfidenceFloor = 1
	}
	if event.Archivable != 0 {
		event.Archivable = 1
	}
	if event.UserFlag != 0 {
		event.UserFlag = 1
	}
}

func sanitizeMergeEmotion(emotion *mergeEmotion) {
	for index, value := range emotion.Values {
		if value < 0 {
			emotion.Values[index] = 0
		}
		if value > 1 {
			emotion.Values[index] = 1
		}
	}
	if emotion.Confidence < 0 {
		emotion.Confidence = 0
	}
	if emotion.Confidence > 1 {
		emotion.Confidence = 1
	}
	if !map[string]bool{"explicit": true, "inferred": true, "user_confirmed": true}[emotion.Derivation] {
		emotion.Derivation = "inferred"
	}
	if !map[string]bool{"": true, "explicit": true, "inferred": true, "user_confirmed": true}[emotion.TriggerDerivation] {
		emotion.TriggerDerivation = ""
	}
	if emotion.TriggerConfidence < 0 {
		emotion.TriggerConfidence = 0
	}
	if emotion.TriggerConfidence > 1 {
		emotion.TriggerConfidence = 1
	}
	if emotion.TriggerConfirmed != 0 {
		emotion.TriggerConfirmed = 1
	}
}

func sanitizeMergeAssertion(assertion *mergeAssertion) {
	assertion.ClaimKey = normalizeMergeText(assertion.ClaimKey)
	if assertion.ScopeType == "" || !map[string]bool{"personal": true, "project": true, "repo": true, "agent": true, "session": true}[assertion.ScopeType] {
		assertion.ScopeType = "personal"
	}
	if assertion.Visibility != "shared" {
		assertion.Visibility = "private"
	}
	if assertion.Confidence < 0 {
		assertion.Confidence = 0
	}
	if assertion.Confidence > 1 {
		assertion.Confidence = 1
	}
}

func localMergeEventDigest(event mergeEvent) string {
	tags := canonicalJSONStringArray(event.Tags.String)
	body, _ := json.Marshal([]any{
		normalizeMergeText(event.Title), normalizeMergeText(event.Description.String), event.BeliefClass,
		event.Domain, tags,
	})
	sum := sha256.Sum256(append([]byte("pulse-local-merge-event-v1\x1f"), body...))
	return hex.EncodeToString(sum[:])
}

func localMergeCapsuleDigest(schema, kind, summary string, confidence float64, hint, privacy, retention, tags string) string {
	body, _ := json.Marshal([]any{schema, kind, normalizeMergeText(summary), confidence, hint, privacy, retention, canonicalJSONStringArray(tags)})
	sum := sha256.Sum256(append([]byte("pulse-local-merge-capsule-v1\x1f"), body...))
	return hex.EncodeToString(sum[:])
}

func localMergeAssertionDigest(assertion mergeAssertion) string {
	body, _ := json.Marshal([]any{
		assertion.ClaimKey, normalizeMergeText(assertion.Predicate), normalizeMergeText(assertion.ObjectText),
		normalizeMergeText(assertion.Qualifiers.String), assertion.ScopeType, assertion.ScopeID, assertion.Visibility,
	})
	sum := sha256.Sum256(append([]byte("pulse-local-merge-assertion-v1\x1f"), body...))
	return hex.EncodeToString(sum[:])
}

func localMergeAssertionPayload(assertion mergeAssertion) map[string]any {
	return map[string]any{
		"claim_key": assertion.ClaimKey, "predicate": assertion.Predicate, "object_text": assertion.ObjectText,
		"qualifiers": nullableValue(assertion.Qualifiers), "confidence": assertion.Confidence,
		"valid_from": nullableValue(assertion.ValidFrom), "valid_to": nullableValue(assertion.ValidTo),
		"system_from": assertion.SystemFrom, "source_event_ids": nullableValue(assertion.SourceEventIDs),
		"extractor_version": nullableValue(assertion.ExtractorVersion), "scope_type": assertion.ScopeType,
		"scope_id": assertion.ScopeID, "visibility": assertion.Visibility, "created_at": assertion.CreatedAt,
	}
}

func localMergeAssertionConflict(current, imported mergeAssertion) LocalMergeConflict {
	sum := sha256.Sum256([]byte(current.ClaimKey + localMergeAssertionDigest(current) + localMergeAssertionDigest(imported)))
	return LocalMergeConflict{
		ID: "assertion_" + hex.EncodeToString(sum[:12]), Kind: "assertion",
		Question: "Which conflicting memory should remain?",
		Choices: []LocalMergeConflictChoice{
			{ID: "current", Label: current.ObjectText, Payload: localMergeAssertionPayload(current)},
			{ID: "imported", Label: imported.ObjectText, Payload: localMergeAssertionPayload(imported)},
		},
	}
}

func localMergeHasConflict(conflicts []LocalMergeConflict, id string) bool {
	for _, conflict := range conflicts {
		if conflict.ID == id {
			return true
		}
	}
	return false
}

func insertMergedAssertionTx(tx *sql.Tx, assertion mergeAssertion) error {
	_, err := tx.Exec(`INSERT INTO assertions(
		claim_key,subject_entity_id,predicate,object_text,object_entity_id,qualifiers,confidence,
		valid_from,valid_to,system_from,system_to,status,superseded_by,source_event_ids,
		extractor_version,scope_type,scope_id,visibility,created_at
	) VALUES(?,NULL,?,?,NULL,?,?,?,?,?,NULL,'active',NULL,?,?,?,?,?,?)`,
		assertion.ClaimKey, assertion.Predicate, assertion.ObjectText, nullableValue(assertion.Qualifiers),
		assertion.Confidence, nullableValue(assertion.ValidFrom), nullableValue(assertion.ValidTo), assertion.SystemFrom,
		nullableValue(assertion.SourceEventIDs), nullableValue(assertion.ExtractorVersion), assertion.ScopeType,
		assertion.ScopeID, assertion.Visibility, assertion.CreatedAt)
	return err
}

func normalizeMergeText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(norm.NFC.String(value)), " "))
}

func canonicalJSONStringArray(raw string) string {
	var values []string
	if json.Unmarshal([]byte(raw), &values) != nil {
		return raw
	}
	for index := range values {
		values[index] = normalizeMergeText(values[index])
	}
	sort.Strings(values)
	body, _ := json.Marshal(values)
	return string(body)
}

func localMergeEmotionDigest(emotion mergeEmotion) string {
	body, _ := json.Marshal(emotion)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func localMergeEmotionPayload(eventID int64, emotion mergeEmotion) map[string]any {
	return map[string]any{"target_event_id": eventID, "values": emotion.Values[:], "tagger": emotion.Tagger, "tagger_version": nullableValue(emotion.TaggerVersion), "confidence": emotion.Confidence, "updated_at": emotion.UpdatedAt, "derivation": emotion.Derivation, "observed_label": emotion.ObservedLabel, "trigger_summary": emotion.TriggerSummary, "trigger_derivation": emotion.TriggerDerivation, "trigger_confidence": emotion.TriggerConfidence, "trigger_confirmed": emotion.TriggerConfirmed, "emotion_key": emotion.EmotionKey}
}

func localMergeEmotionConflict(eventID int64, eventTitle string, current, imported mergeEmotion) LocalMergeConflict {
	sum := sha256.Sum256([]byte(strconv.FormatInt(eventID, 10) + localMergeEmotionDigest(current) + localMergeEmotionDigest(imported)))
	return LocalMergeConflict{
		ID: "emotion_" + hex.EncodeToString(sum[:12]), Kind: "emotion",
		Question: "Какая эмоциональная отметка верна для события «" + strings.TrimSpace(eventTitle) + "»?",
		Choices: []LocalMergeConflictChoice{
			{ID: "current", Label: "Текущая память: " + localMergeEmotionSummary(current), Payload: localMergeEmotionPayload(eventID, current)},
			{ID: "imported", Label: "Старая память: " + localMergeEmotionSummary(imported), Payload: localMergeEmotionPayload(eventID, imported)},
		},
	}
}

func localMergeEmotionSummary(emotion mergeEmotion) string {
	names := [...]string{"радость", "грусть", "злость", "страх", "доверие", "отвращение", "ожидание", "удивление", "стыд", "вина"}
	parts := []string{}
	for index, value := range emotion.Values {
		if value > 0 {
			parts = append(parts, fmt.Sprintf("%s %.2f", names[index], value))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "эмоция не указана")
	}
	parts = append(parts, fmt.Sprintf("уверенность %.2f", emotion.Confidence))
	if strings.TrimSpace(emotion.TriggerSummary) != "" {
		parts = append(parts, "причина: "+strings.TrimSpace(emotion.TriggerSummary))
	}
	return strings.Join(parts, "; ")
}

func readTargetEmotionTx(tx *sql.Tx, eventID int64) (mergeEmotion, bool, error) {
	var emotion mergeEmotion
	err := tx.QueryRow(`SELECT joy,sadness,anger,fear,trust,disgust,anticipation,surprise,shame,guilt,tagger,tagger_version,confidence,updated_at,derivation,observed_label,trigger_summary,trigger_derivation,trigger_confidence,trigger_confirmed,emotion_key FROM event_emotions WHERE event_id=?`, eventID).Scan(&emotion.Values[0], &emotion.Values[1], &emotion.Values[2], &emotion.Values[3], &emotion.Values[4], &emotion.Values[5], &emotion.Values[6], &emotion.Values[7], &emotion.Values[8], &emotion.Values[9], &emotion.Tagger, &emotion.TaggerVersion, &emotion.Confidence, &emotion.UpdatedAt, &emotion.Derivation, &emotion.ObservedLabel, &emotion.TriggerSummary, &emotion.TriggerDerivation, &emotion.TriggerConfidence, &emotion.TriggerConfirmed, &emotion.EmotionKey)
	if errors.Is(err, sql.ErrNoRows) {
		return mergeEmotion{}, false, nil
	}
	if err != nil {
		return mergeEmotion{}, false, err
	}
	return emotion, true, nil
}

func insertTargetEmotionTx(tx *sql.Tx, eventID int64, emotion mergeEmotion) error {
	_, err := tx.Exec(`INSERT INTO event_emotions(event_id,joy,sadness,anger,fear,trust,disgust,anticipation,surprise,shame,guilt,tagger,tagger_version,confidence,updated_at,derivation,observed_label,trigger_summary,trigger_derivation,trigger_confidence,trigger_confirmed,emotion_key) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, eventID, emotion.Values[0], emotion.Values[1], emotion.Values[2], emotion.Values[3], emotion.Values[4], emotion.Values[5], emotion.Values[6], emotion.Values[7], emotion.Values[8], emotion.Values[9], emotion.Tagger, nullableValue(emotion.TaggerVersion), emotion.Confidence, emotion.UpdatedAt, emotion.Derivation, emotion.ObservedLabel, emotion.TriggerSummary, emotion.TriggerDerivation, emotion.TriggerConfidence, emotion.TriggerConfirmed, emotion.EmotionKey)
	return err
}

func applyLocalMergeConflictChoices(db *sql.DB, conflicts []LocalMergeConflict) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, conflict := range conflicts {
		if conflict.Selected == "current" {
			continue
		}
		var payload map[string]any
		for _, choice := range conflict.Choices {
			if choice.ID == conflict.Selected {
				payload = choice.Payload
			}
		}
		if payload == nil {
			return fmt.Errorf("missing selected conflict payload: %s", conflict.ID)
		}
		if conflict.Kind == "assertion" {
			assertion, err := mergeAssertionFromPayload(payload)
			if err != nil {
				return fmt.Errorf("conflict %s: %w", conflict.ID, err)
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			rows, err := tx.Query(`SELECT id FROM assertions WHERE claim_key=? AND status='active' AND system_to IS NULL`, assertion.ClaimKey)
			if err != nil {
				return err
			}
			var previous []int64
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					_ = rows.Close()
					return err
				}
				previous = append(previous, id)
			}
			if err := rows.Close(); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE assertions SET status='superseded',system_to=? WHERE claim_key=? AND status='active' AND system_to IS NULL`, now, assertion.ClaimKey); err != nil {
				return err
			}
			if err := insertMergedAssertionTx(tx, assertion); err != nil {
				return err
			}
			newID, err := lastInsertedAssertionIDTx(tx)
			if err != nil {
				return err
			}
			for _, id := range previous {
				if _, err := tx.Exec(`UPDATE assertions SET superseded_by=? WHERE id=?`, newID, id); err != nil {
					return err
				}
			}
			continue
		}
		if conflict.Kind != "emotion" {
			return fmt.Errorf("unsupported conflict kind: %s", conflict.Kind)
		}
		eventID, ok := numberAsInt64(payload["target_event_id"])
		if !ok {
			return fmt.Errorf("conflict %s has no target event", conflict.ID)
		}
		emotion, err := mergeEmotionFromPayload(payload)
		if err != nil {
			return fmt.Errorf("conflict %s: %w", conflict.ID, err)
		}
		if _, err := tx.Exec(`DELETE FROM event_emotions WHERE event_id=?`, eventID); err != nil {
			return err
		}
		if err := insertTargetEmotionTx(tx, eventID, emotion); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func lastInsertedAssertionIDTx(tx *sql.Tx) (int64, error) {
	var id int64
	err := tx.QueryRow(`SELECT last_insert_rowid()`).Scan(&id)
	return id, err
}

func mergeAssertionFromPayload(payload map[string]any) (mergeAssertion, error) {
	var assertion mergeAssertion
	assertion.ClaimKey, _ = payload["claim_key"].(string)
	assertion.Predicate, _ = payload["predicate"].(string)
	assertion.ObjectText, _ = payload["object_text"].(string)
	if assertion.ClaimKey == "" || assertion.Predicate == "" || assertion.ObjectText == "" {
		return assertion, errors.New("assertion is incomplete")
	}
	setNullString := func(key string, destination *sql.NullString) {
		if value, ok := payload[key].(string); ok {
			*destination = sql.NullString{String: value, Valid: true}
		}
	}
	setNullString("qualifiers", &assertion.Qualifiers)
	setNullString("valid_from", &assertion.ValidFrom)
	setNullString("valid_to", &assertion.ValidTo)
	setNullString("source_event_ids", &assertion.SourceEventIDs)
	setNullString("extractor_version", &assertion.ExtractorVersion)
	assertion.Confidence, _ = payload["confidence"].(float64)
	assertion.SystemFrom, _ = payload["system_from"].(string)
	assertion.ScopeType, _ = payload["scope_type"].(string)
	assertion.ScopeID, _ = payload["scope_id"].(string)
	assertion.Visibility, _ = payload["visibility"].(string)
	assertion.CreatedAt, _ = payload["created_at"].(string)
	if assertion.SystemFrom == "" {
		assertion.SystemFrom = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if assertion.CreatedAt == "" {
		assertion.CreatedAt = assertion.SystemFrom
	}
	sanitizeMergeAssertion(&assertion)
	return assertion, nil
}

func numberAsInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		integer := int64(typed)
		return integer, float64(integer) == typed && integer > 0
	case int64:
		return typed, typed > 0
	case json.Number:
		integer, err := typed.Int64()
		return integer, err == nil && integer > 0
	default:
		return 0, false
	}
}

func mergeEmotionFromPayload(payload map[string]any) (mergeEmotion, error) {
	var emotion mergeEmotion
	values, ok := payload["values"].([]any)
	if !ok || len(values) != len(emotion.Values) {
		return emotion, errors.New("emotion values are invalid")
	}
	for index, raw := range values {
		value, ok := raw.(float64)
		if !ok {
			return emotion, errors.New("emotion value is invalid")
		}
		emotion.Values[index] = value
	}
	emotion.Tagger, _ = payload["tagger"].(string)
	if taggerVersion, ok := payload["tagger_version"].(string); ok {
		emotion.TaggerVersion = sql.NullString{String: taggerVersion, Valid: true}
	}
	emotion.Confidence, _ = payload["confidence"].(float64)
	emotion.UpdatedAt, _ = payload["updated_at"].(string)
	emotion.Derivation, _ = payload["derivation"].(string)
	emotion.ObservedLabel, _ = payload["observed_label"].(string)
	emotion.TriggerSummary, _ = payload["trigger_summary"].(string)
	emotion.TriggerDerivation, _ = payload["trigger_derivation"].(string)
	emotion.TriggerConfidence, _ = payload["trigger_confidence"].(float64)
	if confirmed, ok := numberAsInt64(payload["trigger_confirmed"]); ok {
		emotion.TriggerConfirmed = int(confirmed)
	}
	emotion.EmotionKey, _ = payload["emotion_key"].(string)
	sanitizeMergeEmotion(&emotion)
	return emotion, nil
}

func validateLocalMergeDB(db *sql.DB) error {
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("new Personal database failed integrity check: %s", integrity)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("new Personal database has broken links")
	}
	var orphanEmotions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_emotions emotion LEFT JOIN events event ON event.id=emotion.event_id WHERE event.id IS NULL`).Scan(&orphanEmotions); err != nil {
		return err
	}
	if orphanEmotions != 0 {
		return errors.New("new Personal database has unlinked emotions")
	}
	return rows.Err()
}

func localMergeLogicalDigest(db *sql.DB) (string, error) {
	hash := sha256.New()
	for _, table := range []string{"events", "event_emotions", "event_chains", "memory_capsules", "assertions"} {
		exists, err := localMergeTableExists(db, table)
		if err != nil {
			return "", err
		}
		if !exists {
			continue
		}
		rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
		if err != nil {
			return "", err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return "", err
		}
		_, _ = io.WriteString(hash, table+"\x1f"+strings.Join(columns, "\x1e")+"\n")
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				return "", err
			}
			for _, value := range values {
				switch typed := value.(type) {
				case nil:
					_, _ = io.WriteString(hash, "N;")
				case []byte:
					_, _ = hash.Write([]byte("B"))
					_, _ = hash.Write(typed)
					_, _ = io.WriteString(hash, ";")
				default:
					_, _ = io.WriteString(hash, "V"+fmt.Sprint(typed)+";")
				}
			}
			_, _ = io.WriteString(hash, "\n")
		}
		if err := rows.Close(); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func localMergeCounts(db *sql.DB) (map[string]int64, error) {
	out := map[string]int64{}
	for _, table := range []string{"events", "event_emotions", "event_chains", "memory_capsules", "assertions"} {
		exists, err := localMergeTableExists(db, table)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		var count int64
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			return nil, err
		}
		out[table] = count
	}
	return out, nil
}

func localMergeTableExists(db *sql.DB, table string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n)
	return n == 1, err
}
func localMergeColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def any
		if err := rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func openLocalMergeReadOnly(path string) (*sql.DB, error) {
	if err := requireRegularLocalMergeFile(path); err != nil {
		return nil, err
	}
	dsn := "file:" + strings.ReplaceAll(filepath.Clean(path), "?", "%3F") + "?mode=ro&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func requireRegularLocalMergeFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("local memory file is unsafe")
	}
	return nil
}
func readLocalMergeStoreIdentity(db *sql.DB) (string, error) {
	var id, kind string
	if err := db.QueryRow(`SELECT store_id,store_kind FROM store_identity WHERE singleton=1`).Scan(&id, &kind); err != nil {
		return "", err
	}
	if kind != string(StoreKindPersonal) {
		return "", errors.New("current memory is not a Personal database")
	}
	return id, nil
}
func newLocalMergeID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "merge_" + hex.EncodeToString(b[:]), nil
}
func localMergeFileSHA256(path string) (string, error) {
	if err := requireRegularLocalMergeFile(path); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, 1024*1024)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func sha256Sum(body []byte) []byte { sum := sha256.Sum256(body); return sum[:] }
func nullableValue(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}
func nullableFloatValue(value sql.NullFloat64) any {
	if value.Valid {
		return value.Float64
	}
	return nil
}
