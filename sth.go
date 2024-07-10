package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var templates = template.Must(template.ParseFiles("tmpl/sth.html", "tmpl/sth2.html"))

const dbName = "database.sqlite"

var jsonClient = &http.Client{Timeout: 10 * time.Second}

func main() {
	if _, err := os.Stat(dbName); errors.Is(err, os.ErrNotExist) {
		CreateDatabase()
	}
	ScanDir("D:\\Library")

	fmt.Println("server started")
	http.Handle("/tmpl/css/", http.StripPrefix("/tmpl/css", http.FileServer(http.Dir("./tmpl/css"))))
	http.HandleFunc("/", mainMenuHandler)
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func getJson(url string, target interface{}) error {
	r, err := jsonClient.Get(url)
	if err != nil {
		return err
	}
	defer r.Body.Close()

	return json.NewDecoder(r.Body).Decode(target)
}

func mainMenuHandler(w http.ResponseWriter, r *http.Request) {

	fmt.Println("hoł1")
	filenames := SelectFromDb()
	var c []IssueEntry
	for _, filename := range filenames {
		c = append(c, SearchComic(strings.Split(filename, "#")[0], strings.Split(filename, "#")[1]))
	}
	//c = append(c, IssueEntry{Name: "test", IssueNumber: "5", Image: ImageEntry{ThumbUrl: ""}})

	p1 := &MainMenuPage{Comics: c}

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

type Page struct {
	Title    string
	Body     string
	ImageUrl string
}
type MainMenuPage struct {
	Comics []IssueEntry
}

func CreateDatabase() {
	db, err := sql.Open("sqlite3", dbName)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	sqlStmt := `
	create table foo (id integer not null primary key, path text, processed bool, title text,  UNIQUE(path));
	delete from foo;
	`
	_, err = db.Exec(sqlStmt)
	if err != nil {
		log.Printf("%q: %s\n", err, sqlStmt)
		return
	}
	db.Close()
}

func SelectFromDb() []string {
	db, _ := sql.Open("sqlite3", dbName)
	response, err := db.Query("SELECT path from foo")
	if err != nil {
		log.Fatal(err)
	}
	defer response.Close()
	var paths []string
	for response.Next() {
		var path string
		_ = response.Scan(&path)
		paths = append(paths, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	}
	db.Close()
	return paths
}
func InsertToDb(name string) {
	db, _ := sql.Open("sqlite3", dbName)
	tx, err := db.Begin()
	if err != nil {
		log.Fatal(err)
	}
	stmt, err := tx.Prepare("insert or ignore into foo(id, path, processed, title) values(NULL, ?, false,'')")
	if err != nil {
		log.Fatal(err)
	}
	defer stmt.Close()

	_, err = stmt.Exec(name)
	if err != nil {
		log.Fatal(err)
	}
	err = tx.Commit()
	if err != nil {
		log.Fatal(err)
	}
	db.Close()
}

func ScanDir(dir string) {
	err := filepath.WalkDir(dir,
		func(path string, info fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			ext := filepath.Ext(info.Name())
			if ext == ".pdf" || ext == ".cbz" || ext == ".cbr" {
				InsertToDb(path)
			}
			return nil
		})
	if err != nil {
		log.Println(err)
	}
}

func SearchComic(name string, issueId string) IssueEntry {

	volsResponse := new(VolumesResponse)
	//volsUrl := "https://comicvine.gamespot.com/api/volumes/?api_key=38f4732067d47702b21621d27a828a5b7a51dde1&format=json&filter=name:" + name
	fmt.Println(name + " " + url.PathEscape(name))
	volsUrl := "https://comicvine.gamespot.com/api/search/?api_key=38f4732067d47702b21621d27a828a5b7a51dde1&format=json&resources=volume&query=" + url.PathEscape(name)
	getJson(volsUrl, volsResponse)

	fmt.Printf("Found %v series", len(volsResponse.Results))

	singleVolUrl := volsResponse.Results[0].ApiDetailUrl + "?api_key=38f4732067d47702b21621d27a828a5b7a51dde1&format=json"
	singleVolResponse := new(VolumeResponse)
	getJson(singleVolUrl, singleVolResponse)
	for _, element := range singleVolResponse.Results.Issues {
		if element.IssueNumber == issueId {
			issueRespUrl := element.ApiDetailUrl + "?api_key=38f4732067d47702b21621d27a828a5b7a51dde1&format=json"
			issueResp := new(IssueResponse)
			getJson(issueRespUrl, issueResp)
			fmt.Println("----- " + singleVolResponse.Results.Name)
			fmt.Println("----- " + singleVolResponse.Results.ApiDetailUrl)

			/*			fmt.Println("kurde na bank nie " + issueResp.Results.Name)
						fmt.Println("kurde na bank nie " + issueResp.Results.IssueNumber)
						fmt.Println("kurde na bank nie " + issueResp.Results.Image.SmallUrl)
						fmt.Println("kurde na bank nie " + issueResp.Results.StoreDate)
						fmt.Println("kurde na bank nie " + issueResp.Results.Description)*/
			return issueResp.Results
		}
	}

	var i IssueEntry
	return i
}

type VolumesResponse struct {
	Results    []VolumeEntry `json:"results"`
	StatusCode int           `json:"status_code"`
}
type VolumeResponse struct {
	Results    VolumeEntry `json:"results"`
	StatusCode int         `json:"status_code"`
}
type IssueResponse struct {
	Results    IssueEntry `json:"results"`
	StatusCode int        `json:"status_code"`
}

type ImageEntry struct {
	OriginalUrl string `json:"original_url"`
	SmallUrl    string `json:"small_url"`
	ThumbUrl    string `json:"thumb_url"`
}
type VolumeEntry struct {
	ApiDetailUrl  string       `json:"api_detail_url"`
	Image         ImageEntry   `json:"image"`
	Issues        []IssueEntry `json:"issues"`
	Name          string       `json:"name"`
	CountOfIssues int          `json:"count_of_issues"`
}

type IssueEntry struct {
	Name         string     `json:"name"`
	ApiDetailUrl string     `json:"api_detail_url"`
	IssueNumber  string     `json:"issue_number"`
	Image        ImageEntry `json:"image"`
	Description  string     `json:"description"`
	StoreDate    string     `json:"store_date"`
}
