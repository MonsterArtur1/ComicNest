package library

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"comicnest/internal/store"
)

const extrasComicInfo = `<?xml version="1.0"?><ComicInfo><Series>Spider</Series><Number>1</Number>
<Characters>Spider-Man, Venom</Characters><StoryArc>Maximum Carnage</StoryArc><StoryArcNumber>4</StoryArcNumber></ComicInfo>`

// writeSpiderComic creates root/Spider/Spider 001.cbz tagged with extrasComicInfo.
func writeSpiderComic(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "Spider")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return buildCBZ(t, dir, "Spider 001.cbz",
		map[string][]byte{"p001.png": tinyPNG(t), "ComicInfo.xml": []byte(extrasComicInfo)})
}

func TestScanReadsComicInfoCharactersAndArcs(t *testing.T) {
	root := t.TempDir()
	writeSpiderComic(t, root)
	plain := writeComic(t, filepath.Join(root, "Plain"), "Plain 001.cbz", "", "")

	sc, st := newTestScanner(t, root)
	if err := sc.scan(); err != nil {
		t.Fatal(err)
	}

	chars, err := st.ListCharacters()
	if err != nil || len(chars) != 2 {
		t.Fatalf("characters = %+v, %v", chars, err)
	}
	spider, _ := st.GetIssueByPath(filepath.Join(root, "Spider", "Spider 001.cbz"))
	pos, err := st.ArcPositionsOfIssue(spider.ID)
	if err != nil || len(pos) != 1 || pos[0].Arc.Name != "Maximum Carnage" {
		t.Fatalf("arc positions = %+v, %v", pos, err)
	}
	// A file without ComicInfo is marked as inspected too, so it isn't re-read.
	pi, _ := st.GetIssueByPath(plain)
	if pending, _ := st.ComicInfoExtrasPending(pi.ID); pending {
		t.Error("an issue without ComicInfo should not stay pending")
	}
}

// Rows catalogued before the feature have NULL comicinfo_extras; the next scan
// reads them even when their metadata is locked or from ComicVine.
func TestScanBackfillsComicInfoExtrasOfExistingIssues(t *testing.T) {
	root := t.TempDir()
	path := writeSpiderComic(t, root)

	sc, st := newTestScanner(t, root)
	if err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	issue, _ := st.GetIssueByPath(path)
	// Simulate an upgrade: locked metadata, extras never read.
	issue.MetadataLocked = true
	if err := st.UpdateIssueMetadata(issue); err != nil {
		t.Fatal(err)
	}
	if err := st.ForgetComicInfoExtras(issue.ID); err != nil {
		t.Fatal(err)
	}
	if pending, _ := st.ComicInfoExtrasPending(issue.ID); !pending {
		t.Fatal("setup: should be pending")
	}

	if err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	if pending, _ := st.ComicInfoExtrasPending(issue.ID); pending {
		t.Error("the scan did not backfill the locked issue")
	}
	if chars, _ := st.IssueCharacters(issue.ID); len(chars) != 2 {
		t.Errorf("characters after backfill = %+v", chars)
	}
}

func TestUnreadableArchiveStaysPending(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Broken 001.cbz")
	if err := os.WriteFile(path, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	sc, st := newTestScanner(t, root)
	if err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	issue, _ := st.GetIssueByPath(path)
	if issue == nil {
		t.Fatal("not catalogued")
	}
	if pending, _ := st.ComicInfoExtrasPending(issue.ID); !pending {
		t.Error("an unreadable archive must stay pending so a later scan retries")
	}
}

func TestResetIssueMetadataDropsComicVineExtrasOnly(t *testing.T) {
	root := t.TempDir()
	path := writeSpiderComic(t, root)
	sc, st := newTestScanner(t, root)
	if err := sc.scan(); err != nil {
		t.Fatal(err)
	}
	issue, _ := st.GetIssueByPath(path)
	issue.ComicVineIssueID = sql.NullInt64{Int64: 5, Valid: true}
	issue.MetadataSource = store.SourceComicVine
	if err := st.UpdateIssueMetadata(issue); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReplaceComicVineExtras(issue.ID, []store.CharacterInput{{Name: "Carnage", ComicVineID: 9}}, nil); err != nil {
		t.Fatal(err)
	}

	if err := ResetIssueMetadata(st, sc.covers, issue); err != nil {
		t.Fatal(err)
	}
	chars, _ := st.IssueCharacters(issue.ID)
	names := map[string]bool{}
	for _, c := range chars {
		names[c.Name] = true
	}
	if len(chars) != 2 || !names["Spider-Man"] || names["Carnage"] {
		t.Errorf("characters after unlink = %+v, want the ComicInfo ones only", chars)
	}
}
