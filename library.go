package main

import (
	"fmt"
	"io/fs"
	"log"
	"math"
	"path/filepath"
	"strings"
)

func ScanDir(dir string) {
	err := filepath.WalkDir(dir,
		func(path string, dirEntry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if IsCorrentFile(dirEntry.Name()) {
				title, issue := ExtractDataFromPath(path)
				info, _ := dirEntry.Info()

				newIssue := IssueEntry{Path: path, VolumeName: title, IssueNumber: issue, DiskSize: prettyByteSize(info.Size())}
				newIssue.InsertToDb()
			}
			return nil
		})
	if err != nil {
		log.Println(err)
	}
}

func prettyByteSize(b int64) string {
	bf := float64(b)
	for _, unit := range []string{"", "K", "M", "G"} {
		if math.Abs(bf) < 1024.0 {
			return fmt.Sprintf("%3.1f%sB", bf, unit)
		}
		bf /= 1024.0
	}
	return fmt.Sprintf("%.1fYiB", bf)
}

func IsCorrentFile(path string) bool {
	ext := filepath.Ext(path)
	if ext == ".pdf" || ext == ".cbz" || ext == ".cbr" {
		return true
	}
	return false
}

func ExtractDataFromPath(path string) (string, string) { //title, issue
	filename := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	filenameSplited := strings.Split(filename, "#")

	return filenameSplited[0], filenameSplited[1]
}
