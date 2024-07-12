package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"text/template"
)

func main() {
	if _, err := os.Stat(dbName); errors.Is(err, os.ErrNotExist) {
		CreateDatabase()
	}
	//ScanDir("D:\\Library")

	fmt.Println("server started")
	http.Handle("/tmpl/css/", http.StripPrefix("/tmpl/css", http.FileServer(http.Dir("./tmpl/css"))))
	http.HandleFunc("/menu", mainMenuHandler)
	http.HandleFunc("/scan_directory", scanDirectoryHandler)
	http.HandleFunc("/analyze_library", analyzeLibraryHandler)
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func analyzeLibraryHandler(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	issue := SelectSingleIssueFromDb(id)

	//	issues := SelectFromDb(true)
	//var c []IssueEntry
	//	for _, issue := range issues {
	//c = append(c, SearchComic(strings.Split(filename, "#")[0], strings.Split(filename, "#")[1]))
	SearchComic(&issue)
	issue.UpdateIntoDb()

	//	}

	fmt.Fprint(w, "Database scan complete")
}

func scanDirectoryHandler(w http.ResponseWriter, r *http.Request) {
	ScanDir("D:\\Library")
	fmt.Fprint(w, "Database scan complete")
}

func mainMenuHandler(w http.ResponseWriter, r *http.Request) {
	var templates = template.Must(template.ParseFiles("tmpl/sth.html", "tmpl/sth2.html"))

	filter := r.FormValue("filter")
	onlyUnprocessed := false
	if filter == "all" {
		onlyUnprocessed = false
	} else if filter == "unprocessed" {
		onlyUnprocessed = true
	}

	fmt.Println("hoł1 ")
	issues := SelectFromDb(onlyUnprocessed, false)
	// var c []IssueEntry
	// for _, filename := range filenames {
	// 	//c = append(c, SearchComic(strings.Split(filename, "#")[0], strings.Split(filename, "#")[1]))
	// 	c = append(c, IssueEntry{Name: strings.Split(filename, "#")[0], IssueNumber: strings.Split(filename, "#")[1], Image: ImageEntry{ThumbUrl: "tmpl/css/empty.jpg"}})
	// }
	//c = append(c, IssueEntry{Name: "test", IssueNumber: "5", Image: ImageEntry{ThumbUrl: ""}})

	p1 := &MainMenuPage{Comics: issues}

	templates.ExecuteTemplate(w, "sth2.html", p1)
	//fmt.Println("tutaj " + c)
}

func handler(w http.ResponseWriter, r *http.Request) {

	fmt.Println("hoł")
	title := r.FormValue("ftitle")
	//issue := r.FormValue("fissue")
	p1 := &Page{Title: "TestPage", Body: "", ImageUrl: ""}

	if title != "" {
		//url := SearchComic(title, issue)
		//p1 = &Page{Title: "TestPage", Body: "", ImageUrl: url.ApiDetailUrl}
	}

	templates.ExecuteTemplate(w, "sth.html", p1)
	//fmt.Println("tutaj " + c)
}
