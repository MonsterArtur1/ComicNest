package store

import "testing"

func TestFollowRules(t *testing.T) {
	st := openTestStore(t)
	matched, _ := st.FindOrCreateSeriesByFolder("Matched", "Matched", "libA")
	unmatched, _ := st.FindOrCreateSeriesByFolder("Plain", "Plain", "libA")
	other, _ := st.FindOrCreateSeriesByFolder("Other", "Other", "libB")
	mi := addTestIssues(t, st, matched, "1", "2")
	addTestIssues(t, st, unmatched, "1")
	addTestIssues(t, st, other, "1")
	st.SetSeriesComicVineVolume(matched, 10, "")
	st.SetSeriesComicVineVolume(other, 30, "")

	state := func(user string, id int64) (bool, bool) {
		f, e, err := st.FollowState(user, id)
		if err != nil {
			t.Fatal(err)
		}
		return f, e
	}
	if f, e := state("ania", matched); f || e {
		t.Fatal("not following before reading")
	}
	if ids, _ := st.FollowedVolumeIDs(); len(ids) != 0 {
		t.Fatalf("no one follows yet: %v", ids)
	}

	// Implicit: started reading a matched series.
	if err := st.SetReadingProgress("ania", mi[0].ID, 3); err != nil {
		t.Fatal(err)
	}
	if f, e := state("ania", matched); !f || e {
		t.Fatalf("implicit follow: %v %v", f, e)
	}
	if f, _ := state("bartek", matched); f {
		t.Fatal("follow is per user")
	}
	if ids, _ := st.FollowedVolumeIDs(); len(ids) != 1 || ids[0] != 10 {
		t.Fatalf("followed volumes: %v", ids)
	}

	// Explicit unfollow overrides reading; the volume is no longer followed.
	if err := st.SetSeriesFollow("ania", matched, false); err != nil {
		t.Fatal(err)
	}
	if f, e := state("ania", matched); f || !e {
		t.Fatalf("explicit unfollow: %v %v", f, e)
	}
	if ids, _ := st.FollowedVolumeIDs(); len(ids) != 0 {
		t.Fatalf("unfollowed volume still followed: %v", ids)
	}

	// Explicit follow without reading, on a matched series only.
	if err := st.SetSeriesFollow("bartek", other, true); err != nil {
		t.Fatal(err)
	}
	if f, e := state("bartek", other); !f || !e {
		t.Fatalf("explicit follow: %v %v", f, e)
	}
	if ids, _ := st.FollowedVolumeIDs(); len(ids) != 1 || ids[0] != 30 {
		t.Fatalf("followed volumes: %v", ids)
	}
	if f, _ := state("ania", unmatched); f {
		t.Fatal("unmatched series can't be followed")
	}
	st.SetSeriesFollow("ania", unmatched, true)
	if f, _ := state("ania", unmatched); f {
		t.Fatal("unmatched series can't be followed, even explicitly")
	}
}

func TestListReleases(t *testing.T) {
	st := openTestStore(t)
	sid, _ := st.FindOrCreateSeriesByFolder("Saga", "Saga", "libA")
	rows := addTestIssues(t, st, sid, "055", "56")
	st.SetSeriesComicVineVolume(sid, 10, "")
	st.MarkIssuesMissing([]int64{rows[1].ID}) // #56's file is gone: not "in library"

	other, _ := st.FindOrCreateSeriesByFolder("Unread", "Unread", "libB")
	addTestIssues(t, st, other, "1")
	st.SetSeriesComicVineVolume(other, 20, "")

	err := st.UpsertVolumeIssues([]CVIssue{
		{VolumeID: 10, CVIssueID: 1, IssueNumber: "55", Name: "Owned", StoreDate: "2026-11-18", URL: "http://cv/1"},
		{VolumeID: 10, CVIssueID: 2, IssueNumber: "56", StoreDate: "2026-11-25"},
		{VolumeID: 10, CVIssueID: 3, IssueNumber: "57", StoreDate: "2026-12-02"},
		{VolumeID: 10, CVIssueID: 4, IssueNumber: "58", StoreDate: "2027-03-01"}, // outside the window
		{VolumeID: 10, CVIssueID: 5, IssueNumber: "59"},                          // no store date
		{VolumeID: 20, CVIssueID: 6, IssueNumber: "2", StoreDate: "2026-11-20"},
	}, 0, false)
	if err != nil {
		t.Fatal(err)
	}

	if rs, _ := st.ListReleases("ania", "2026-11-01", "2026-12-31", ""); len(rs) != 0 {
		t.Fatalf("not following anything yet: %+v", rs)
	}
	if err := st.SetReadingProgress("ania", rows[0].ID, 2); err != nil { // starts reading Saga
		t.Fatal(err)
	}
	rs, err := st.ListReleases("ania", "2026-11-01", "2026-12-31", "")
	if err != nil || len(rs) != 3 {
		t.Fatalf("releases: %+v %v", rs, err)
	}
	if rs[0].IssueNumber != "55" || !rs[0].InLibrary || rs[0].URL != "http://cv/1" || rs[0].SeriesName != "Saga" {
		t.Errorf("first: %+v", rs[0])
	}
	if rs[1].IssueNumber != "56" || rs[1].InLibrary {
		t.Errorf("a vanished file isn't in the library: %+v", rs[1])
	}
	if rs[2].IssueNumber != "57" || rs[2].InLibrary {
		t.Errorf("third: %+v", rs[2])
	}
	if rs, _ = st.ListReleases("ania", "2026-11-01", "2026-12-31", "libB"); len(rs) != 0 {
		t.Errorf("library scope: %+v", rs)
	}
	if rs, _ = st.ListReleases("bartek", "2026-11-01", "2026-12-31", ""); len(rs) != 0 {
		t.Errorf("per-user follow: %+v", rs)
	}
	// Explicit follow of the second series.
	st.SetSeriesFollow("ania", other, true)
	if rs, _ = st.ListReleases("ania", "2026-11-01", "2026-12-31", ""); len(rs) != 4 {
		t.Errorf("after following: %+v", rs)
	}
}

func TestMeta(t *testing.T) {
	st := openTestStore(t)
	if _, ok, err := st.MetaTime(MetaReleasesChecked); ok || err != nil {
		t.Fatalf("unset: %v %v", ok, err)
	}
	if err := st.SetMeta(MetaReleasesChecked, "2026-11-26 10:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMeta(MetaReleasesChecked, "2026-11-26 11:00:00"); err != nil {
		t.Fatal(err)
	}
	tm, ok, err := st.MetaTime(MetaReleasesChecked)
	if err != nil || !ok || tm.Hour() != 11 {
		t.Fatalf("MetaTime = %v %v %v", tm, ok, err)
	}
}
