package retrieve

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

type promptCapsuleCandidate struct {
	id          int64
	text        string
	cosine      float64
	lexicalRank int
	daysAgo     float64
}

const (
	promptCapsuleSemanticMinimum = 0.47
	promptCapsuleLexicalMinimum  = 0.45
)

func promptCapsuleAccepted(candidate promptCapsuleCandidate) bool {
	if candidate.lexicalRank > 0 {
		return candidate.cosine >= promptCapsuleLexicalMinimum
	}
	return candidate.cosine >= promptCapsuleSemanticMinimum
}

var promptStopwords = map[string]struct{}{
	"about": {}, "after": {}, "again": {}, "also": {}, "before": {}, "could": {},
	"does": {}, "from": {}, "have": {}, "her": {}, "his": {}, "many": {}, "our": {}, "that": {}, "their": {},
	"them": {}, "they": {}, "this": {}, "what": {}, "when": {}, "where": {},
	"which": {}, "with": {}, "would": {}, "your": {},
	"было": {}, "были": {}, "для": {}, "его": {}, "еще": {}, "ещё": {},
	"какая": {}, "какие": {}, "какой": {}, "когда": {}, "который": {},
	"много": {}, "мой": {}, "моя": {}, "наша": {}, "наши": {}, "после": {}, "потом": {}, "сколько": {}, "того": {}, "этого": {},
}

var promptEventAnchors = map[string]struct{}{
	"about": {}, "after": {}, "before": {}, "by": {}, "during": {}, "over": {},
	"во": {}, "время": {}, "из": {}, "насчет": {}, "насчёт": {}, "перед": {}, "поводу": {}, "после": {},
}

var promptEmotionQuestionTerms = map[string]struct{}{
	"afrai": {}, "anger": {}, "angry": {}, "anxio": {}, "asham": {}, "delig": {},
	"disap": {}, "embar": {}, "emoti": {}, "excit": {}, "feel": {}, "frigh": {}, "grief": {},
	"guilt": {}, "happy": {}, "joy": {}, "joyfu": {}, "nervo": {}, "pride": {}, "proud": {},
	"react": {}, "relie": {}, "resen": {}, "sad": {}, "safe": {}, "shame": {},
	"signi": {}, "stres": {}, "surpr": {}, "trust": {}, "upset": {}, "worri": {},
	"винов": {}, "гордо": {}, "груст": {}, "довер": {}, "злост": {}, "испуг": {},
	"облег": {}, "переж": {}, "почув": {}, "радос": {}, "разоч": {}, "реакц": {}, "страх": {},
	"стыд": {}, "трево": {}, "чувст": {}, "эмоци": {},
}

// These verbs say that something happened but not what happened. The object
// (marathon, flight, scholarship) is the part that must agree with memory.
var promptGenericEventActionTerms = map[string]struct{}{
	"adopt": {}, "arriv": {}, "buyin": {}, "cance": {}, "compl": {}, "gradu": {},
	"losin": {}, "missi": {}, "movin": {}, "openi": {}, "recei": {}, "selli": {}, "suppo": {}, "winni": {},
	"завер": {}, "откры": {}, "переез": {}, "побед": {}, "получ": {}, "покуп": {},
	"прода": {}, "проиг": {}, "пропу": {},
}

func promptQuestionAsksEmotion(query string) bool {
	for _, term := range promptRankTerms(query) {
		if _, ok := promptEmotionQuestionTerms[term]; ok {
			return true
		}
	}
	return false
}

func promptTokenIsEventAnchor(tokens []struct{ raw, term string }, index int) bool {
	lower := strings.ToLower(tokens[index].raw)
	if _, strong := promptEventAnchors[lower]; strong {
		return true
	}
	if lower == "to" && index > 0 {
		previous := tokens[index-1].term
		return previous == "react" || previous == "respo"
	}
	if lower == "for" && index+1 < len(tokens) {
		next := strings.ToLower(tokens[index+1].raw)
		return strings.HasSuffix(next, "ing")
	}
	return false
}

// promptPresupposedEventTerms returns the concrete part of an event that the
// question claims happened ("after completing a marathon", "about buying a
// sailboat"). A capsule that only shares the person's name must not be treated
// as evidence for that different event. Questions without an explicit event
// anchor keep the semantic path unchanged.
func promptPresupposedEventTerms(query string) []string {
	if !promptQuestionAsksEmotion(query) {
		return nil
	}
	var tokens []struct{ raw, term string }
	field := strings.Builder{}
	flush := func() {
		raw := strings.TrimSpace(field.String())
		field.Reset()
		if raw == "" {
			return
		}
		terms := promptRankTerms(raw)
		if len(terms) == 1 {
			tokens = append(tokens, struct{ raw, term string }{raw: raw, term: terms[0]})
			return
		}
		// Keep short anchor words such as "by" and "во", which the general
		// ranker intentionally drops.
		lower := strings.ToLower(raw)
		if _, anchor := promptEventAnchors[lower]; anchor || lower == "to" || lower == "for" {
			tokens = append(tokens, struct{ raw, term string }{raw: raw, term: lower})
		}
	}
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			field.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()

	anchor := -1
	for index := range tokens {
		if promptTokenIsEventAnchor(tokens, index) {
			anchor = index
		}
	}
	if anchor < 0 || anchor+1 >= len(tokens) {
		return nil
	}

	seen := map[string]struct{}{}
	terms := make([]string, 0, len(tokens)-anchor-1)
	for _, item := range tokens[anchor+1:] {
		if item.term == "to" || item.term == "for" {
			continue
		}
		// A capitalized token inside the clause is normally a person's name,
		// not proof that the claimed event itself happened.
		runes := []rune(item.raw)
		if len(runes) > 0 && unicode.IsUpper(runes[0]) {
			continue
		}
		if _, stopped := promptStopwords[item.term]; stopped {
			continue
		}
		if _, genericAction := promptGenericEventActionTerms[item.term]; genericAction {
			continue
		}
		if _, duplicate := seen[item.term]; duplicate {
			continue
		}
		seen[item.term] = struct{}{}
		terms = append(terms, item.term)
	}
	return terms
}

func promptCapsuleSupportsPresupposedEvent(eventTerms []string, text string) bool {
	if len(eventTerms) == 0 {
		return true
	}
	memoryTerms := map[string]struct{}{}
	for _, term := range promptRankTerms(text) {
		memoryTerms[term] = struct{}{}
	}
	for _, term := range eventTerms {
		if _, ok := memoryTerms[term]; ok {
			return true
		}
	}
	return false
}

func promptRankTerms(value string) []string {
	seen := map[string]struct{}{}
	var terms []string
	field := strings.Builder{}
	flush := func() {
		term := strings.ToLower(strings.TrimSpace(field.String()))
		field.Reset()
		if len([]rune(term)) < 3 {
			return
		}
		if _, stopped := promptStopwords[term]; stopped {
			return
		}
		runes := []rune(term)
		if len(runes) > 5 {
			term = string(runes[:5])
		}
		if _, duplicate := seen[term]; duplicate {
			return
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			field.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return terms
}

func promptLexicalCoverage(queryTerms []string, text string, documentFrequency map[string]int, documents int) float64 {
	if len(queryTerms) == 0 {
		return 0
	}
	memory := map[string]struct{}{}
	for _, term := range promptRankTerms(text) {
		memory[term] = struct{}{}
	}
	matchedWeight := 0.0
	totalWeight := 0.0
	for _, term := range queryTerms {
		weight := math.Log(1 + float64(documents+1)/float64(documentFrequency[term]+1))
		totalWeight += weight
		if _, ok := memory[term]; ok {
			matchedWeight += weight
		}
	}
	if totalWeight == 0 {
		return 0
	}
	return matchedWeight / totalWeight
}

var (
	promptDateValue         = regexp.MustCompile(`(?i)(?:\b(?:19|20)\d{2}(?:-\d{2}-\d{2})?\b|\b(?:yesterday|last|previous|weekend|week|month|year|ago)\b|(?:^|[^\pL])(?:вчера|недел\pL*|месяц\pL*|год\pL*)(?:[^\pL]|$))`)
	promptCountValue        = regexp.MustCompile(`(?i)(?:\b\d+\b|\b(?:one|two|three|four|five|six|seven|eight|nine|ten|once|twice|multiple)\b|(?:^|[^\pL])(?:один|два|три|четыр\pL*|пять|шесть|семь|восемь|девять|десять)(?:[^\pL]|$))`)
	promptRelationshipValue = regexp.MustCompile(`(?i)(?:\b(?:single|married|divorced|partner|relationship|boyfriend|girlfriend|wife|husband|spouse)\b|(?:^|[^\pL])(?:одинок\pL*|женат\pL*|замуж\pL*|развед\pL*|партн\pL*|отношен\pL*|муж|жена)(?:[^\pL]|$))`)
	promptQuotedTitle       = regexp.MustCompile(`["“”«»][^"“”«»]{2,100}["“”«»]`)
	promptTitleValue        = regexp.MustCompile(`(?i)(?:\b(?:book|song|movie|film|game|series|show|console|nickname|brand|company|read|recommend|suggest|called|named|favorite)\pL*\b|(?:^|[^\pL])(?:книг\pL*|песн\pL*|фильм\pL*|игр\pL*|сериал\pL*|шоу|приставк\pL*|прозвищ\pL*|бренд\pL*|компан\pL*|читал\pL*|рекоменд\pL*|назван\pL*|любим\pL*)(?:[^\pL]|$))`)
	promptNamedItemQuery    = regexp.MustCompile(`(?i)(?:\b(?:book|song|movie|film|game|series|show|console|nickname|brand|company|title)\pL*\b|(?:^|[^\pL])(?:книг\pL*|песн\pL*|фильм\pL*|игр\pL*|сериал\pL*|шоу|приставк\pL*|прозвищ\pL*|бренд\pL*|компан\pL*|назван\pL*)(?:[^\pL]|$))`)
	promptRecentQuery       = regexp.MustCompile(`(?i)(?:\b(?:recent|recently|latest|newest|most recent)\b|(?:^|[^\pL])(?:недавн\pL*|последн\pL*|сам\pL* свеж\pL*)(?:[^\pL]|$))`)
	promptLocationQuestion  = regexp.MustCompile(`(?i)(?:\b(?:where|what state|which state|what city|which city|what country|which country|what location|which location|what place|which place)\b|(?:^|[^\pL])(?:где|в каком штате|в какой стране|в каком городе|какое место|какое местоположение)(?:[^\pL]|$))`)
	promptLocationMemory    = regexp.MustCompile(`(?i)(?:\b(?:visit\pL*|travel\pL*|trip|journey|went|gone|moved|move\pL*|took|beach|destination|lives|living|based|born)\b|(?:^|[^\pL])(?:ездил\pL*|поездк\pL*|путешеств\pL*|переех\pL*|живёт|живет|родил\pL*)(?:[^\pL]|$))`)
	promptConclusionQuery   = regexp.MustCompile(`(?i)(?:\b(?:realize\pL*|learn\pL*|take away|conclude\pL*|lesson|why did|what made)\b|(?:^|[^\pL])(?:понял\pL*|осознал\pL*|узнал\pL*|вывод\pL*|урок\pL*|почему|что заставил\pL*)(?:[^\pL]|$))`)
	promptConclusionMemory  = regexp.MustCompile(`(?i)(?:\b(?:realize\pL*|learn\pL*|conclude\pL*|teach\pL*|taught|remind\pL*|because|therefore|showed)\b|(?:^|[^\pL])(?:понял\pL*|осознал\pL*|узнал\pL*|вывод\pL*|урок\pL*|научил\pL*|потому что|напомнил\pL*|показал\pL*)(?:[^\pL]|$))`)
	promptRecommendQuery    = regexp.MustCompile(`(?i)(?:\b(?:recommend\pL*|suggest\pL*)\b|(?:^|[^\pL])(?:рекоменд\pL*|посовет\pL*)(?:[^\pL]|$))`)
	promptRecommendMemory   = regexp.MustCompile(`(?i)(?:\b(?:recommend\pL*|suggest\pL*)\b|(?:^|[^\pL])(?:рекоменд\pL*|посовет\pL*)(?:[^\pL]|$))`)
)

func promptDenseQuery(query string) string {
	expanded := query
	if promptLocationQuestion.MatchString(query) {
		expanded += "\nRetrieve a past trip or visit with a named location."
	}
	if promptNamedItemQuery.MatchString(query) {
		expanded += "\nRetrieve the memory that explicitly names the requested book, movie, song, game, series, nickname, brand, company, or title."
	}
	return expanded
}

func promptIntentBonus(query, text string) float64 {
	rawQuery, rawText := query, text
	query = strings.ToLower(query)
	text = strings.ToLower(text)
	score := 0.0
	if (strings.Contains(query, "when") || strings.Contains(query, "how long ago") ||
		strings.Contains(query, "когда") || strings.Contains(query, "как давно")) && promptDateValue.MatchString(text) {
		score += 0.14
	}
	if (strings.Contains(query, "how many") || strings.Contains(query, "сколько")) && promptCountValue.MatchString(text) {
		score += 0.14
	}
	if (strings.Contains(query, "relationship status") || strings.Contains(query, "marital status") ||
		strings.Contains(query, "семейное положение") || strings.Contains(query, "статус отношений")) && promptRelationshipValue.MatchString(text) {
		score += 0.16
	}
	if promptNamedItemQuery.MatchString(query) && promptTitleValue.MatchString(text) {
		if promptQuotedTitle.MatchString(text) {
			score += 0.22
		} else {
			score += 0.06
		}
	}
	if promptLocationQuestion.MatchString(rawQuery) && promptLocationMemory.MatchString(rawText) {
		score += 0.20
	}
	if promptConclusionQuery.MatchString(rawQuery) && promptConclusionMemory.MatchString(rawText) {
		score += 0.30
	}
	if promptRecommendQuery.MatchString(rawQuery) && promptRecommendMemory.MatchString(rawText) {
		score += 0.18
	}
	return score
}

func rankPromptCapsules(query string, candidates []promptCapsuleCandidate, limit int) []int64 {
	if limit <= 0 || len(candidates) == 0 {
		return nil
	}
	queryTerms := promptRankTerms(query)
	documentFrequency := make(map[string]int, len(queryTerms))
	queryTermSet := make(map[string]struct{}, len(queryTerms))
	for _, term := range queryTerms {
		queryTermSet[term] = struct{}{}
	}
	for _, candidate := range candidates {
		seen := map[string]struct{}{}
		for _, term := range promptRankTerms(candidate.text) {
			if _, queried := queryTermSet[term]; !queried {
				continue
			}
			seen[term] = struct{}{}
		}
		for term := range seen {
			documentFrequency[term]++
		}
	}
	recentQuery := promptRecentQuery.MatchString(query)
	minimumDays, maximumDays := 0.0, 0.0
	if recentQuery {
		minimumDays, maximumDays = candidates[0].daysAgo, candidates[0].daysAgo
		for _, candidate := range candidates[1:] {
			minimumDays = math.Min(minimumDays, candidate.daysAgo)
			maximumDays = math.Max(maximumDays, candidate.daysAgo)
		}
	}
	type scored struct {
		candidate promptCapsuleCandidate
		score     float64
	}
	ranked := make([]scored, 0, len(candidates))
	for _, candidate := range candidates {
		score := candidate.cosine + 0.18*promptLexicalCoverage(queryTerms, candidate.text, documentFrequency, len(candidates)) +
			promptIntentBonus(query, candidate.text)
		if candidate.lexicalRank > 0 {
			score += 0.04 / float64(candidate.lexicalRank)
		}
		if recentQuery && maximumDays > minimumDays {
			score += 0.12 * (maximumDays - candidate.daysAgo) / (maximumDays - minimumDays)
		}
		ranked = append(ranked, scored{candidate: candidate, score: score})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].candidate.id < ranked[j].candidate.id
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	ids := make([]int64, len(ranked))
	for index, item := range ranked {
		ids[index] = item.candidate.id
	}
	return ids
}
