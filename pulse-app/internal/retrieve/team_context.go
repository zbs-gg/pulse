package retrieve

import (
	"context"
	"math"
	"sort"
	"strings"
)

const (
	teamGraphSeedLimit                = 5
	teamGraphHopLimit                 = 2
	teamGraphMinStrength              = 0.5
	teamAssertionSupersededMultiplier = 0.5
)

type scoredTeamContextDocument struct {
	kind              string
	rootObjectID      string
	objectID          string
	partitionKey      string
	text              string
	lexical           float64
	cosine            float64
	graph             float64
	score             float64
	assertionState    string
	contributionRoots map[string]bool
	influenceRoots    map[string]bool
	payload           any
}

func (engine *TeamRetrievalEngine) Context(
	ctx context.Context,
	repository TeamAuthorizedRepository,
	request TeamContextRequest,
) (TeamContextResponse, error) {
	empty := emptyTeamContextResponse(request.IncludeTrace)
	graphMode := request.GraphMode
	if graphMode == "" {
		graphMode = TeamGraphModeOff
	}
	if engine == nil || repository == nil || strings.TrimSpace(request.Query) == "" ||
		request.TopK < 0 || request.TopK > maxTeamTopK || !validTeamGraphMode(graphMode) {
		return empty, ErrInvalidTeamRetrievalRequest
	}
	topK := request.TopK
	if topK == 0 {
		topK = 10
	}
	corpus, err := repository.LoadAuthorizedCorpus(ctx, TeamCorpusQuery{
		Query: request.Query, Limit: engine.candidateLimit,
		EmbeddingModel: engine.EmbeddingModel(),
		Surface:        TeamCorpusSurfaceContext,
	})
	if err != nil {
		return empty, err
	}
	documents, byObject, err := buildTeamContextDocuments(corpus, engine.candidateLimit)
	if err != nil {
		return empty, err
	}
	documents = pruneIneligibleTeamContextDocuments(documents, byObject)
	queryTokens := teamSearchTokens(request.Query)
	for _, document := range documents {
		document.lexical = teamLexicalScore(queryTokens, document.text)
		document.score = document.lexical
	}
	if countUniqueTeamSemanticEmbeddings(corpus.SemanticEmbeddings) > engine.candidateLimit {
		return empty, ErrInvalidTeamAuthorizedCorpus
	}
	if engine.embedder != nil {
		queryVector, err := engine.embedTeamQuery(ctx, request.Query)
		if err != nil {
			return empty, err
		}
		if len(corpus.SemanticEmbeddings) != 0 {
			if err := applyTeamSemanticCosine(
				engine.embedder.Model(), queryVector, byObject, corpus.SemanticEmbeddings,
			); err != nil {
				return empty, err
			}
		}
	}
	if graphMode != TeamGraphModeOff {
		if err := applyTeamGraphScores(documents, byObject, corpus.GraphLinks, graphMode); err != nil {
			return empty, err
		}
	}
	applyTeamAssertionReduction(byObject, corpus.Assertions)
	ranked := make([]*scoredTeamContextDocument, 0, len(documents))
	for _, document := range documents {
		if document.score > 0 {
			ranked = append(ranked, document)
		}
	}
	sort.SliceStable(ranked, func(left, right int) bool {
		if ranked[left].score != ranked[right].score {
			return ranked[left].score > ranked[right].score
		}
		if ranked[left].rootObjectID != ranked[right].rootObjectID {
			return ranked[left].rootObjectID < ranked[right].rootObjectID
		}
		return ranked[left].objectID < ranked[right].objectID
	})
	selected := selectTeamContextDocuments(ranked, byObject, topK)
	rootIDs := teamUniqueSortedContextRoots(selected)
	if err := repository.RecheckAuthorizedRoots(ctx, rootIDs); err != nil {
		return empty, err
	}
	response := empty
	for _, document := range selected {
		appendTeamContextResult(&response, document)
		if request.IncludeTrace {
			response.Trace = append(response.Trace, TeamContextTrace{
				RootObjectID: document.rootObjectID, ObjectID: document.objectID,
				Kind: document.kind, Lexical: document.lexical, Cosine: document.cosine,
				Graph: document.graph, Score: clampTeamScore(document.score),
				AssertionState: document.assertionState,
			})
		}
	}
	response.Counts = TeamContextCounts{
		Entities: len(response.Entities), Relations: len(response.Relations),
		Facts: len(response.Facts), Events: len(response.Events),
		Assertions: len(response.Assertions), Total: len(selected),
	}
	return response, nil
}

func applyTeamSemanticCosine(
	model string,
	queryVector []float32,
	byObject map[string]*scoredTeamContextDocument,
	embeddings []TeamSemanticEmbeddingDocument,
) error {
	seenEmbeddingObjects := make(map[string]bool, len(embeddings))
	seenSources := make(map[string]bool, len(embeddings))
	for _, embedding := range embeddings {
		source := byObject[embedding.SourceObjectID]
		if source == nil || source.rootObjectID != embedding.RootObjectID ||
			source.partitionKey != embedding.PartitionKey {
			// Only the contribution that backs the deterministically selected
			// source derivative is allowed to affect ranking or validation.
			continue
		}
		if embedding.EmbeddingObjectID == "" || embedding.Model != model ||
			len(embedding.Vector) == 0 || seenEmbeddingObjects[embedding.EmbeddingObjectID] ||
			seenSources[embedding.SourceObjectID] || len(embedding.Vector) != len(queryVector) {
			return ErrInvalidTeamAuthorizedCorpus
		}
		seenEmbeddingObjects[embedding.EmbeddingObjectID] = true
		seenSources[embedding.SourceObjectID] = true
		cosine, ok := teamCosine(queryVector, embedding.Vector)
		if !ok {
			return ErrInvalidTeamAuthorizedCorpus
		}
		if cosine < 0 {
			cosine = 0
		}
		source.cosine = cosine
		source.score = source.lexical + source.cosine + source.graph
	}
	return nil
}

func buildTeamContextDocuments(
	corpus TeamAuthorizedCorpus,
	candidateLimit int,
) ([]*scoredTeamContextDocument, map[string]*scoredTeamContextDocument, error) {
	documents := make([]*scoredTeamContextDocument, 0, candidateLimit)
	byObject := make(map[string]*scoredTeamContextDocument, candidateLimit)
	add := func(document *scoredTeamContextDocument) error {
		if document.rootObjectID == "" || document.objectID == "" ||
			document.partitionKey == "" || strings.TrimSpace(document.text) == "" {
			return ErrInvalidTeamAuthorizedCorpus
		}
		if existing := byObject[document.objectID]; existing != nil {
			if !mergeTeamContextContribution(existing, document) {
				return ErrInvalidTeamAuthorizedCorpus
			}
			return nil
		}
		if len(byObject) >= candidateLimit {
			return ErrInvalidTeamAuthorizedCorpus
		}
		document.contributionRoots = map[string]bool{document.rootObjectID: true}
		document.influenceRoots = map[string]bool{document.rootObjectID: true}
		byObject[document.objectID] = document
		documents = append(documents, document)
		return nil
	}
	for _, entity := range corpus.Entities {
		if entity.EntityKind == "" || entity.Name == "" || !validTeamUnitFloat(entity.Confidence) ||
			add(&scoredTeamContextDocument{
				kind: "entity", rootObjectID: entity.RootObjectID, objectID: entity.ObjectID,
				partitionKey: entity.PartitionKey, text: strings.TrimSpace(entity.Name + " " + entity.Summary),
				payload: entity,
			}) != nil {
			return nil, nil, ErrInvalidTeamAuthorizedCorpus
		}
	}
	for _, relation := range corpus.Relations {
		if relation.FromObjectID == "" || relation.ToObjectID == "" || relation.RelationKind == "" ||
			!validTeamUnitFloat(relation.Strength) || !validTeamUnitFloat(relation.Confidence) ||
			add(&scoredTeamContextDocument{
				kind: "relation", rootObjectID: relation.RootObjectID, objectID: relation.ObjectID,
				partitionKey: relation.PartitionKey,
				text:         strings.TrimSpace(relation.RelationKind + " " + relation.Summary), payload: relation,
			}) != nil {
			return nil, nil, ErrInvalidTeamAuthorizedCorpus
		}
	}
	for _, fact := range corpus.Facts {
		if fact.NodeObjectID == "" || fact.Domain == "" || !validTeamUnitFloat(fact.Confidence) ||
			add(&scoredTeamContextDocument{
				kind: "fact", rootObjectID: fact.RootObjectID, objectID: fact.ObjectID,
				partitionKey: fact.PartitionKey,
				text:         strings.TrimSpace(fact.Text + " " + fact.Predicate + " " + fact.ObjectText), payload: fact,
			}) != nil {
			return nil, nil, ErrInvalidTeamAuthorizedCorpus
		}
	}
	for _, event := range corpus.Events {
		if event.Title == "" || event.Domain == "" || !validTeamUnitFloat(event.Confidence) ||
			add(&scoredTeamContextDocument{
				kind: "event", rootObjectID: event.RootObjectID, objectID: event.ObjectID,
				partitionKey: event.PartitionKey,
				text:         strings.TrimSpace(event.Title + " " + event.Summary), payload: event,
			}) != nil {
			return nil, nil, ErrInvalidTeamAuthorizedCorpus
		}
	}
	for _, assertion := range corpus.Assertions {
		if assertion.CandidateObjectID == "" || assertion.SubjectObjectID == "" ||
			assertion.ClaimSlotDigest == "" || assertion.Predicate == "" || assertion.ObjectText == "" ||
			assertion.ObservedAtUnixMilli < 0 || !validTeamUnitFloat(assertion.Confidence) {
			return nil, nil, ErrInvalidTeamAuthorizedCorpus
		}
		if !eligibleTeamAssertionContribution(assertion, byObject) {
			continue
		}
		if add(&scoredTeamContextDocument{
			kind: "assertion", rootObjectID: assertion.RootObjectID, objectID: assertion.ObjectID,
			partitionKey: assertion.PartitionKey,
			text:         strings.TrimSpace(assertion.Text + " " + assertion.Predicate + " " + assertion.ObjectText),
			payload:      assertion,
		}) != nil {
			return nil, nil, ErrInvalidTeamAuthorizedCorpus
		}
	}
	return documents, byObject, nil
}

func countUniqueTeamSemanticEmbeddings(embeddings []TeamSemanticEmbeddingDocument) int {
	unique := make(map[string]bool, len(embeddings))
	for _, embedding := range embeddings {
		unique[embedding.EmbeddingObjectID] = true
	}
	return len(unique)
}

func mergeTeamContextContribution(
	left, right *scoredTeamContextDocument,
) bool {
	if left.kind != right.kind || left.objectID != right.objectID ||
		left.partitionKey != right.partitionKey {
		return false
	}
	preferRight := false
	switch old := left.payload.(type) {
	case TeamEntityDocument:
		current, ok := right.payload.(TeamEntityDocument)
		if !ok || old.EntityKind != current.EntityKind || old.Name != current.Name {
			return false
		}
		preferRight = current.Confidence > old.Confidence ||
			(current.Confidence == old.Confidence && current.RootObjectID < old.RootObjectID)
	case TeamRelationDocument:
		current, ok := right.payload.(TeamRelationDocument)
		if !ok || old.FromObjectID != current.FromObjectID || old.ToObjectID != current.ToObjectID ||
			old.RelationKind != current.RelationKind {
			return false
		}
		preferRight = current.Confidence > old.Confidence ||
			(current.Confidence == old.Confidence && current.RootObjectID < old.RootObjectID)
	case TeamFactDocument:
		current, ok := right.payload.(TeamFactDocument)
		if !ok || old.NodeObjectID != current.NodeObjectID || old.Text != current.Text ||
			old.Predicate != current.Predicate || old.ObjectText != current.ObjectText ||
			old.Domain != current.Domain {
			return false
		}
		preferRight = current.Confidence > old.Confidence ||
			(current.Confidence == old.Confidence && current.RootObjectID < old.RootObjectID)
	case TeamEventDocument:
		current, ok := right.payload.(TeamEventDocument)
		if !ok || old.Title != current.Title || old.OccurredAt != current.OccurredAt ||
			old.Domain != current.Domain {
			return false
		}
		preferRight = current.Confidence > old.Confidence ||
			(current.Confidence == old.Confidence && current.RootObjectID < old.RootObjectID)
	case TeamAssertionDocument:
		current, ok := right.payload.(TeamAssertionDocument)
		if !ok || old.SubjectObjectID != current.SubjectObjectID ||
			old.ClaimSlotDigest != current.ClaimSlotDigest || old.Predicate != current.Predicate {
			return false
		}
		preferRight = current.ObservedAtUnixMilli > old.ObservedAtUnixMilli ||
			(current.ObservedAtUnixMilli == old.ObservedAtUnixMilli &&
				(current.Confidence > old.Confidence ||
					(current.Confidence == old.Confidence && current.RootObjectID < old.RootObjectID)))
	default:
		return false
	}
	left.contributionRoots[right.rootObjectID] = true
	if preferRight {
		left.rootObjectID = right.rootObjectID
		left.text = right.text
		left.payload = right.payload
		left.influenceRoots = map[string]bool{right.rootObjectID: true}
	}
	return true
}

func pruneIneligibleTeamContextDocuments(
	documents []*scoredTeamContextDocument,
	byObject map[string]*scoredTeamContextDocument,
) []*scoredTeamContextDocument {
	eligible := make([]*scoredTeamContextDocument, 0, len(documents))
	for _, document := range documents {
		valid := true
		switch value := document.payload.(type) {
		case TeamRelationDocument:
			from, to := byObject[value.FromObjectID], byObject[value.ToObjectID]
			valid = from != nil && to != nil && from.kind == "entity" && to.kind == "entity" &&
				from.partitionKey == document.partitionKey && to.partitionKey == document.partitionKey
		case TeamAssertionDocument:
			valid = eligibleTeamAssertionContribution(value, byObject)
		}
		if valid {
			eligible = append(eligible, document)
			continue
		}
		delete(byObject, document.objectID)
	}
	return eligible
}

func eligibleTeamAssertionContribution(
	assertion TeamAssertionDocument,
	byObject map[string]*scoredTeamContextDocument,
) bool {
	candidate := byObject[assertion.CandidateObjectID]
	subject := byObject[assertion.SubjectObjectID]
	if candidate == nil || subject == nil || subject.kind != "entity" ||
		candidate.partitionKey != assertion.PartitionKey ||
		subject.partitionKey != assertion.PartitionKey ||
		!candidate.contributionRoots[assertion.RootObjectID] ||
		!subject.contributionRoots[assertion.RootObjectID] {
		return false
	}
	fact, isFact := candidate.payload.(TeamFactDocument)
	return isFact && fact.NodeObjectID == assertion.SubjectObjectID
}

func selectTeamContextDocuments(
	ranked []*scoredTeamContextDocument,
	byObject map[string]*scoredTeamContextDocument,
	topK int,
) []*scoredTeamContextDocument {
	selected := make(map[string]*scoredTeamContextDocument, topK)
	for _, document := range ranked {
		dependencies := make([]*scoredTeamContextDocument, 0, 2)
		switch value := document.payload.(type) {
		case TeamRelationDocument:
			from, to := byObject[value.FromObjectID], byObject[value.ToObjectID]
			if from == nil || to == nil || from.kind != "entity" || to.kind != "entity" ||
				from.partitionKey != document.partitionKey || to.partitionKey != document.partitionKey {
				continue
			}
			dependencies = append(dependencies, from, to)
		case TeamAssertionDocument:
			subject := byObject[value.SubjectObjectID]
			if subject == nil || subject.kind != "entity" || subject.partitionKey != document.partitionKey {
				continue
			}
			dependencies = append(dependencies, subject)
		}
		newObjects := 0
		if selected[document.objectID] == nil {
			newObjects++
		}
		seenDependencies := make(map[string]bool, len(dependencies))
		for _, dependency := range dependencies {
			if selected[dependency.objectID] == nil && !seenDependencies[dependency.objectID] {
				seenDependencies[dependency.objectID] = true
				newObjects++
			}
		}
		if len(selected)+newObjects > topK {
			continue
		}
		selected[document.objectID] = document
		for _, dependency := range dependencies {
			selected[dependency.objectID] = dependency
		}
		if len(selected) == topK {
			break
		}
	}
	result := make([]*scoredTeamContextDocument, 0, len(selected))
	for _, document := range selected {
		result = append(result, document)
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].score != result[right].score {
			return result[left].score > result[right].score
		}
		if result[left].rootObjectID != result[right].rootObjectID {
			return result[left].rootObjectID < result[right].rootObjectID
		}
		return result[left].objectID < result[right].objectID
	})
	return result
}

func applyTeamGraphScores(
	documents []*scoredTeamContextDocument,
	byObject map[string]*scoredTeamContextDocument,
	links []TeamGraphLink,
	mode TeamGraphMode,
) error {
	hopLimit := 1
	if mode == TeamGraphModeWalk {
		hopLimit = teamGraphHopLimit
	}
	type neighbor struct {
		objectID     string
		rootObjectID string
		strength     float64
	}
	type frontierNode struct {
		objectID string
		roots    map[string]bool
	}
	adjacency := make(map[string][]neighbor)
	for _, link := range links {
		if link.RootObjectID == "" || link.PartitionKey == "" ||
			link.FromObjectID == "" || link.ToObjectID == "" ||
			!validTeamUnitFloat(link.Strength) {
			return ErrInvalidTeamAuthorizedCorpus
		}
		from, fromOK := byObject[link.FromObjectID]
		to, toOK := byObject[link.ToObjectID]
		if !fromOK || !toOK || from.partitionKey != link.PartitionKey ||
			to.partitionKey != link.PartitionKey ||
			(!from.contributionRoots[link.RootObjectID] && !to.contributionRoots[link.RootObjectID]) ||
			link.Strength < teamGraphMinStrength {
			continue
		}
		adjacency[from.objectID] = append(adjacency[from.objectID], neighbor{
			objectID: to.objectID, rootObjectID: link.RootObjectID, strength: link.Strength,
		})
		adjacency[to.objectID] = append(adjacency[to.objectID], neighbor{
			objectID: from.objectID, rootObjectID: link.RootObjectID, strength: link.Strength,
		})
	}
	for objectID := range adjacency {
		sort.Slice(adjacency[objectID], func(left, right int) bool {
			leftNeighbor, rightNeighbor := adjacency[objectID][left], adjacency[objectID][right]
			if leftNeighbor.objectID != rightNeighbor.objectID {
				return leftNeighbor.objectID < rightNeighbor.objectID
			}
			if leftNeighbor.strength != rightNeighbor.strength {
				return leftNeighbor.strength > rightNeighbor.strength
			}
			return leftNeighbor.rootObjectID < rightNeighbor.rootObjectID
		})
	}
	seeds := make([]*scoredTeamContextDocument, 0, len(documents))
	for _, document := range documents {
		if document.score > 0 {
			seeds = append(seeds, document)
		}
	}
	sort.Slice(seeds, func(left, right int) bool {
		if seeds[left].score != seeds[right].score {
			return seeds[left].score > seeds[right].score
		}
		return seeds[left].objectID < seeds[right].objectID
	})
	if len(seeds) > teamGraphSeedLimit {
		seeds = seeds[:teamGraphSeedLimit]
	}
	for _, seed := range seeds {
		seen := map[string]bool{seed.objectID: true}
		frontier := []frontierNode{{
			objectID: seed.objectID,
			roots:    map[string]bool{seed.rootObjectID: true},
		}}
		for hop := 1; hop <= hopLimit && len(frontier) != 0; hop++ {
			next := make([]frontierNode, 0)
			for _, current := range frontier {
				for _, adjacent := range adjacency[current.objectID] {
					if seen[adjacent.objectID] {
						continue
					}
					seen[adjacent.objectID] = true
					pathRoots := make(map[string]bool, len(current.roots)+2)
					for rootObjectID := range current.roots {
						pathRoots[rootObjectID] = true
					}
					pathRoots[adjacent.rootObjectID] = true
					target := byObject[adjacent.objectID]
					pathRoots[target.rootObjectID] = true
					next = append(next, frontierNode{objectID: adjacent.objectID, roots: pathRoots})
					boost := adjacent.strength * (0.25 / float64(hop))
					if boost > target.graph {
						target.graph = boost
						target.score = target.lexical + target.cosine + target.graph
						target.influenceRoots = pathRoots
					}
				}
			}
			frontier = next
		}
	}
	return nil
}

func validTeamGraphMode(mode TeamGraphMode) bool {
	switch mode {
	case TeamGraphModeOff, TeamGraphModeAnchored, TeamGraphModeWalk:
		return true
	default:
		return false
	}
}

func applyTeamAssertionReduction(
	byObject map[string]*scoredTeamContextDocument,
	assertions []TeamAssertionDocument,
) {
	groups := make(map[string][]TeamAssertionDocument)
	for _, assertion := range assertions {
		assertionDocument := byObject[assertion.ObjectID]
		if assertionDocument == nil ||
			!assertionDocument.contributionRoots[assertion.RootObjectID] ||
			!eligibleTeamAssertionContribution(assertion, byObject) {
			continue
		}
		key := assertion.PartitionKey + "\x00" + assertion.ClaimSlotDigest
		groups[key] = append(groups[key], assertion)
	}
	for _, group := range groups {
		sort.Slice(group, func(left, right int) bool {
			if group[left].ObservedAtUnixMilli != group[right].ObservedAtUnixMilli {
				return group[left].ObservedAtUnixMilli > group[right].ObservedAtUnixMilli
			}
			if group[left].Confidence != group[right].Confidence {
				return group[left].Confidence > group[right].Confidence
			}
			return group[left].ObjectID < group[right].ObjectID
		})
		groupRoots := make(map[string]bool, len(group))
		for _, assertion := range group {
			groupRoots[assertion.RootObjectID] = true
		}
		winner := group[0].CandidateObjectID
		for _, assertion := range group {
			candidate := byObject[assertion.CandidateObjectID]
			for rootObjectID := range groupRoots {
				candidate.influenceRoots[rootObjectID] = true
			}
			if assertion.CandidateObjectID == winner {
				if candidate.assertionState == "" {
					candidate.assertionState = "current"
				}
				continue
			}
			candidate.assertionState = "superseded"
			candidate.score *= teamAssertionSupersededMultiplier
		}
	}
}

func appendTeamContextResult(response *TeamContextResponse, document *scoredTeamContextDocument) {
	switch value := document.payload.(type) {
	case TeamEntityDocument:
		response.Entities = append(response.Entities, TeamContextEntity{
			RootObjectID: value.RootObjectID, ObjectID: value.ObjectID,
			EntityKind: value.EntityKind, Name: value.Name, Summary: value.Summary,
			Confidence: value.Confidence, Score: clampTeamScore(document.score),
		})
	case TeamRelationDocument:
		response.Relations = append(response.Relations, TeamContextRelation{
			RootObjectID: value.RootObjectID, ObjectID: value.ObjectID,
			FromObjectID: value.FromObjectID, ToObjectID: value.ToObjectID,
			RelationKind: value.RelationKind, Summary: value.Summary,
			Strength: value.Strength, Confidence: value.Confidence,
			Score: clampTeamScore(document.score),
		})
	case TeamFactDocument:
		response.Facts = append(response.Facts, TeamContextFact{
			RootObjectID: value.RootObjectID, ObjectID: value.ObjectID,
			NodeObjectID: value.NodeObjectID, Text: value.Text,
			Predicate: value.Predicate, ObjectText: value.ObjectText,
			Confidence: value.Confidence, Domain: value.Domain,
			Score: clampTeamScore(document.score),
		})
	case TeamEventDocument:
		response.Events = append(response.Events, TeamContextEvent{
			RootObjectID: value.RootObjectID, ObjectID: value.ObjectID,
			Title: value.Title, Summary: value.Summary, OccurredAt: value.OccurredAt,
			Confidence: value.Confidence, Domain: value.Domain,
			Score: clampTeamScore(document.score),
		})
	case TeamAssertionDocument:
		response.Assertions = append(response.Assertions, TeamContextAssertion{
			RootObjectID: value.RootObjectID, ObjectID: value.ObjectID,
			CandidateObjectID: value.CandidateObjectID, SubjectObjectID: value.SubjectObjectID,
			ClaimSlotDigest: value.ClaimSlotDigest, Text: value.Text,
			Predicate: value.Predicate, ObjectText: value.ObjectText,
			Confidence: value.Confidence, Score: clampTeamScore(document.score),
		})
	}
}

func clampTeamScore(score float64) float64 {
	if math.IsNaN(score) || math.IsInf(score, 0) || score <= 0 {
		return 0
	}
	if score >= 1 {
		return 1
	}
	return score
}

func emptyTeamContextResponse(includeTrace bool) TeamContextResponse {
	response := TeamContextResponse{
		Entities: make([]TeamContextEntity, 0), Relations: make([]TeamContextRelation, 0),
		Facts: make([]TeamContextFact, 0), Events: make([]TeamContextEvent, 0),
		Assertions: make([]TeamContextAssertion, 0),
	}
	if includeTrace {
		response.Trace = make([]TeamContextTrace, 0)
	}
	return response
}

func teamUniqueSortedContextRoots(documents []*scoredTeamContextDocument) []string {
	seen := make(map[string]bool, len(documents))
	roots := make([]string, 0, len(documents))
	for _, document := range documents {
		for rootObjectID := range document.influenceRoots {
			if !seen[rootObjectID] {
				seen[rootObjectID] = true
				roots = append(roots, rootObjectID)
			}
		}
	}
	sort.Strings(roots)
	return roots
}
