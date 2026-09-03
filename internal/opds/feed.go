// Package opds builds OPDS 1.2 catalog feeds (Atom XML) so external comic
// readers (Panels, Chunky, Moon+ Reader, Librera, KOReader, …) can browse
// and download the library. It knows nothing about HTTP or the database:
// callers assemble Feed values and hand them to Write.
package opds

import (
	"encoding/xml"
	"fmt"
	"io"
	"time"
)

// Media types used in OPDS link attributes and responses.
const (
	TypeNavigation  = "application/atom+xml;profile=opds-catalog;kind=navigation"
	TypeAcquisition = "application/atom+xml;profile=opds-catalog;kind=acquisition"
	TypeOpenSearch  = "application/opensearchdescription+xml"
	TypeAtomEntry   = "application/atom+xml;type=entry;profile=opds-catalog"

	TypeCBZ = "application/vnd.comicbook+zip"
	TypeCBR = "application/vnd.comicbook-rar"
	TypePDF = "application/pdf"
)

// Link relations.
const (
	RelSelf        = "self"
	RelStart       = "start"
	RelUp          = "up"
	RelNext        = "next"
	RelPrevious    = "previous"
	RelSearch      = "search"
	RelSubsection  = "subsection"
	RelNew         = "http://opds-spec.org/sort/new"
	RelAcquisition = "http://opds-spec.org/acquisition"
	RelImage       = "http://opds-spec.org/image"
	RelThumbnail   = "http://opds-spec.org/image/thumbnail"
)

// Feed is an OPDS catalog feed (navigation or acquisition — the difference is
// only in the entries and the media type the caller advertises).
type Feed struct {
	XMLName       xml.Name `xml:"feed"`
	Xmlns         string   `xml:"xmlns,attr"`
	XmlnsDC       string   `xml:"xmlns:dc,attr"`
	XmlnsOPDS     string   `xml:"xmlns:opds,attr"`
	XmlnsOpensrch string   `xml:"xmlns:opensearch,attr"`
	ID            string   `xml:"id"`
	Title         string   `xml:"title"`
	Updated       string   `xml:"updated"`
	Author        *Author  `xml:"author,omitempty"`
	Links         []Link   `xml:"link"`
	TotalResults  int      `xml:"opensearch:totalResults,omitempty"`
	ItemsPerPage  int      `xml:"opensearch:itemsPerPage,omitempty"`
	StartIndex    int      `xml:"opensearch:startIndex,omitempty"`
	Entries       []Entry  `xml:"entry"`
}

// Author is an Atom person construct.
type Author struct {
	Name string `xml:"name"`
	URI  string `xml:"uri,omitempty"`
}

// Link is an Atom link with the attributes OPDS cares about.
type Link struct {
	Rel   string `xml:"rel,attr,omitempty"`
	Href  string `xml:"href,attr"`
	Type  string `xml:"type,attr,omitempty"`
	Title string `xml:"title,attr,omitempty"`
}

// Text is an Atom text construct; Type is "text" or "html".
type Text struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

// Entry is a catalog entry: a navigation link (with a subsection link) or a
// publication (with acquisition and image links).
type Entry struct {
	ID        string   `xml:"id"`
	Title     string   `xml:"title"`
	Updated   string   `xml:"updated"`
	Authors   []Author `xml:"author"`
	Publisher string   `xml:"dc:publisher,omitempty"`
	Issued    string   `xml:"dc:issued,omitempty"`
	Language  string   `xml:"dc:language,omitempty"`
	Summary   *Text    `xml:"summary,omitempty"`
	Content   *Text    `xml:"content,omitempty"`
	Links     []Link   `xml:"link"`
}

// NewFeed returns a feed with the namespaces declared and the common
// self/start/search links attached.
func NewFeed(id, title string, updated time.Time) *Feed {
	return &Feed{
		Xmlns:         "http://www.w3.org/2005/Atom",
		XmlnsDC:       "http://purl.org/dc/terms/",
		XmlnsOPDS:     "http://opds-spec.org/2010/catalog",
		XmlnsOpensrch: "http://a9.com/-/spec/opensearch/1.1/",
		ID:            id,
		Title:         title,
		Updated:       FormatTime(updated),
		Author:        &Author{Name: "ComicNest"},
	}
}

// AddLink appends a link to the feed.
func (f *Feed) AddLink(rel, href, typ string) {
	f.Links = append(f.Links, Link{Rel: rel, Href: href, Type: typ})
}

// Write serializes the feed as an XML document.
func (f *Feed) Write(w io.Writer) error {
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(f); err != nil {
		return fmt.Errorf("opds: encoding feed: %w", err)
	}
	return enc.Flush()
}

// OpenSearchDescription describes the catalog's search endpoint; readers
// fetch it via the feed's "search" link.
type OpenSearchDescription struct {
	XMLName     xml.Name `xml:"OpenSearchDescription"`
	Xmlns       string   `xml:"xmlns,attr"`
	ShortName   string   `xml:"ShortName"`
	Description string   `xml:"Description"`
	InputEnc    string   `xml:"InputEncoding"`
	OutputEnc   string   `xml:"OutputEncoding"`
	URL         struct {
		Type     string `xml:"type,attr"`
		Template string `xml:"template,attr"`
	} `xml:"Url"`
}

// WriteOpenSearch writes an OpenSearch description whose template points at
// searchURL, which must contain the literal placeholder "{searchTerms}".
func WriteOpenSearch(w io.Writer, searchURL string) error {
	d := OpenSearchDescription{
		Xmlns:       "http://a9.com/-/spec/opensearch/1.1/",
		ShortName:   "ComicNest",
		Description: "Wyszukiwanie serii i zeszytów w bibliotece ComicNest",
		InputEnc:    "UTF-8",
		OutputEnc:   "UTF-8",
	}
	d.URL.Type = TypeAcquisition
	d.URL.Template = searchURL

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(d); err != nil {
		return fmt.Errorf("opds: encoding opensearch: %w", err)
	}
	return enc.Flush()
}

// FormatTime renders a time in the RFC 3339 form Atom requires.
func FormatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// ParseDBTime converts SQLite's datetime('now') text ("2006-01-02 15:04:05",
// UTC) to a time; unparsable input yields the fallback.
func ParseDBTime(s string, fallback time.Time) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		return fallback
	}
	return t.UTC()
}
