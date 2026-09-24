package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"comicnest/internal/store"
)

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		t.Fatalf("day(%q): %v", s, err)
	}
	return d
}

func TestDayStreaks(t *testing.T) {
	tests := []struct {
		name              string
		days              []string
		today             string
		wantCurr, wantLng int
	}{
		{"empty", nil, "2026-09-10", 0, 0},
		{"streak ending today", []string{"2026-09-08", "2026-09-09", "2026-09-10"}, "2026-09-10", 3, 3},
		{"grace day — nothing read yet today", []string{"2026-09-07", "2026-09-08", "2026-09-09"}, "2026-09-10", 3, 3},
		{"broken — last read two days ago", []string{"2026-09-07", "2026-09-08"}, "2026-09-10", 0, 2},
		{"longest run predates a shorter current one", []string{"2026-09-01", "2026-09-02", "2026-09-03", "2026-09-04", "2026-09-10"}, "2026-09-10", 1, 4},
		{"single day today", []string{"2026-09-10"}, "2026-09-10", 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			days := make(map[string]bool, len(tt.days))
			for _, d := range tt.days {
				days[d] = true
			}
			curr, lng := dayStreaks(days, day(t, tt.today))
			if curr != tt.wantCurr || lng != tt.wantLng {
				t.Errorf("dayStreaks() = (%d, %d), want (%d, %d)", curr, lng, tt.wantCurr, tt.wantLng)
			}
		})
	}
}

func TestComputeReadingStats(t *testing.T) {
	now := day(t, "2026-09-10").Add(12 * time.Hour) // noon UTC, so now.Location() == UTC

	t.Run("empty input", func(t *testing.T) {
		st := computeReadingStats(nil, now)
		if st.Any() {
			t.Errorf("computeReadingStats(nil) = %+v, want nothing to show", st)
		}
	})

	t.Run("counts only this calendar month", func(t *testing.T) {
		raw := []string{
			"2026-08-31 23:59:00", // last month
			"2026-09-01 00:00:00", // this month
			"2026-09-09 08:00:00", // this month
			"2026-09-10 11:59:00", // this month, same day as now
		}
		st := computeReadingStats(raw, now)
		if st.ReadThisMonth != 3 {
			t.Errorf("ReadThisMonth = %d, want 3", st.ReadThisMonth)
		}
	})

	t.Run("streak follows from the same timestamps", func(t *testing.T) {
		raw := []string{
			"2026-09-08 10:00:00",
			"2026-09-09 10:00:00",
			"2026-09-10 10:00:00",
		}
		st := computeReadingStats(raw, now)
		if st.CurrentStreak != 3 || st.LongestStreak != 3 {
			t.Errorf("streaks = (%d, %d), want (3, 3)", st.CurrentStreak, st.LongestStreak)
		}
	})

	t.Run("multiple finishes on one day count once for the streak", func(t *testing.T) {
		raw := []string{
			"2026-09-10 09:00:00",
			"2026-09-10 21:00:00",
		}
		st := computeReadingStats(raw, now)
		if st.CurrentStreak != 1 {
			t.Errorf("CurrentStreak = %d, want 1", st.CurrentStreak)
		}
		if st.ReadThisMonth != 2 {
			t.Errorf("ReadThisMonth = %d, want 2 (each finish still counts)", st.ReadThisMonth)
		}
	})

	t.Run("week, year, months and weekdays", func(t *testing.T) {
		// now is Thursday 2026-09-10, so the week started Monday 2026-09-07.
		raw := []string{
			"2025-09-30 10:00:00", // last year, outside the 12-month window
			"2025-10-15 10:00:00", // last year, oldest bar
			"2026-02-02 10:00:00", // this year, a Monday
			"2026-09-06 10:00:00", // Sunday before this week
			"2026-09-07 10:00:00", // Monday of this week
			"2026-09-10 10:00:00", // today (Thursday)
		}
		st := computeReadingStats(raw, now)
		if st.ReadThisWeek != 2 || st.ReadThisMonth != 3 || st.ReadThisYear != 4 || st.ReadingDays != 6 {
			t.Errorf("week/month/year/days = %d/%d/%d/%d, want 2/3/4/6",
				st.ReadThisWeek, st.ReadThisMonth, st.ReadThisYear, st.ReadingDays)
		}
		if len(st.Months) != 12 || st.Months[0].Label != "Oct" || st.Months[0].Count != 1 ||
			st.Months[11].Label != "Sep" || st.Months[11].Count != 3 || st.Months[11].Pct != 100 {
			t.Errorf("Months = %+v, want Oct..Sep with 1 in Oct and 3 (100%%) in Sep", st.Months)
		}
		if len(st.Weekdays) != 7 || st.Weekdays[0].Label != "Mon" || st.Weekdays[0].Count != 2 || st.Weekdays[6].Label != "Sun" {
			t.Errorf("Weekdays = %+v, want Mon-first with 2 Mondays", st.Weekdays)
		}
		if got := st.BusiestWeekday(); got != "Mon" {
			t.Errorf("BusiestWeekday = %q, want Mon", got)
		}
	})
}

func TestStatsPage(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()

	rec := get(t, h, "/stats", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Nothing here yet") {
		t.Fatalf("/stats with no reading: %d\n%s", rec.Code, rec.Body)
	}

	issues, err := srv.store.ListIssuesBySeries(1, store.IssueFilterAll)
	if err != nil || len(issues) == 0 {
		t.Fatalf("ListIssuesBySeries: %v, %v", issues, err)
	}
	if err := srv.store.SetReadingProgress("", issues[0].ID, issues[0].FilePages); err != nil {
		t.Fatal(err)
	}
	rec = get(t, h, "/stats", nil)
	body := rec.Body.String()
	for _, want := range []string{"issues read", "Most read series", `href="/series/1">Saga</a>`, "Brian K. Vaughan", "Last 12 months"} {
		if !strings.Contains(body, want) {
			t.Errorf("/stats missing %q", want)
		}
	}
	if home := get(t, h, "/", nil).Body.String(); !strings.Contains(home, `href="/stats"`) {
		t.Error("library page has no link to /stats")
	}
}
