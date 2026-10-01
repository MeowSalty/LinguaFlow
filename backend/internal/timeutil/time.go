// Package timeutil defines the UTC representation of platform instants.
// Duration measurements should use time.Now directly to retain monotonic clocks.
package timeutil

import "time"

// SQLiteLayout is fixed width so SQLite text indexes order instants correctly.
const SQLiteLayout = "2006-01-02T15:04:05.000000000Z"

func NowUTC() time.Time { return time.Now().UTC() }

func Normalize(t time.Time) time.Time { return t.UTC() }

// NormalizePtr returns a copy and preserves absent timestamps.
func NormalizePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func Format(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// CeilMicrosecond maps an inclusive lower or exclusive upper bound onto the
// PostgreSQL timestamp grid without changing which persisted instants match.
func CeilMicrosecond(t time.Time) time.Time {
	t = t.UTC()
	if remainder := t.Nanosecond() % 1000; remainder != 0 {
		return t.Add(time.Duration(1000-remainder) * time.Nanosecond)
	}
	return t
}
