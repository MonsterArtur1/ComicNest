package server

import (
	"net/http"
	"sort"
	"time"

	"comicnest/internal/opds"
	"comicnest/internal/store"
)

// readingStats is what the user's finish timestamps reduce to: the "your
// stats" strip on the library page and the time-based half of /stats.
type readingStats struct {
	ReadThisWeek  int // since Monday
	ReadThisMonth int
	ReadThisYear  int
	CurrentStreak int // consecutive days, up to today, with at least one finish
	LongestStreak int // longest such run ever
	ReadingDays   int // distinct days with at least one finish

	Months   []statBar // the last 12 calendar months, oldest first
	Weekdays []statBar // Monday..Sunday, all time
}

// statBar is one bar of a /stats histogram; Pct is its height relative to
// the tallest bar in the same chart (0..100).
type statBar struct {
	Label string
	Count int
	Pct   int
}

// BusiestWeekday names the weekday with the most finishes ("" when none).
func (s readingStats) BusiestWeekday() string {
	best := statBar{}
	for _, b := range s.Weekdays {
		if b.Count > best.Count {
			best = b
		}
	}
	return best.Label
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
	y, m, d := now.Date()
	weekStart := time.Date(y, m, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, -mondayIndex(now.Weekday()))

	const monthFormat = "2006-01"
	months := make([]statBar, 12)
	monthIndex := make(map[string]int, len(months))
	for i := range months {
		start := time.Date(y, m-time.Month(len(months)-1-i), 1, 0, 0, 0, 0, now.Location())
		months[i].Label = start.Format("Jan")
		monthIndex[start.Format(monthFormat)] = i
	}
	weekdays := make([]statBar, 7)
	for i := range weekdays {
		weekdays[i].Label = time.Weekday((i + 1) % 7).String()[:3]
	}

	for _, raw := range rawTimes {
		t := opds.ParseDBTime(raw, time.Time{})
		if t.IsZero() {
			continue
		}
		local := t.In(now.Location())
		ly, lm, _ := local.Date()
		if ly == y {
			st.ReadThisYear++
			if lm == m {
				st.ReadThisMonth++
			}
		}
		if !local.Before(weekStart) {
			st.ReadThisWeek++
		}
		if i, ok := monthIndex[local.Format(monthFormat)]; ok {
			months[i].Count++
		}
		weekdays[mondayIndex(local.Weekday())].Count++
		days[local.Format("2006-01-02")] = true
	}
	st.ReadingDays = len(days)
	st.CurrentStreak, st.LongestStreak = dayStreaks(days, now)
	st.Months = scaleBars(months)
	st.Weekdays = scaleBars(weekdays)
	return st
}

// mondayIndex maps a weekday to its position in a Monday-first week.
func mondayIndex(wd time.Weekday) int { return (int(wd) + 6) % 7 }

// scaleBars fills in each bar's Pct relative to the tallest one.
func scaleBars(bars []statBar) []statBar {
	peak := 0
	for _, b := range bars {
		peak = max(peak, b.Count)
	}
	if peak == 0 {
		return bars
	}
	for i := range bars {
		bars[i].Pct = bars[i].Count * 100 / peak
	}
	return bars
}

type statsPageData struct {
	Reading readingStats
	User    store.UserStats
}

// handleStats serves /stats, the requesting user's full reading summary.
// Like the strip on the library page it spans every library.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	reading, err := s.userReadingStats(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	user, err := s.store.UserStats(userFrom(r))
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "stats.html", statsPageData{Reading: reading, User: user})
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
