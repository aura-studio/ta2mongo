package store

import (
	"context"
	"fmt"
)

func (ir *IdentityResolver) resolveFromDB(ctx context.Context, accountID, distinctID string) (int64, error) {
	// Load current state from MongoDB.
	var accountMapping, distinctMapping *IDMapping
	var err error

	if accountID != "" {
		accountMapping, err = ir.findByAccountID(ctx, accountID)
		if err != nil {
			return 0, err
		}
	}

	if distinctID != "" {
		distinctMapping, err = ir.findByDistinctID(ctx, distinctID)
		if err != nil {
			return 0, err
		}
	}

	// Case 1: Only account_id
	if accountID != "" && distinctID == "" {
		if accountMapping != nil {
			ir.cacheMapping(accountMapping)
			return accountMapping.UserID, nil
		}
		return ir.atomicCreateForAccountID(ctx, accountID)
	}

	// Case 2: Only distinct_id
	if accountID == "" && distinctID != "" {
		if distinctMapping != nil {
			ir.cacheMapping(distinctMapping)
			return distinctMapping.UserID, nil
		}
		return ir.atomicCreateForDistinctID(ctx, distinctID)
	}

	// Case 3: Both present
	return ir.resolveWithBoth(ctx, accountID, distinctID, accountMapping, distinctMapping)
}

func (ir *IdentityResolver) resolveWithBoth(
	ctx context.Context,
	accountID, distinctID string,
	accountMapping, distinctMapping *IDMapping,
) (int64, error) {
	accountExists := accountMapping != nil
	distinctExists := distinctMapping != nil

	switch {
	case !accountExists && !distinctExists:
		// Both new -> bind together, create new user_id.
		return ir.atomicCreateForBoth(ctx, accountID, distinctID)

	case accountExists && !distinctExists:
		// account_id exists, distinct_id is new -> bind distinct_id to account's user.
		if err := ir.atomicBindDistinctToAccount(ctx, accountMapping.UserID, distinctID); err != nil {
			return 0, fmt.Errorf("bind distinct_id to account: %w", err)
		}
		ir.cacheMapping(accountMapping)
		ir.distinctCache.Store(distinctID, accountMapping.UserID)
		return accountMapping.UserID, nil

	case !accountExists && distinctExists:
		// account_id is new, distinct_id exists.
		if distinctMapping.AccountID == "" {
			// distinct_id not yet bound -> try atomic bind.
			bound, err := ir.atomicBindAccountToDistinct(ctx, distinctMapping.UserID, accountID)
			if err != nil {
				return 0, err
			}
			if bound {
				// Successful bind.
				ir.accountCache.Store(accountID, distinctMapping.UserID)
				return distinctMapping.UserID, nil
			}
			// Another pod bound it first -> account_id needs its own user.
			return ir.atomicCreateForAccountID(ctx, accountID)
		}
		// distinct_id already bound to another account -> no bind.
		return ir.atomicCreateForAccountID(ctx, accountID)

	default:
		// Both exist -> no binding change. Priority: account_id's user_id.
		ir.cacheMapping(accountMapping)
		ir.cacheMapping(distinctMapping)
		return accountMapping.UserID, nil
	}
}
