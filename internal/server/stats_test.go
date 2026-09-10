package server

import (
	"testing"
	"time"
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
}
