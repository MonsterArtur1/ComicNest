package opds

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

func TestFeedWriteIsWellFormedAtom(t *testing.T) {
	updated := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	f := NewFeed("urn:comicnest:root", "ComicNest", updated)
	f.AddLink(RelSelf, "http://host/opds", TypeNavigation)
	f.Entries = append(f.Entries, Entry{
		ID:        "urn:comicnest:issue:7",
		Title:     "Saga #55 – Chapter Fifty-Five",
		Updated:   FormatTime(updated),
		Authors:   []Author{{Name: "Brian K. Vaughan"}},
		Publisher: "Image",
		Issued:    "2020-09-01",
		Summary:   &Text{Type: "text", Value: "Opis z <b>tagiem</b> & znakiem"},
		Links: []Link{
			{Rel: RelAcquisition, Href: "http://host/opds/issues/7/file", Type: TypeCBZ},
			{Rel: RelThumbnail, Href: "http://host/opds/issues/7/cover", Type: "image/jpeg"},
		},
	})

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := buf.String()

	// Must parse back as XML (escaping of & and < in the summary included).
	dec := xml.NewDecoder(strings.NewReader(out))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("output is not well-formed XML: %v\n%s", err, out)
		}
	}

	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		`xmlns="http://www.w3.org/2005/Atom"`,
		`xmlns:dc="http://purl.org/dc/terms/"`,
		`xmlns:opds="http://opds-spec.org/2010/catalog"`,
		`<updated>2026-09-03T12:00:00Z</updated>`,
		`<dc:publisher>Image</dc:publisher>`,
		`<dc:issued>2020-09-01</dc:issued>`,
		`rel="http://opds-spec.org/acquisition"`,
		`type="application/vnd.comicbook+zip"`,
		`<summary type="text">Opis z &lt;b&gt;tagiem&lt;/b&gt; &amp; znakiem</summary>`,
		`<name>Brian K. Vaughan</name>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("feed missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "<dc:language>") {
		t.Errorf("empty optional element should be omitted:\n%s", out)
	}
}

func TestWriteOpenSearch(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteOpenSearch(&buf, "http://host/opds/search?q={searchTerms}"); err != nil {
		t.Fatalf("WriteOpenSearch: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		`<OpenSearchDescription xmlns="http://a9.com/-/spec/opensearch/1.1/">`,
		`template="http://host/opds/search?q={searchTerms}"`,
		`type="` + TypeAcquisition + `"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("opensearch missing %q\n%s", want, out)
		}
	}
}

func TestParseDBTime(t *testing.T) {
	fallback := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	got := ParseDBTime("2026-09-03 08:15:30", fallback)
	if got.Format(time.RFC3339) != "2026-09-03T08:15:30Z" {
		t.Errorf("ParseDBTime = %v", got)
	}
	if ParseDBTime("garbage", fallback) != fallback {
		t.Errorf("unparsable input should return fallback")
	}
}
