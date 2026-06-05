package store

// tryCache attempts to resolve user_id purely from in-memory cache.
// Returns (user_id, true) on full cache hit, (0, false) if DB access needed.
func (ir *IdentityResolver) tryCache(accountID, distinctID string) (int64, bool) {
	if accountID != "" && distinctID != "" {
		aUID, aOk := ir.accountCache.Load(accountID)
		_, dOk := ir.distinctCache.Load(distinctID)
		if aOk && dOk {
			// Both known, return account_id's user_id (priority rule).
			return aUID.(int64), true
		}
		// One or both unknown -> need DB for potential binding.
		return 0, false
	}

	if accountID != "" {
		if uid, ok := ir.accountCache.Load(accountID); ok {
			return uid.(int64), true
		}
		return 0, false
	}

	if distinctID != "" {
		if uid, ok := ir.distinctCache.Load(distinctID); ok {
			return uid.(int64), true
		}
		return 0, false
	}

	return 0, false
}

// cacheMapping populates the in-memory caches from a loaded IDMapping.
func (ir *IdentityResolver) cacheMapping(m *IDMapping) {
	if m == nil {
		return
	}
	if m.AccountID != "" {
		ir.accountCache.Store(m.AccountID, m.UserID)
	}
	for _, did := range m.DistinctIDs {
		ir.distinctCache.Store(did, m.UserID)
	}
}
