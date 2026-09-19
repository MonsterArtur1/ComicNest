package store

import "testing"

// TestIssueFavorite checks the toggle, batch lookup and per-series listing.
// There is no series-level favorite — only individual issues can be
// favorited.
func TestIssueFavorite(t *testing.T) {
	st := openTestStore(t)
	seriesID, err := st.FindOrCreateSeriesByFolder("Saga", "Saga", "")
	if err != nil {
		t.Fatal(err)
	}
	one := &Issue{SeriesID: seriesID, Path: "Saga 001.cbz", IssueNumber: "1", MetadataSource: SourceFilename}
	two := &Issue{SeriesID: seriesID, Path: "Saga 002.cbz", IssueNumber: "2", MetadataSource: SourceFilename}
	for _, i := range []*Issue{one, two} {
		if err := st.InsertIssue(i); err != nil {
			t.Fatal(err)
		}
	}

	if fav, err := st.IsIssueFavorite("ania", one.ID); err != nil || fav {
		t.Fatalf("IsIssueFavorite before set = %v, %v", fav, err)
	}
	if err := st.SetIssueFavorite("ania", one.ID, true); err != nil {
		t.Fatal(err)
	}
	// Setting twice must not error (ON CONFLICT DO NOTHING).
	if err := st.SetIssueFavorite("ania", one.ID, true); err != nil {
		t.Fatal(err)
	}
	if fav, err := st.IsIssueFavorite("ania", one.ID); err != nil || !fav {
		t.Fatalf("IsIssueFavorite(one) = %v, %v", fav, err)
	}
	if fav, err := st.IsIssueFavorite("ania", two.ID); err != nil || fav {
		t.Fatalf("IsIssueFavorite(two) = %v, %v, want false", fav, err)
	}
	// Favoriting is scoped per user.
	if fav, err := st.IsIssueFavorite("bartek", one.ID); err != nil || fav {
		t.Fatalf("IsIssueFavorite(bartek) = %v, %v, want false (different user)", fav, err)
	}

	favs, err := st.IssueFavoritesFor("ania", []int64{one.ID, two.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !favs[one.ID] || favs[two.ID] {
		t.Fatalf("IssueFavoritesFor = %v, want only issue 1", favs)
	}

	bySeries, err := st.ListFavoriteIssuesBySeries("ania", seriesID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bySeries) != 1 || bySeries[0].ID != one.ID {
		t.Fatalf("ListFavoriteIssuesBySeries = %+v, want only issue 1", bySeries)
	}

	if err := st.SetIssueFavorite("ania", one.ID, false); err != nil {
		t.Fatal(err)
	}
	if fav, err := st.IsIssueFavorite("ania", one.ID); err != nil || fav {
		t.Fatalf("IsIssueFavorite after unset = %v, %v", fav, err)
	}
}

// TestSeriesListSurfacesIssueFavorite checks that favoriting a single issue
// makes its series show up under the ListSeries "favorite" filter and its
// IsFavorite flag — there is no way to favorite a series directly, so this
// is the only path that surfaces it.
func TestSeriesListSurfacesIssueFavorite(t *testing.T) {
	st := openTestStore(t)
	favID, err := st.FindOrCreateSeriesByFolder("Saga", "Saga", "")
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := st.FindOrCreateSeriesByFolder("Y", "Y The Last Man", "")
	if err != nil {
		t.Fatal(err)
	}
	issue := &Issue{SeriesID: favID, Path: "Saga 001.cbz", IssueNumber: "1", MetadataSource: SourceFilename}
	if err := st.InsertIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertIssue(&Issue{SeriesID: otherID, Path: "Y 001.cbz", IssueNumber: "1", MetadataSource: SourceFilename}); err != nil {
		t.Fatal(err)
	}

	if err := st.SetIssueFavorite("ania", issue.ID, true); err != nil {
		t.Fatal(err)
	}

	series, err := st.ListSeries("ania", "", SeriesSortName, SeriesFilterFavorite, "")
	if err != nil {
		t.Fatalf("ListSeries favorite filter: %v", err)
	}
	if len(series) != 1 || series[0].ID != favID || !series[0].IsFavorite {
		t.Fatalf("ListSeries(favorite) = %+v, want only %q surfaced by its favorited issue", series, "Saga")
	}

	all, err := st.ListSeries("ania", "", SeriesSortName, SeriesFilterAll, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, sr := range all {
		want := sr.ID == favID
		if sr.IsFavorite != want {
			t.Errorf("series %q IsFavorite = %v, want %v", sr.Name, sr.IsFavorite, want)
		}
	}

	// A different user's favorite must not surface the series for "ania".
	if series, err := st.ListSeries("bartek", "", SeriesSortName, SeriesFilterFavorite, ""); err != nil || len(series) != 0 {
		t.Fatalf("ListSeries(bartek, favorite) = %+v, %v, want empty (different user)", series, err)
	}
}

// TestFavoriteIssuesCombined checks that ListFavoriteIssues/CountFavoriteIssues
// list only the issues the user directly favorited, excluding missing files
// and other users' favorites.
func TestFavoriteIssuesCombined(t *testing.T) {
	st := openTestStore(t)
	seriesID, err := st.FindOrCreateSeriesByFolder("Saga", "Saga", "")
	if err != nil {
		t.Fatal(err)
	}

	fav1 := &Issue{SeriesID: seriesID, Path: "Saga 001.cbz", IssueNumber: "1", MetadataSource: SourceFilename}
	fav2 := &Issue{SeriesID: seriesID, Path: "Saga 002.cbz", IssueNumber: "2", MetadataSource: SourceFilename}
	// Not favorited.
	plain := &Issue{SeriesID: seriesID, Path: "Saga 003.cbz", IssueNumber: "3", MetadataSource: SourceFilename}
	// Favorited but the file has since disappeared — must not count.
	missing := &Issue{SeriesID: seriesID, Path: "Saga 004.cbz", IssueNumber: "4", MetadataSource: SourceFilename}
	for _, i := range []*Issue{fav1, fav2, plain, missing} {
		if err := st.InsertIssue(i); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.MarkIssuesMissing([]int64{missing.ID}); err != nil {
		t.Fatal(err)
	}

	for _, i := range []*Issue{fav1, fav2, missing} {
		if err := st.SetIssueFavorite("ania", i.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	// A different user's favorite must not leak in.
	if err := st.SetIssueFavorite("bartek", plain.ID, true); err != nil {
		t.Fatal(err)
	}

	n, err := st.CountFavoriteIssues("ania")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("CountFavoriteIssues(ania) = %d, want 2 (fav1, fav2)", n)
	}

	got, err := st.ListFavoriteIssues("ania", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ListFavoriteIssues(ania) = %d entries, want 2", len(got))
	}
	ids := map[int64]bool{}
	for _, i := range got {
		ids[i.ID] = true
	}
	for _, want := range []int64{fav1.ID, fav2.ID} {
		if !ids[want] {
			t.Errorf("ListFavoriteIssues missing issue %d", want)
		}
	}
	if ids[plain.ID] || ids[missing.ID] {
		t.Error("ListFavoriteIssues should exclude non-favorites and missing files")
	}

	if n, err := st.CountFavoriteIssues("bartek"); err != nil || n != 1 {
		t.Fatalf("CountFavoriteIssues(bartek) = %d, %v, want 1", n, err)
	}
}
