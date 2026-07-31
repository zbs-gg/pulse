package historicalingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

type materialCore struct {
	Kind            MaterialKind    `json:"kind"`
	Confidence      float64         `json:"confidence"`
	Privacy         Privacy         `json:"privacy"`
	EpistemicStatus EpistemicStatus `json:"epistemic_status"`
	Derivation      Derivation      `json:"derivation"`
	ValidTime       ValidTime       `json:"valid_time"`
	Scope           Scope           `json:"scope"`
	Payload         MaterialPayload `json:"payload"`
}

func MergeAcceptedResults(jobID, snapshotDigest string, results []WorkUnitResult) (Manifest, error) {
	groups := map[string]MaterialItem{}
	for _, result := range results {
		if err := validateWorkUnitResult(result); err != nil {
			return Manifest{}, err
		}
		for _, source := range result.Items {
			item := source
			item.SourceRefs = sortedUniqueSourceRefs(item.SourceRefs)
			identity := materialIdentity(item)
			item.CandidateID = "candidate_" + identity
			if current, ok := groups[identity]; ok {
				current.SourceRefs = sortedUniqueSourceRefs(append(current.SourceRefs, item.SourceRefs...))
				groups[identity] = current
			} else {
				groups[identity] = item
			}
		}
	}
	items := make([]MaterialItem, 0, len(groups))
	for _, item := range groups {
		items = append(items, item)
	}
	markAssertionConflicts(items)
	final := map[string]MaterialItem{}
	for _, item := range items {
		identity := materialIdentity(item)
		item.CandidateID = "candidate_" + identity
		if current, ok := final[identity]; ok {
			current.SourceRefs = sortedUniqueSourceRefs(append(current.SourceRefs, item.SourceRefs...))
			final[identity] = current
		} else {
			final[identity] = item
		}
	}
	items = items[:0]
	for _, item := range final {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].CandidateID < items[j].CandidateID
	})
	manifest := Manifest{SchemaVersion: SchemaVersionV1, JobID: jobID, Revision: 1, SourceSnapshotDigest: snapshotDigest, Items: items}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func materialIdentity(item MaterialItem) string {
	core := materialCore{item.Kind, item.Confidence, item.Privacy, item.EpistemicStatus, item.Derivation, item.ValidTime, item.Scope, item.Payload}
	encoded, _ := json.Marshal(core)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func sortedUniqueSourceRefs(refs []SourceRef) []SourceRef {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Alias != refs[j].Alias {
			return refs[i].Alias < refs[j].Alias
		}
		if refs[i].PrefixDigest != refs[j].PrefixDigest {
			return refs[i].PrefixDigest < refs[j].PrefixDigest
		}
		return refs[i].RecordLocator < refs[j].RecordLocator
	})
	unique := refs[:0]
	for _, ref := range refs {
		if len(unique) == 0 || unique[len(unique)-1] != ref {
			unique = append(unique, ref)
		}
	}
	return append([]SourceRef(nil), unique...)
}

func markAssertionConflicts(items []MaterialItem) {
	values := map[string]map[string]struct{}{}
	for _, item := range items {
		if item.Kind != MaterialKindAssertion {
			continue
		}
		key := item.Payload.SubjectID + "\x1f" + item.Payload.Predicate
		if values[key] == nil {
			values[key] = map[string]struct{}{}
		}
		values[key][item.Payload.ObjectValue] = struct{}{}
	}
	for index := range items {
		item := &items[index]
		if item.Kind == MaterialKindAssertion && len(values[item.Payload.SubjectID+"\x1f"+item.Payload.Predicate]) > 1 {
			item.EpistemicStatus = EpistemicConflict
		}
	}
}
