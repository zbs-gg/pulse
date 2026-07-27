package store

const memoryHomeDeliveryFactsQuery = `SELECT ` + continuityDeliveryReceiptColumns + `
	FROM continuity_delivery_receipts AS receipt INDEXED BY idx_continuity_delivery_memory_home
	WHERE repository_id=? AND binding_digest=? AND context_id IN (
		SELECT context_id
		  FROM continuity_delivery_receipts AS recent
		       INDEXED BY idx_continuity_delivery_memory_home_recent
		 WHERE repository_id=? AND binding_digest=?
		   AND purpose='session_start' AND receipt_state='offered_to_host'
		 ORDER BY receipt_seq DESC
		 LIMIT ?
	)
	ORDER BY receipt_seq`

// ReadMemoryHomeDeliveryFacts projects a bounded set of immutable delivery
// contexts for the exact configured vault boundary. Provider measurements stay
// separate in the ledger and are attached only to their verified observation.
func (s *Store) ReadMemoryHomeDeliveryFacts(repositoryID, bindingDigest string, limit int) ([]MemoryHomeDeliveryFact, error) {
	if s == nil || limit < 1 || limit > 100 {
		return nil, ErrContinuityDeliveryInvalid
	}
	if err := s.validateContinuityDeliveryAuthority(bindingDigest, repositoryID); err != nil {
		return nil, err
	}
	return s.readMemoryHomeDeliveryFacts(repositoryID, bindingDigest, limit)
}

func (s *Store) ReadMemoryHomeDeliveryFactsForVerifiedBinding(
	repositoryID, bindingDigest string,
	limit int,
) ([]MemoryHomeDeliveryFact, error) {
	if s == nil || limit < 1 || limit > 100 ||
		!validTrayIdentifier(repositoryID) || !trayBindingDigestPattern.MatchString(bindingDigest) {
		return nil, ErrContinuityDeliveryInvalid
	}
	return s.readMemoryHomeDeliveryFacts(repositoryID, bindingDigest, limit)
}

func (s *Store) readMemoryHomeDeliveryFacts(
	repositoryID, bindingDigest string,
	limit int,
) ([]MemoryHomeDeliveryFact, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(
		memoryHomeDeliveryFactsQuery,
		repositoryID, bindingDigest, repositoryID, bindingDigest, limit,
	)
	if err != nil {
		return nil, err
	}
	receipts := make([]ContinuityDeliveryReceipt, 0, limit*3)
	for rows.Next() {
		receipt, scanErr := scanContinuityDeliveryReceipt(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		receipts = append(receipts, receipt)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]MemoryHomeDeliveryFact, 0, len(receipts))
	observedIndex := make(map[string]int)
	if err := hydrateContinuityDeliveryRefsBatchTx(tx, receipts); err != nil {
		return nil, err
	}
	for index := range receipts {
		receipt := &receipts[index]
		switch receipt.State {
		case ContinuityDeliveryOfferedToHost, ContinuityDeliveryHostObserved:
			sourceEquivalent := 0
			if receipt.SourceEquivalentTokens != nil {
				sourceEquivalent = *receipt.SourceEquivalentTokens
			}
			result = append(result, MemoryHomeDeliveryFact{
				ReceiptID: receipt.ReceiptID, ContextID: receipt.ContextID,
				Acknowledgement: receipt.State, Purpose: receipt.Purpose,
				PayloadDigest: receipt.PayloadDigest, BindingDigest: receipt.BindingDigest,
				RepositoryID: receipt.RepositoryID, Host: receipt.Host, SessionRef: receipt.SessionRef,
				ObjectIDs: append([]string(nil), receipt.ObjectIDs...), EvidenceIDs: append([]string(nil), receipt.EvidenceIDs...),
				MethodID: receipt.MethodID, MethodVersion: receipt.MethodVersion,
				RenderedBytes: receipt.RenderedBytes, PulseTokens: receipt.PulseTokens,
				SourceEquivalentTokens: sourceEquivalent, BaselineKind: receipt.BaselineKind,
				CoverageCounted: receipt.CoverageCounted, CoverageTotal: receipt.CoverageTotal,
				CreatedAt: receipt.CreatedAt,
			})
			if receipt.State == ContinuityDeliveryHostObserved {
				observedIndex[receipt.ContextID] = len(result) - 1
			}
		case continuityDeliveryProviderMeasurement:
			factIndex, ok := observedIndex[receipt.ContextID]
			if !ok || receipt.ProviderActualInputTokens == nil || receipt.ProviderActualSource == "" ||
				!validMemoryHomeDigest(receipt.ProviderEvidenceDigest) {
				return nil, ErrContinuityDeliveryTransition
			}
			fact := &result[factIndex]
			fact.ProviderActualInputTokens = *receipt.ProviderActualInputTokens
			fact.ProviderActualSource = receipt.ProviderActualSource
			fact.ProviderEvidenceDigest = receipt.ProviderEvidenceDigest
			fact.ProviderEvidenceVerified = true
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
