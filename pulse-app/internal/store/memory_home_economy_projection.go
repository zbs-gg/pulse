package store

import (
	"sort"
	"time"
)

type MemoryHomeEconomyCoverage struct {
	ComparablePairs int `json:"comparable_pairs"`
	ExcludedPairs   int `json:"excluded_pairs"`
	MeasuredPairs   int `json:"measured_pairs"`
	CountedObjects  int `json:"counted_objects"`
	TotalObjects    int `json:"total_objects"`
}

type MemoryHomeEconomy struct {
	State                     string                      `json:"state"`
	LatestOffer               *MemoryHomeLocalOffer       `json:"latest_offer,omitempty"`
	Aggregate                 *MemoryHomeEconomyAggregate `json:"aggregate,omitempty"`
	Coverage                  MemoryHomeEconomyCoverage   `json:"coverage"`
	EstimatedAvoidedTokens    *int                        `json:"estimated_avoided_tokens,omitempty"`
	EstimatedReductionPercent *float64                    `json:"estimated_reduction_percent,omitempty"`
	Trend                     string                      `json:"trend,omitempty"`
	MeasuredAvoidedTokens     *int                        `json:"measured_avoided_tokens,omitempty"`
	MeasuredSource            string                      `json:"measured_source,omitempty"`
}

type MemoryHomeEconomyAggregate struct {
	MethodID               string `json:"method_id"`
	MethodVersion          string `json:"method_version"`
	BaselineKind           string `json:"baseline_kind"`
	WindowStart            string `json:"window_start"`
	WindowEnd              string `json:"window_end"`
	ComparablePairs        int    `json:"comparable_pairs"`
	SourceEquivalentTokens int    `json:"source_equivalent_tokens"`
	PulseTokens            int    `json:"pulse_tokens"`
	CountedObjects         int    `json:"counted_objects"`
	TotalObjects           int    `json:"total_objects"`
}

type MemoryHomeLocalOffer struct {
	RenderedBytes int    `json:"rendered_bytes"`
	PulseTokens   int    `json:"pulse_tokens"`
	MethodID      string `json:"method_id"`
	MethodVersion string `json:"method_version"`
}

type memoryHomeTimedDeliveryFact struct {
	fact      MemoryHomeDeliveryFact
	createdAt time.Time
}

func ProjectMemoryHomeEconomy(facts []MemoryHomeDeliveryFact) MemoryHomeEconomy {
	result := MemoryHomeEconomy{State: MemoryHomeEconomyCollectingBaseline}
	offers := make([]memoryHomeTimedDeliveryFact, 0, len(facts))
	observations := make(map[string][]memoryHomeTimedDeliveryFact)
	var latest *memoryHomeTimedDeliveryFact
	for _, fact := range facts {
		createdAt, ok := canonicalMemoryHomeTime(fact.CreatedAt)
		if !ok {
			continue
		}
		timed := memoryHomeTimedDeliveryFact{fact: fact, createdAt: createdAt}
		if fact.Acknowledgement == MemoryHomeDeliveryOfferedToHost {
			offers = append(offers, timed)
		} else if fact.Acknowledgement == MemoryHomeDeliveryHostObserved {
			observations[fact.ContextID] = append(observations[fact.ContextID], timed)
		}
		if fact.Acknowledgement != MemoryHomeDeliveryOfferedToHost || fact.Purpose != MemoryHomeDeliveryPurposeSessionStart ||
			fact.RenderedBytes <= 0 || fact.PulseTokens <= 0 || fact.MethodID == "" || fact.MethodVersion == "" {
			continue
		}
		if latest == nil || timed.createdAt.After(latest.createdAt) {
			copy := timed
			latest = &copy
		}
	}
	if latest != nil {
		result.LatestOffer = &MemoryHomeLocalOffer{
			RenderedBytes: latest.fact.RenderedBytes, PulseTokens: latest.fact.PulseTokens,
			MethodID: latest.fact.MethodID, MethodVersion: latest.fact.MethodVersion,
		}
	}
	var estimatedAvoided int
	var sourceEquivalentTotal int
	var pulseTotal int
	var firstReduction, lastReduction float64
	var measuredAvoided int
	var cohortWindowStart, cohortWindowEnd string
	sort.SliceStable(offers, func(left, right int) bool {
		return offers[left].createdAt.Before(offers[right].createdAt)
	})
	for _, timedOffer := range offers {
		offer := timedOffer.fact
		observed, observedOK := matchingMemoryHomeTimedObservation(offer, timedOffer.createdAt, observations[offer.ContextID])
		if offer.Acknowledgement != MemoryHomeDeliveryOfferedToHost ||
			offer.Purpose != MemoryHomeDeliveryPurposeSessionStart ||
			offer.SourceEquivalentTokens < offer.PulseTokens || offer.PulseTokens <= 0 ||
			offer.BaselineKind == "" || offer.CoverageCounted <= 0 ||
			offer.CoverageTotal < offer.CoverageCounted || !observedOK {
			continue
		}
		if offer.MethodID != MemoryHomeCountMethodUTF8BytesDiv4Ceil || offer.MethodVersion != "1" ||
			offer.BaselineKind != MemoryHomeBaselineCanonicalStructured {
			result.Coverage.ExcludedPairs++
			continue
		}
		result.Coverage.ComparablePairs++
		if result.Coverage.ComparablePairs == 1 {
			cohortWindowStart = offer.CreatedAt
		}
		cohortWindowEnd = offer.CreatedAt
		result.Coverage.CountedObjects += offer.CoverageCounted
		result.Coverage.TotalObjects += offer.CoverageTotal
		estimatedAvoided += offer.SourceEquivalentTokens - offer.PulseTokens
		sourceEquivalentTotal += offer.SourceEquivalentTokens
		pulseTotal += offer.PulseTokens
		reduction := float64(offer.SourceEquivalentTokens-offer.PulseTokens) / float64(offer.SourceEquivalentTokens)
		if result.Coverage.ComparablePairs == 1 {
			firstReduction = reduction
		}
		lastReduction = reduction
		if observed.ProviderEvidenceVerified && validMemoryHomeDigest(observed.ProviderEvidenceDigest) &&
			observed.ProviderActualSource != "" && observed.ProviderActualInputTokens > 0 &&
			offer.SourceEquivalentTokens >= observed.ProviderActualInputTokens {
			if result.MeasuredSource == "" || result.MeasuredSource == observed.ProviderActualSource {
				result.MeasuredSource = observed.ProviderActualSource
				result.Coverage.MeasuredPairs++
				measuredAvoided += offer.SourceEquivalentTokens - observed.ProviderActualInputTokens
			}
		}
	}
	if result.Coverage.ComparablePairs > 0 {
		result.State = MemoryHomeEconomyEstimated
		result.EstimatedAvoidedTokens = &estimatedAvoided
		result.LatestOffer = nil
		result.Aggregate = &MemoryHomeEconomyAggregate{
			MethodID: MemoryHomeCountMethodUTF8BytesDiv4Ceil, MethodVersion: "1",
			BaselineKind: MemoryHomeBaselineCanonicalStructured,
			WindowStart:  cohortWindowStart, WindowEnd: cohortWindowEnd,
			ComparablePairs:        result.Coverage.ComparablePairs,
			SourceEquivalentTokens: sourceEquivalentTotal, PulseTokens: pulseTotal,
			CountedObjects: result.Coverage.CountedObjects, TotalObjects: result.Coverage.TotalObjects,
		}
	}
	if result.Coverage.ComparablePairs >= 3 {
		percent := float64(sourceEquivalentTotal-pulseTotal) * 100 / float64(sourceEquivalentTotal)
		result.EstimatedReductionPercent = &percent
		switch {
		case lastReduction > firstReduction+0.005:
			result.Trend = MemoryHomeEconomyTrendUp
		case lastReduction < firstReduction-0.005:
			result.Trend = MemoryHomeEconomyTrendDown
		default:
			result.Trend = MemoryHomeEconomyTrendFlat
		}
	}
	if result.Coverage.MeasuredPairs > 0 {
		result.State = MemoryHomeEconomyMeasured
		result.MeasuredAvoidedTokens = &measuredAvoided
	} else if result.Coverage.ComparablePairs == 0 && result.Coverage.ExcludedPairs > 0 {
		result.State = MemoryHomeEconomyUnavailable
	}
	return result
}

func matchingMemoryHomeTimedObservation(
	offer MemoryHomeDeliveryFact, offerTime time.Time, observations []memoryHomeTimedDeliveryFact,
) (MemoryHomeDeliveryFact, bool) {
	for _, observed := range observations {
		if observed.createdAt.After(offerTime) && memoryHomeObservationMatches(offer, observed.fact) {
			return observed.fact, true
		}
	}
	return MemoryHomeDeliveryFact{}, false
}
