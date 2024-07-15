package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"text/template"

	"github.com/upper/db/v4"
	"github.com/upper/db/v4/adapter/sqlite"
)

var config conf

func main() {

	config.getConf()

	if _, err := os.Stat(config.Database); errors.Is(err, os.ErrNotExist) {
		CreateDatabase()
	}

	http.Handle("/tmpl/css/", http.StripPrefix("/tmpl/css", http.FileServer(http.Dir("./tmpl/css"))))
	http.HandleFunc("/menu", mainMenuHandler)
	http.HandleFunc("/scan_directory", scanDirectoryHandler)
	http.HandleFunc("/analyze_library", analyzeLibraryHandler)

	fmt.Printf("server started, visit: http://localhost:%v/menu", config.Port)

	log.Fatal(http.ListenAndServe(":"+strconv.Itoa(config.Port), nil))
}

func analyzeLibraryHandler(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	settings := sqlite.ConnectionURL{Database: config.Database}

	sess, err := sqlite.Open(settings)
	if err != nil {
		log.Fatal("Open: ", err)
	}
	defer sess.Close()

	issue := SelectSingleIssueFromDb(sess, id)

	SearchComic(&issue)
	issue.UpdateIntoDb(sess)

	fmt.Fprint(w, "Database scan complete")
}

func scanDirectoryHandler(w http.ResponseWriter, r *http.Request) {
	ScanDir(config.Library)
	fmt.Fprint(w, "Library scan complete")
}

func mainMenuHandler(w http.ResponseWriter, r *http.Request) {
	settings := sqlite.ConnectionURL{Database: config.Database}

	sess, err := sqlite.Open(settings)
	if err != nil {
		log.Fatal("Open: ", err)
	}
	defer sess.Close()

	var templates = template.Must(template.ParseFiles("tmpl/sth.html", "tmpl/sth2.html"))

	filter := r.FormValue("filter")
	dbCond := db.Cond{}

	if filter == "all" {

	} else if filter == "unprocessed" {
		dbCond = db.Cond{"processed": false}
	} else if filter == "processed" {
		dbCond = db.Cond{"processed": true}
	}

	issues := SelectFromDb(sess, dbCond)

	p1 := &MainMenuPage{Comics: issues}

	templates.ExecuteTemplate(w, "sth2.html", p1)
}
