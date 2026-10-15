package server

import (
	"fmt"
	"strconv"
	"time"

	"comicnest/internal/opds"
)

// pageStats is what the user's page-read log (Store.PageReadTimes) reduces
// to: the page-level half of /stats, counting individual pages rather than
// finished issues.
type pageStats struct {
	Total     int
	Today     int
	ThisWeek  int // since Monday
	ThisMonth int
	ThisYear  int
	Days      int    // distinct days with at least one page read
	Since     string // the first logged day — tracking only started with migration 15

	BestDay      string // the day with the most pages ("" when none)
	BestDayCount int

	// Sessions and ReadingTime come from the gaps between consecutive page
	// reads: a gap up to sessionGap is time spent on the earlier page, a
	// longer one ends the session. The last page of each session adds
	// nothing (there's no "closed the reader" event), so this is a floor.
	Sessions    int
	ReadingTime time.Duration
	timedPages  int // pages whose reading time is known (followed within sessionGap)

	Last30 []statBar // the last 30 days, oldest first
	Hours  []statBar // hour of day, 0..23, all time
	Months []statBar // the last 12 calendar months, oldest first
}

// sessionGap is the longest pause between two pages that still counts as
// one reading session.
const sessionGap = 5 * time.Minute

// AvgPerDay is the mean number of pages on days with any reading.
func (p pageStats) AvgPerDay() int {
	if p.Days == 0 {
		return 0
	}
	return (p.Total + p.Days/2) / p.Days
}

// SecondsPerPage is the mean time spent on a page, over pages whose time
// is known (0 when none is).
func (p pageStats) SecondsPerPage() int {
	if p.timedPages == 0 {
		return 0
	}
	return int(p.ReadingTime.Round(time.Second).Seconds()) / p.timedPages
}

// ReadingTimeText renders ReadingTime as "3 h 12 min" / "45 min" / "<1 min".
func (p pageStats) ReadingTimeText() string {
	mins := int(p.ReadingTime / time.Minute)
	switch {
	case mins < 1:
		return "<1 min"
	case mins < 60:
		return fmt.Sprintf("%d min", mins)
	default:
		return fmt.Sprintf("%d h %d min", mins/60, mins%60)
	}
}

// PeakHour names the hour of day with the most pages read ("" when none).
func (p pageStats) PeakHour() string {
	best := statBar{}
	for _, b := range p.Hours {
		if b.Count > best.Count {
			best = b
		}
	}
	return best.Tip
}

// computePageStats buckets raw UTC page-read timestamps (oldest first, as
// Store.PageReadTimes returns them) into local calendar days, hours and
// months the same way computeReadingStats does for finishes.
func computePageStats(rawTimes []string, now time.Time) pageStats {
	var st pageStats
	loc := now.Location()
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, loc)
	weekStart := today.AddDate(0, 0, -mondayIndex(now.Weekday()))

	const dayFormat = "2006-01-02"
	last30 := make([]statBar, 30)
	dayIndex := make(map[string]int, len(last30))
	for i := range last30 {
		day := today.AddDate(0, 0, i-(len(last30)-1))
		// Label only Mondays and the two ends, so 30 bars stay legible.
		if i == 0 || i == len(last30)-1 || day.Weekday() == time.Monday {
			last30[i].Label = strconv.Itoa(day.Day())
		}
		last30[i].Tip = day.Format("Mon 2 Jan")
		dayIndex[day.Format(dayFormat)] = i
	}
	hours := make([]statBar, 24)
	for h := range hours {
		if h%6 == 0 {
			hours[h].Label = strconv.Itoa(h)
		}
		hours[h].Tip = fmt.Sprintf("%02d:00", h)
	}
	months, monthIndex := lastMonths(now)

	perDay := make(map[string]int)
	var prev time.Time
	for _, raw := range rawTimes {
		t := opds.ParseDBTime(raw, time.Time{})
		if t.IsZero() {
			continue
		}
		local := t.In(loc)
		st.Total++
		ly, lm, _ := local.Date()
		if ly == y {
			st.ThisYear++
			if lm == m {
				st.ThisMonth++
			}
		}
		if !local.Before(weekStart) {
			st.ThisWeek++
		}
		if !local.Before(today) {
			st.Today++
		}
		key := local.Format(dayFormat)
		perDay[key]++
		if i, ok := dayIndex[key]; ok {
			last30[i].Count++
		}
		if i, ok := monthIndex[local.Format(monthFormat)]; ok {
			months[i].Count++
		}
		hours[local.Hour()].Count++

		if gap := t.Sub(prev); prev.IsZero() || gap > sessionGap || gap < 0 {
			st.Sessions++
		} else {
			st.ReadingTime += gap
			st.timedPages++
		}
		prev = t
	}

	st.Days = len(perDay)
	for day, n := range perDay {
		if st.Since == "" || day < st.Since {
			st.Since = day
		}
		if n > st.BestDayCount || (n == st.BestDayCount && day > st.BestDay) {
			st.BestDay, st.BestDayCount = day, n
		}
	}
	st.Since = displayDay(st.Since, loc)
	st.BestDay = displayDay(st.BestDay, loc)
	st.Last30 = scaleBars(last30)
	st.Hours = scaleBars(hours)
	st.Months = scaleBars(months)
	return st
}

// displayDay reformats a "2006-01-02" key as "2 Jan 2006" ("" stays "").
func displayDay(key string, loc *time.Location) string {
	t, err := time.ParseInLocation("2006-01-02", key, loc)
	if err != nil {
		return ""
	}
	return t.Format("2 Jan 2006")
}
