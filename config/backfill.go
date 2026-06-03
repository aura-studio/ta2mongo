package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ForceSkip returns the effective value of ForceSkipExisting, defaulting to
// true when the pointer is nil (i.e. user omitted the field).
func (b *BackfillConfig) ForceSkip() bool {
	if b.ForceSkipExisting == nil {
		return true
	}
	return *b.ForceSkipExisting
}

// ShouldPaginate reports the effective pagination mode, defaulting to true
// when the user omits the field.
func (b *BackfillConfig) ShouldPaginate() bool {
	return b.Paginate == nil || *b.Paginate
}

// EffectivePageSize returns the pageSize to pass to submit-sql: PageSize in
// paginated mode, or 0 (no server-side pagination) when Paginate is false.
func (b *BackfillConfig) EffectivePageSize() int {
	if b.ShouldPaginate() {
		return b.PageSize
	}
	return 0
}

func (b *BackfillConfig) validate(table string) error {
	if b.APIBaseURL == "" {
		return fmt.Errorf("backfill.apiBaseURL is required")
	}
	if !strings.HasPrefix(b.APIBaseURL, "http://") && !strings.HasPrefix(b.APIBaseURL, "https://") {
		return fmt.Errorf("backfill.apiBaseURL must start with http(s)://; got %q", b.APIBaseURL)
	}
	if b.Token == "" {
		return fmt.Errorf("backfill.token is required")
	}
	if b.ProjectID <= 0 {
		return fmt.Errorf("backfill.projectID must be a positive integer")
	}
	if b.RunID == "" {
		return fmt.Errorf("backfill.runID is required (used as resume key)")
	}
	if b.PageSize < 1000 {
		return fmt.Errorf("backfill.pageSize must be >= 1000 (TA OpenAPI minimum)")
	}
	// User tables in TA do not have a $part_date partition column, so the
	// date range is required only for the event table.
	if table == BackfillTableEvent {
		if _, err := time.Parse("2006-01-02", b.PartDateRange.Start); err != nil {
			return fmt.Errorf("backfill.partDateRange.start invalid (want YYYY-MM-DD): %w", err)
		}
		if _, err := time.Parse("2006-01-02", b.PartDateRange.End); err != nil {
			return fmt.Errorf("backfill.partDateRange.end invalid (want YYYY-MM-DD): %w", err)
		}
	}
	if b.Proxy != "" {
		u, err := url.Parse(b.Proxy)
		if err != nil {
			return fmt.Errorf("backfill.proxy invalid: %w", err)
		}
		switch u.Scheme {
		case "http", "https", "socks5":
		default:
			return fmt.Errorf("backfill.proxy scheme %q not supported (http/https/socks5 only)", u.Scheme)
		}
	}
	if !b.EventTimeRange.Empty() {
		if b.EventTimeRange.Start != "" {
			if _, err := time.Parse("2006-01-02 15:04:05", b.EventTimeRange.Start); err != nil {
				return fmt.Errorf("backfill.eventTimeRange.start invalid (want YYYY-MM-DD HH:MM:SS): %w", err)
			}
		}
		if b.EventTimeRange.End != "" {
			if _, err := time.Parse("2006-01-02 15:04:05", b.EventTimeRange.End); err != nil {
				return fmt.Errorf("backfill.eventTimeRange.end invalid (want YYYY-MM-DD HH:MM:SS): %w", err)
			}
		}
	}
	return nil
}
