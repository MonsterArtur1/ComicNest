package library

import "comicnest/internal/store"

// recordComicInfoExtras stores the characters and story arcs a ComicInfo.xml
// names (nil ci = the file has none, which still marks it as read so the scan
// does not look again). A failure is a per-file problem, not a scan failure:
// the issue stays pending and the next scan retries.
func (sc *Scanner) recordComicInfoExtras(path string, issueID int64, ci *ComicInfo) {
	var characters []string
	var arcs []store.ArcInput
	if ci != nil {
		characters = ci.CharacterList()
		for _, a := range ci.StoryArcs() {
			arcs = append(arcs, store.ArcInput{Name: a.Name, Number: a.Number})
		}
	}
	if err := sc.store.ReplaceComicInfoExtras(issueID, characters, arcs); err != nil {
		sc.problem(path, "comicinfo", err)
	}
}
