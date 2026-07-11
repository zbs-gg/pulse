package retrieve

import (
	"context"
	"sort"
	"strings"
)

const maxTeamResumeDocuments = 50

func (engine *TeamRetrievalEngine) Resume(
	ctx context.Context,
	repository TeamAuthorizedRepository,
	request TeamResumeRequest,
) (TeamResumeResponse, error) {
	empty := emptyTeamResumeResponse()
	if engine == nil || repository == nil ||
		(strings.TrimSpace(request.ThreadID) == "" &&
			strings.TrimSpace(request.ProjectID) == "" &&
			strings.TrimSpace(request.SessionID) == "") ||
		request.Limit < 0 || request.Limit > maxTeamResumeDocuments {
		return empty, ErrInvalidTeamRetrievalRequest
	}
	limit := request.Limit
	if limit == 0 {
		limit = 20
	}
	documents, err := repository.LoadAuthorizedContinuity(ctx, TeamResumeQuery{
		ThreadID: request.ThreadID, ProjectID: request.ProjectID,
		SessionID: request.SessionID, Limit: engine.candidateLimit,
	})
	if err != nil {
		return empty, err
	}
	filtered := make([]TeamContinuityDocument, 0, len(documents))
	objectIndexes := make(map[string]int, len(documents))
	uniqueObjects := make(map[string]bool, engine.candidateLimit)
	for _, document := range documents {
		if !validTeamContinuityDocument(document) {
			return empty, ErrInvalidTeamAuthorizedCorpus
		}
		if !uniqueObjects[document.ObjectID] {
			if len(uniqueObjects) >= engine.candidateLimit {
				return empty, ErrInvalidTeamAuthorizedCorpus
			}
			uniqueObjects[document.ObjectID] = true
		}
		if request.ThreadID != "" && document.ThreadID != request.ThreadID {
			continue
		}
		if request.ProjectID != "" && document.ProjectID != request.ProjectID {
			continue
		}
		if request.SessionID != "" && document.SessionID != request.SessionID {
			continue
		}
		if index, exists := objectIndexes[document.ObjectID]; exists {
			existing := filtered[index]
			if !sameTeamContinuityDerivative(existing, document) {
				return empty, ErrInvalidTeamAuthorizedCorpus
			}
			if document.UpdatedAtUnixMilli > existing.UpdatedAtUnixMilli ||
				(document.UpdatedAtUnixMilli == existing.UpdatedAtUnixMilli &&
					document.RootObjectID < existing.RootObjectID) {
				filtered[index] = document
			}
			continue
		}
		objectIndexes[document.ObjectID] = len(filtered)
		filtered = append(filtered, document)
	}
	sort.SliceStable(filtered, func(left, right int) bool {
		if filtered[left].UpdatedAtUnixMilli != filtered[right].UpdatedAtUnixMilli {
			return filtered[left].UpdatedAtUnixMilli > filtered[right].UpdatedAtUnixMilli
		}
		if filtered[left].RootObjectID != filtered[right].RootObjectID {
			return filtered[left].RootObjectID < filtered[right].RootObjectID
		}
		return filtered[left].ObjectID < filtered[right].ObjectID
	})
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	response := empty
	seenReturnedRoots := make(map[string]bool, len(filtered))
	for _, document := range filtered {
		appendResumeText := func(destination *[]TeamResumeItem, text string) {
			if response.ReturnedCount >= limit || strings.TrimSpace(text) == "" {
				return
			}
			*destination = append(*destination, TeamResumeItem{
				RootObjectID: document.RootObjectID, ObjectID: document.ObjectID, Text: text,
			})
			seenReturnedRoots[document.RootObjectID] = true
			response.ReturnedCount++
		}
		appendResumeText(&response.WhereWeLeftOff, document.Summary)
		for _, text := range document.Decisions {
			appendResumeText(&response.ActiveDecisions, text)
		}
		for _, text := range document.OpenLoops {
			appendResumeText(&response.OpenLoops, text)
		}
		for _, text := range document.DoNotRepeat {
			appendResumeText(&response.DoNotRepeat, text)
		}
		for _, text := range document.EmotionalStateContext {
			appendResumeText(&response.RelevantEmotionalStateContext, text)
		}
		appendResumeText(&response.SuggestedNextStep, document.SuggestedNextStep)
	}
	roots := make([]string, 0, len(seenReturnedRoots))
	for rootID := range seenReturnedRoots {
		roots = append(roots, rootID)
	}
	sort.Strings(roots)
	if err := repository.RecheckAuthorizedRoots(ctx, roots); err != nil {
		return empty, err
	}
	return response, nil
}

func validTeamContinuityDocument(document TeamContinuityDocument) bool {
	return document.RootObjectID != "" && document.ObjectID != "" &&
		document.PartitionKey != "" && document.ThreadID != "" &&
		strings.TrimSpace(document.Summary) != "" && document.UpdatedAtUnixMilli >= 0
}

func sameTeamContinuityDerivative(left, right TeamContinuityDocument) bool {
	return left.ObjectID == right.ObjectID && left.PartitionKey == right.PartitionKey &&
		left.ThreadID == right.ThreadID && left.ProjectID == right.ProjectID &&
		left.SessionID == right.SessionID
}

func emptyTeamResumeResponse() TeamResumeResponse {
	return TeamResumeResponse{
		WhereWeLeftOff:  make([]TeamResumeItem, 0),
		ActiveDecisions: make([]TeamResumeItem, 0),
		OpenLoops:       make([]TeamResumeItem, 0), DoNotRepeat: make([]TeamResumeItem, 0),
		RelevantEmotionalStateContext: make([]TeamResumeItem, 0),
		SuggestedNextStep:             make([]TeamResumeItem, 0),
	}
}
