package main

import (
	"database/sql"
	"log"

	_ "github.com/mattn/go-sqlite3"
)

const dbName = "database.sqlite"

func CreateDatabase() {
	db, err := sql.Open("sqlite3", dbName)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	sqlStmt := `
	create table foo (id integer not null primary key, path text, processed bool, volume_name text, name text, issue_number text, image_uri text, disk_size text, UNIQUE(path));
	delete from foo;
	`
	_, err = db.Exec(sqlStmt)
	if err != nil {
		log.Printf("%q: %s\n", err, sqlStmt)
		return
	}
	db.Close()
}

func SelectFromDb(onlyUnprocessed bool) []IssueEntry {
	db, _ := sql.Open("sqlite3", dbName)
	query := "SELECT * from foo"

	if onlyUnprocessed {
		query = "select * from foo where not processed"
	}

	response, err := db.Query(query)
	if err != nil {
		log.Fatal(err)
	}
	defer response.Close()
	var issues []IssueEntry
	for response.Next() {
		var issue IssueEntry
		_ = response.Scan(&issue.Id, &issue.Path, &issue.Processed, &issue.VolumeName, &issue.Name, &issue.IssueNumber, &issue.ImageUri, &issue.DiskSize)
		issues = append(issues, issue)
	}
	db.Close()
	return issues
}

// func InsertToDb(name string, title string, issue string) {
// 	db, _ := sql.Open("sqlite3", dbName)
// 	tx, err := db.Begin()
// 	if err != nil {
// 		log.Fatal(err)
// 	}
// 	stmt, err := tx.Prepare("insert or ignore into foo(id, path, processed, title, issue,thumbnailName) values(NULL, ?, false,?,?,'')")
// 	if err != nil {
// 		log.Fatal(err)
// 	}
// 	defer stmt.Close()

// 	_, err = stmt.Exec(name, title, issue)
// 	if err != nil {
// 		log.Fatal(err)
// 	}
// 	err = tx.Commit()
// 	if err != nil {
// 		log.Fatal(err)
// 	}
// 	db.Close()
// }

func (issueEntry IssueEntry) InsertToDb() {
	db, _ := sql.Open("sqlite3", dbName)
	tx, err := db.Begin()
	if err != nil {
		log.Fatal(err)
	}
	stmt, err := tx.Prepare("insert or ignore into foo(id, path, processed, volume_name, name, issue_number, image_uri, disk_size) values(null, ?, ?,?,?,?,?, ?)")
	if err != nil {
		log.Fatal(err)
	}
	defer stmt.Close()

	_, err = stmt.Exec(issueEntry.Path, issueEntry.Processed, issueEntry.VolumeName, issueEntry.Name, issueEntry.IssueNumber, issueEntry.ImageUri, issueEntry.DiskSize)
	if err != nil {
		log.Fatal(err)
	}
	err = tx.Commit()
	if err != nil {
		log.Fatal(err)
	}
	db.Close()
}

func (issueEntry IssueEntry) UpdateIntoDb() {
	db, _ := sql.Open("sqlite3", dbName)
	tx, err := db.Begin()
	if err != nil {
		log.Fatal(err)
	}
	stmt, err := tx.Prepare("insert or replace into foo(id, path, processed, volume_name, name, issue_number, image_uri, disk_size) values(?,?, ?, ?,?,?,?, ?)")
	if err != nil {
		log.Fatal(err)
	}
	defer stmt.Close()

	_, err = stmt.Exec(issueEntry.Id, issueEntry.Path, issueEntry.Processed, issueEntry.VolumeName, issueEntry.Name, issueEntry.IssueNumber, issueEntry.ImageUri, issueEntry.DiskSize)
	if err != nil {
		log.Fatal(err)
	}
	err = tx.Commit()
	if err != nil {
		log.Fatal(err)
	}
	db.Close()
}
