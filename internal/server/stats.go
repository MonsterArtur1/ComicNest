package server

import (
	"net/http"
	"sort"
	"time"

	"comicnest/internal/opds"
)

// readingStats is the "your stats" strip on the library page.
type readingStats struct {
	ReadThisMonth int
	CurrentStreak int // consecutive days, up to today, with at least one finish
	LongestStreak int // longest such run ever
}

// Any lets the template hide the whole strip when there is nothing to show
// yet (a fresh install, or a user who has never finished anything).
func (s readingStats) Any() bool { return s.ReadThisMonth > 0 || s.CurrentStreak > 0 || s.LongestStreak > 0 }

// userReadingStats loads the requesting user's finish timestamps and
// reduces them to month/streak counts for the library page.
func (s *Server) userReadingStats(r *http.Request) (readingStats, error) {
	raw, err := s.store.FinishedAtTimes(userFrom(r))
	if err != nil {
		return readingStats{}, err
	}
	return computeReadingStats(raw, time.Now()), nil
}

// computeReadingStats buckets raw UTC timestamps into calendar days in now's
// location (matching how "read at" times are displayed elsewhere, e.g.
// newReadingView's t.Local()) rather than the UTC day the database stores,
// so a late-night read doesn't land on the wrong side of midnight. now
// carries the zone to bucket by; production calls pass time.Now().
func computeReadingStats(rawTimes []string, now time.Time) readingStats {
	var st readingStats
	days := make(map[string]bool)
	y, m, _ := now.Date()
	for _, raw := range rawTimes {
		t := opds.ParseDBTime(raw, time.Time{})
		if t.IsZero() {
			continue
		}
		local := t.In(now.Location())
		if ly, lm, _ := local.Date(); ly == y && lm == m {
			st.ReadThisMonth++
		}
		days[local.Format("2006-01-02")] = true
	}
	st.CurrentStreak, st.LongestStreak = dayStreaks(days, now)
	return st
}

// dayStreaks returns the current streak (consecutive days ending today or
// yesterday — a day not yet visited today doesn't break it, it just hasn't
// extended it yet) and the longest run of consecutive days in the set.
func dayStreaks(days map[string]bool, today time.Time) (current, longest int) {
	if len(days) == 0 {
		return 0, 0
	}
	const dayFormat = "2006-01-02"

	cursor := today
	if !days[cursor.Format(dayFormat)] {
		cursor = cursor.AddDate(0, 0, -1)
	}
	for days[cursor.Format(dayFormat)] {
		current++
		cursor = cursor.AddDate(0, 0, -1)
	}

	sorted := make([]string, 0, len(days))
	for d := range days {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)

	run := 0
	var prev time.Time
	for i, d := range sorted {
		t, err := time.ParseInLocation(dayFormat, d, today.Location())
		if err != nil {
			continue
		}
		if i > 0 && prev.AddDate(0, 0, 1).Equal(t) {
			run++
		} else {
			run = 1
		}
		if run > longest {
			longest = run
		}
		prev = t
	}
	return current, longest
}
