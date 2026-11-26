package store

import (
	"fmt"
	"reflect"
	"testing"
)

func owned(nums ...string) []OwnedIssue {
	out := make([]OwnedIssue, len(nums))
	for i, n := range nums {
		out[i] = OwnedIssue{Number: n}
	}
	return out
}

func missingNums(g *Gaps) []int {
	var out []int
	for _, m := range g.Missing {
		out = append(out, m.Number)
	}
	return out
}

func TestParseIssueInt(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		ok   bool
	}{
		{"55", 55, true}, {"055", 55, true}, {" 7 ", 7, true}, {"#12", 12, true},
		{"0", 0, true}, {"000", 0, true},
		{"", 0, false}, {"1.5", 0, false}, {"Annual 1", 0, false}, {"12a", 0, false},
		{"-1", 0, false}, {"½", 0, false}, {"1234567", 0, false},
	} {
		got, ok := ParseIssueInt(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ParseIssueInt(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestFormatRanges(t *testing.T) {
	for _, tc := range []struct {
		in   []int
		want string
	}{
		{nil, ""},
		{[]int{20}, "#20"},
		{[]int{12, 13, 14, 20}, "#12–14, #20"},
		{[]int{13, 12}, "#12, #13"}, // a run of two stays spelled out
		{[]int{20, 12, 14, 13, 13}, "#12–14, #20"},
		{[]int{1, 3, 5}, "#1, #3, #5"},
		{[]int{6, 7, 8, 9, 40, 41, 42}, "#6–9, #40–42"},
	} {
		if got := FormatRanges(tc.in, "–"); got != tc.want {
			t.Errorf("FormatRanges(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := FormatRanges([]int{1, 2, 3}, "-"); got != "#1-3" {
		t.Errorf("ASCII separator: %q", got)
	}
}

func TestInferredGaps(t *testing.T) {
	t.Run("simple hole", func(t *testing.T) {
		g := ComputeInferredGaps(owned("1", "2", "3", "4", "5", "9"))
		if g == nil || !reflect.DeepEqual(missingNums(g), []int{6, 7, 8}) {
			t.Fatalf("got %+v", g)
		}
		if g.Owned != 6 || g.Total != 9 || g.RangeText() != "#6–8" || g.Source != GapSourceInferred {
			t.Errorf("summary wrong: %+v %q", g, g.RangeText())
		}
	})
	t.Run("leading zeros are the same number", func(t *testing.T) {
		g := ComputeInferredGaps(owned("001", "002", "004", "5"))
		if g == nil || !reflect.DeepEqual(missingNums(g), []int{3}) {
			t.Fatalf("got %+v", g)
		}
	})
	t.Run("no gaps", func(t *testing.T) {
		g := ComputeInferredGaps(owned("1", "2", "3"))
		if g == nil || g.MissingCount() != 0 {
			t.Fatalf("got %+v", g)
		}
	})
	t.Run("numbers below the lowest owned are unknowable", func(t *testing.T) {
		g := ComputeInferredGaps(owned("10", "11", "13"))
		if g == nil || !reflect.DeepEqual(missingNums(g), []int{12}) {
			t.Fatalf("got %+v", g)
		}
	})
	t.Run("annuals and decimals neither create nor fill gaps", func(t *testing.T) {
		g := ComputeInferredGaps(owned("1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "Annual 1", "1.5", "0.5"))
		if g == nil || g.MissingCount() != 0 {
			t.Fatalf("got %+v", g)
		}
		// "2.5" doesn't stand in for #3.
		g = ComputeInferredGaps(owned("1", "2", "2.5", "4", "5", "6", "7", "8", "9"))
		if g == nil || !reflect.DeepEqual(missingNums(g), []int{3}) {
			t.Fatalf("got %+v", g)
		}
	})
	t.Run("mostly non-integer numbering is skipped", func(t *testing.T) {
		if g := ComputeInferredGaps(owned("1", "3", "Vol 1", "Vol 2", "Vol 3")); g != nil {
			t.Errorf("expected nil, got %+v", g)
		}
	})
	t.Run("unnumbered issues count against the ratio", func(t *testing.T) {
		if g := ComputeInferredGaps(owned("1", "3", "", "", "")); g != nil {
			t.Errorf("expected nil, got %+v", g)
		}
	})
	t.Run("a single number or empty list is not a candidate", func(t *testing.T) {
		if ComputeInferredGaps(owned("7")) != nil || ComputeInferredGaps(nil) != nil {
			t.Error("expected nil")
		}
	})
	t.Run("implausibly sparse ranges are skipped", func(t *testing.T) {
		if g := ComputeInferredGaps(owned("1", "2000")); g != nil {
			t.Errorf("expected nil, got %+v", g)
		}
	})
	t.Run("vanished files are missing but distinguished", func(t *testing.T) {
		in := owned("1", "2", "3", "4")
		in[1].FileMissing = true
		g := ComputeInferredGaps(in)
		if g == nil || !reflect.DeepEqual(missingNums(g), []int{2}) || !g.Missing[0].Vanished {
			t.Fatalf("got %+v", g)
		}
		if g.VanishedCount() != 1 || g.VanishedText() != "#2" || g.Owned != 3 {
			t.Errorf("vanished summary wrong: %+v", g)
		}
	})
	t.Run("a present duplicate beats a vanished one", func(t *testing.T) {
		in := append(owned("1", "2", "3"), OwnedIssue{Number: "2", FileMissing: true})
		if g := ComputeInferredGaps(in); g == nil || g.MissingCount() != 0 {
			t.Fatalf("got %+v", g)
		}
	})
}

func cvList(dates map[string]string, nums ...string) []CVIssue {
	var out []CVIssue
	for i, n := range nums {
		out = append(out, CVIssue{CVIssueID: int64(100 + i), IssueNumber: n, Name: "T" + n, StoreDate: dates[n]})
	}
	return out
}

func TestCVGaps(t *testing.T) {
	today := "2026-11-26"
	t.Run("compares against the volume list", func(t *testing.T) {
		g := ComputeCVGaps(cvList(nil, "1", "2", "3", "4", "5"), owned("1", "02", "5"), today)
		if g == nil || !reflect.DeepEqual(missingNums(g), []int{3, 4}) || g.Owned != 3 || g.Total != 5 {
			t.Fatalf("got %+v", g)
		}
		if g.Missing[0].Name != "T3" || !g.FromComicVine() {
			t.Errorf("name/source wrong: %+v", g.Missing[0])
		}
	})
	t.Run("leading gaps count with a list", func(t *testing.T) {
		g := ComputeCVGaps(cvList(nil, "1", "2", "3"), owned("3"), today)
		if g == nil || !reflect.DeepEqual(missingNums(g), []int{1, 2}) {
			t.Fatalf("got %+v", g)
		}
	})
	t.Run("upcoming issues are not missing yet", func(t *testing.T) {
		dates := map[string]string{"1": "2026-01-01", "2": "2026-12-03", "3": "2027-01-07"}
		g := ComputeCVGaps(cvList(dates, "1", "2", "3"), owned("1"), today)
		if g == nil || g.MissingCount() != 0 || g.Total != 1 {
			t.Fatalf("got %+v", g)
		}
	})
	t.Run("cover date stands in for a missing store date", func(t *testing.T) {
		list := []CVIssue{{CVIssueID: 1, IssueNumber: "1"}, {CVIssueID: 2, IssueNumber: "2", CoverDate: "2027-03-01"}}
		g := ComputeCVGaps(list, owned("1"), today)
		if g == nil || g.MissingCount() != 0 {
			t.Fatalf("got %+v", g)
		}
	})
	t.Run("annuals and decimals in the volume are ignored", func(t *testing.T) {
		g := ComputeCVGaps(cvList(nil, "1", "2", "Annual 1", "1.5", "", "2.1"), owned("1", "2"), today)
		if g == nil || g.MissingCount() != 0 || g.Total != 2 {
			t.Fatalf("got %+v", g)
		}
	})
	t.Run("duplicate numbers collapse", func(t *testing.T) {
		g := ComputeCVGaps(cvList(nil, "1", "1", "2"), owned("1"), today)
		if g == nil || g.Total != 2 || !reflect.DeepEqual(missingNums(g), []int{2}) {
			t.Fatalf("got %+v", g)
		}
	})
	t.Run("never owned versus file disappeared", func(t *testing.T) {
		in := owned("1", "2", "4")
		in[1].FileMissing = true
		g := ComputeCVGaps(cvList(nil, "1", "2", "3", "4"), in, today)
		if g == nil || !reflect.DeepEqual(missingNums(g), []int{2, 3}) {
			t.Fatalf("got %+v", g)
		}
		if !g.Missing[0].Vanished || g.Missing[1].Vanished {
			t.Errorf("vanished flags wrong: %+v", g.Missing)
		}
		if g.VanishedText() != "#2" || g.RangeText() != "#2, #3" {
			t.Errorf("text: %q %q", g.VanishedText(), g.RangeText())
		}
	})
	t.Run("nothing numeric or released yields nil", func(t *testing.T) {
		if ComputeCVGaps(cvList(nil, "Annual 1"), owned("1"), today) != nil || ComputeCVGaps(nil, owned("1"), today) != nil {
			t.Error("expected nil")
		}
	})
}

func addTestIssues(t *testing.T, st *Store, seriesID int64, nums ...string) []*Issue {
	t.Helper()
	var out []*Issue
	for _, n := range nums {
		i := &Issue{SeriesID: seriesID, Path: fmt.Sprintf("s%d-%s.cbz", seriesID, n), IssueNumber: n, MetadataSource: SourceFilename}
		if err := st.InsertIssue(i); err != nil {
			t.Fatal(err)
		}
		out = append(out, i)
	}
	return out
}

func TestSeriesGapsStore(t *testing.T) {
	st := openTestStore(t)
	today := "2026-11-26"

	// Unmatched series: inferred.
	inf, _ := st.FindOrCreateSeriesByFolder("Inferred", "Inferred", "libA")
	addTestIssues(t, st, inf, "1", "2", "5")
	// Matched series with a cached volume list.
	cv, _ := st.FindOrCreateSeriesByFolder("Matched", "Matched", "libB")
	rows := addTestIssues(t, st, cv, "1", "2", "3")
	if err := st.SetSeriesComicVineVolume(cv, 77, ""); err != nil {
		t.Fatal(err)
	}
	// Before a full sync the volume's list isn't trusted: falls back to inference.
	g, err := st.SeriesGaps(cv, today)
	if err != nil || g == nil || g.Source != GapSourceInferred || g.MissingCount() != 0 {
		t.Fatalf("unsynced: %+v %v", g, err)
	}
	// A partial (release-check) row must not make the volume "synced".
	if err := st.UpsertVolumeIssues([]CVIssue{{VolumeID: 77, CVIssueID: 9, IssueNumber: "9", StoreDate: "2026-11-20"}}, 0, false); err != nil {
		t.Fatal(err)
	}
	if g, _ := st.SeriesGaps(cv, today); g.Source != GapSourceInferred {
		t.Fatalf("partial rows must not count: %+v", g)
	}
	full := []CVIssue{
		{VolumeID: 77, CVIssueID: 1, IssueNumber: "1"}, {VolumeID: 77, CVIssueID: 2, IssueNumber: "2"},
		{VolumeID: 77, CVIssueID: 3, IssueNumber: "3"}, {VolumeID: 77, CVIssueID: 4, IssueNumber: "4", Name: "Four", StoreDate: "2026-05-01"},
		{VolumeID: 77, CVIssueID: 9, IssueNumber: "9"}, // dates merge with the partial row
	}
	if err := st.UpsertVolumeIssues(full, 77, true); err != nil {
		t.Fatal(err)
	}
	g, err = st.SeriesGaps(cv, today)
	if err != nil || g == nil || g.Source != GapSourceComicVine {
		t.Fatalf("synced: %+v %v", g, err)
	}
	if !reflect.DeepEqual(missingNums(g), []int{4, 9}) || g.Missing[0].Name != "Four" || g.Missing[0].Date != "2026-05-01" {
		t.Errorf("missing: %+v", g.Missing)
	}
	if g.Missing[1].Date != "2026-11-20" {
		t.Errorf("an empty date must not wipe a known one: %+v", g.Missing[1])
	}

	// A flagged-missing file counts as vanished.
	if err := st.MarkIssuesMissing([]int64{rows[0].ID}); err != nil {
		t.Fatal(err)
	}
	g, _ = st.SeriesGaps(cv, today)
	if !reflect.DeepEqual(missingNums(g), []int{1, 4, 9}) || !g.Missing[0].Vanished {
		t.Errorf("vanished: %+v", g.Missing)
	}

	// One-shots are skipped.
	one, _ := st.FindOrCreateSeriesByFolder("One", "One", "libA")
	addTestIssues(t, st, one, "1", "4")
	if err := st.SetSeriesOneShot(one, true); err != nil {
		t.Fatal(err)
	}
	if g, _ := st.SeriesGaps(one, today); g != nil {
		t.Errorf("one-shot: %+v", g)
	}

	// The list: biggest gap first, library scoped.
	list, err := st.ListSeriesGaps("", today)
	if err != nil || len(list) != 2 || list[0].SeriesID != cv || list[1].SeriesID != inf {
		t.Fatalf("list: %+v %v", list, err)
	}
	if list, _ = st.ListSeriesGaps("libA", today); len(list) != 1 || list[0].SeriesID != inf {
		t.Errorf("library scope: %+v", list)
	}
}

func TestStaleVolumeIDs(t *testing.T) {
	st := openTestStore(t)
	a, _ := st.FindOrCreateSeriesByFolder("A", "A", "")
	b, _ := st.FindOrCreateSeriesByFolder("B", "B", "")
	addTestIssues(t, st, a, "1")
	addTestIssues(t, st, b, "1")
	st.SetSeriesComicVineVolume(a, 10, "")
	st.SetSeriesComicVineVolume(b, 20, "")

	ids, err := st.StaleVolumeIDs(0, 10)
	if err != nil || len(ids) != 2 {
		t.Fatalf("never synced: %v %v", ids, err)
	}
	if err := st.UpsertVolumeIssues(nil, 10, true); err != nil {
		t.Fatal(err)
	}
	ids, _ = st.StaleVolumeIDs(0, 10)
	if len(ids) != 1 || ids[0] != 20 {
		t.Fatalf("after syncing 10: %v", ids)
	}
	if fresh, _ := st.VolumeSyncedWithin(10, 3600e9); !fresh {
		t.Error("volume 10 should be fresh")
	}
	if _, err := st.db.Exec(`UPDATE cv_volume_sync SET synced_at = '2020-01-01 00:00:00' WHERE volume_id = 10`); err != nil {
		t.Fatal(err)
	}
	ids, _ = st.StaleVolumeIDs(0, 10)
	if len(ids) != 2 || ids[0] != 20 { // never-synced first, then the oldest
		t.Fatalf("stale ordering: %v", ids)
	}
	if ids, _ = st.StaleVolumeIDs(0, 1); len(ids) != 1 {
		t.Errorf("limit ignored: %v", ids)
	}
}
