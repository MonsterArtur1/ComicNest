package library

import (
	"os"

	"comicnest/internal/kosync"
)

// syncDocHashes keeps the issue's KOReader document ids (partial md5 of the
// content and md5 of the file name, see internal/kosync) current. It hashes
// issues that have none yet (backfill for libraries scanned before kosync
// existed) and issues whose size or mtime changed since the last hashing; for
// everything else it is a single indexed lookup. It reads at most ~12 KiB per
// file.
func (sc *Scanner) syncDocHashes(path string) {
	st, ok, err := sc.store.GetDocHashState(path)
	if err != nil {
		sc.problem(path, "hash", err)
		return
	}
	if !ok {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		sc.problem(path, "hash", err)
		return
	}
	mtime := info.ModTime().Unix()
	if st.Hashed && st.Size == info.Size() && st.MTime == mtime {
		return
	}
	partial, err := kosync.PartialMD5(path)
	if err != nil {
		sc.problem(path, "hash", err)
		return
	}
	if err := sc.store.SetIssueHashes(st.IssueID, partial, kosync.FilenameMD5(path), info.Size(), mtime); err != nil {
		sc.problem(path, "hash", err)
	}
}
