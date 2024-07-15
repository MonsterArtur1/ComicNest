package main

import (
	"database/sql"
	"log"

	"github.com/upper/db/v4"
)

func CreateDatabase() {

	db, err := sql.Open("sqlite3", config.Database)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	sqlStmt := `
	create table foo (id integer not null primary key, path text, processed bool, volume_name text, name text, issue_number text, image_uri text, store_date text, description text, disk_size text, created_at TEXT DEFAULT CURRENT_TIMESTAMP, UNIQUE(path));
	delete from foo;
	`
	_, err = db.Exec(sqlStmt)
	if err != nil {
		log.Printf("%q: %s\n", err, sqlStmt)
		return
	}
	db.Close()
}

func SelectFromDb(sess db.Session, querry db.Cond) []IssueEntry {

	var issues []IssueEntry
	coll := sess.Collection("foo")
	res := coll.Find(querry).OrderBy("-created_at")
	res.All(&issues)
	return issues
}

func SelectSingleIssueFromDb(sess db.Session, id string) IssueEntry {

	coll := sess.Collection("foo")

	res := coll.Find(db.Cond{"id": id})
	count, _ := res.Count()
	isse := IssueEntry{}
	if count > 0 {
		res.One(&isse)
	}
	return isse

}
func (issueEntry IssueEntry) InsertOnlyNew(sess db.Session) {

	coll := sess.Collection("foo")

	res := coll.Find(db.Cond{"path": issueEntry.Path})
	count, err := res.Count()
	if count == 0 {
		_, err = coll.Insert(issueEntry)
	}
	if err != nil {
		log.Fatal(err.Error())
	}
}

func (issueEntry *IssueEntry) UpdateIntoDb(sess db.Session) {

	coll := sess.Collection("foo")

	res := coll.Find(db.Cond{"id": issueEntry.Id})
	res.Update(issueEntry)
}
