// Package comicvine provides a minimal client for the ComicVine API,
// used to look up comic volumes/issues and fetch cover images.
package comicvine

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ErrNoKey is returned by every call when the client has no API key.
var ErrNoKey = errors.New("comicvine: no API key configured")

// defaultBaseURL is the production ComicVine API endpoint.
const defaultBaseURL = "https://comicvine.gamespot.com/api"

// maxJSONBody caps how many bytes of a JSON API response we will read.
const maxJSONBody = 10 << 20 // 10 MB

// maxImageBody caps how many bytes of a cover image we will read.
const maxImageBody = 20 << 20 // 20 MB

// userAgent is sent on every request; ComicVine rejects requests with an
// empty or default Go User-Agent.
const userAgent = "ComicNest/1.0"

// Client is a minimal ComicVine API client with built-in rate limiting.
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client

	mu          sync.Mutex
	lastRequest time.Time
	minInterval time.Duration
}

// New returns a client; empty key yields a disabled client (Enabled() == false).
func New(apiKey string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: defaultBaseURL,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
		minInterval: time.Second,
	}
}

// Enabled reports whether an API key is configured.
func (c *Client) Enabled() bool {
	return c.apiKey != ""
}

// throttle blocks, if necessary, until at least minInterval has elapsed
// since the previous request. The very first request is never delayed.
func (c *Client) throttle() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.lastRequest.IsZero() {
		if elapsed := time.Since(c.lastRequest); elapsed < c.minInterval {
			time.Sleep(c.minInterval - elapsed)
		}
	}
	c.lastRequest = time.Now()
}

// apiResponse is the common envelope wrapping every ComicVine API response.
type apiResponse struct {
	Error      string          `json:"error"`
	StatusCode int             `json:"status_code"`
	Results    json.RawMessage `json:"results"`
}

// imageField mirrors the "image" object ComicVine embeds in volumes/issues.
type imageField struct {
	SmallURL  string `json:"small_url"`
	MediumURL string `json:"medium_url"`
}

// publisherField mirrors the "publisher" object embedded in volumes.
type publisherField struct {
	Name string `json:"name"`
}

// volumeRaw is the wire format for a volume result.
type volumeRaw struct {
	ID            int              `json:"id"`
	Name          string           `json:"name"`
	StartYear     string           `json:"start_year"`
	Publisher     *publisherField  `json:"publisher"`
	CountOfIssues int              `json:"count_of_issues"`
	Description   string           `json:"description"`
	Image         *imageField      `json:"image"`
	Issues        []volumeIssueRaw `json:"issues"`
}

// volumeIssueRaw is the wire format for one entry of a volume's issue list.
type volumeIssueRaw struct {
	ID          int    `json:"id"`
	IssueNumber string `json:"issue_number"`
	Name        string `json:"name"`
}

// personCredit is the wire format for one entry of an issue's person_credits.
type personCredit struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// issueRaw is the wire format for an issue result.
type issueRaw struct {
	ID            int            `json:"id"`
	Name          string         `json:"name"`
	IssueNumber   string         `json:"issue_number"`
	CoverDate     string         `json:"cover_date"`
	StoreDate     string         `json:"store_date"`
	Description   string         `json:"description"`
	Image         *imageField    `json:"image"`
	PersonCredits []personCredit `json:"person_credits"`
}

// Volume is a ComicVine volume (roughly: a comic series/run).
type Volume struct {
	ID            int
	Name          string
	StartYear     string
	Publisher     string
	CountOfIssues int
	Description   string // plain text (HTML stripped)
	ImageURL      string // small cover URL, may be empty
}

// VolumeIssue is one entry of a volume's issue list.
type VolumeIssue struct {
	ID          int
	IssueNumber string
	Name        string
}

// Issue is a single ComicVine issue with full metadata.
type Issue struct {
	ID          int
	Name        string
	IssueNumber string
	CoverDate   string // "2006-01-02" or ""
	StoreDate   string
	Description string // plain text (HTML stripped)
	Writers     string // comma-joined person names with a writer role
	Artists     string // comma-joined names with penciler/artist/inker role
	ImageURL    string // medium cover URL preferred, small as fallback
}

// volumeFromRaw converts the wire format into the public Volume type.
func volumeFromRaw(v volumeRaw) Volume {
	vol := Volume{
		ID:            v.ID,
		Name:          v.Name,
		StartYear:     v.StartYear,
		CountOfIssues: v.CountOfIssues,
		Description:   StripHTML(v.Description),
	}
	if v.Publisher != nil {
		vol.Publisher = v.Publisher.Name
	}
	if v.Image != nil {
		vol.ImageURL = v.Image.SmallURL
	}
	return vol
}

// normalizeDate trims a ComicVine timestamp such as "2006-01-02 00:00:00"
// down to the date-only portion.
func normalizeDate(s string) string {
	if idx := strings.IndexByte(s, ' '); idx >= 0 {
		return s[:idx]
	}
	return s
}

// creditsFromPersons splits person_credits into deduplicated, order-preserving
// comma-joined lists of writers and artists.
func creditsFromPersons(people []personCredit) (writers, artists string) {
	var writerNames, artistNames []string
	seenWriter := make(map[string]bool)
	seenArtist := make(map[string]bool)

	for _, p := range people {
		role := strings.ToLower(p.Role)

		if strings.Contains(role, "writer") && !seenWriter[p.Name] {
			seenWriter[p.Name] = true
			writerNames = append(writerNames, p.Name)
		}

		isArtist := strings.Contains(role, "penciler") ||
			strings.Contains(role, "penciller") ||
			strings.Contains(role, "artist") ||
			strings.Contains(role, "inker")
		if isArtist && !seenArtist[p.Name] {
			seenArtist[p.Name] = true
			artistNames = append(artistNames, p.Name)
		}
	}

	return strings.Join(writerNames, ", "), strings.Join(artistNames, ", ")
}

// doRequest performs a rate-limited GET against the ComicVine API and
// returns the raw "results" payload once the envelope has been validated.
func (c *Client) doRequest(path string, params url.Values) (json.RawMessage, error) {
	if !c.Enabled() {
		return nil, ErrNoKey
	}

	if params == nil {
		params = url.Values{}
	}
	params.Set("api_key", c.apiKey)
	params.Set("format", "json")

	reqURL := strings.TrimRight(c.baseURL, "/") + path + "?" + params.Encode()

	c.throttle()

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("comicvine: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("comicvine: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody))
	if err != nil {
		return nil, fmt.Errorf("comicvine: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("comicvine: unexpected HTTP status %d", resp.StatusCode)
	}

	var envelope apiResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("comicvine: decode response: %w", err)
	}
	if envelope.StatusCode != 1 {
		return nil, fmt.Errorf("comicvine: api error: %s", envelope.Error)
	}

	return envelope.Results, nil
}

// SearchVolumes queries volumes by name (max ~20 results).
func (c *Client) SearchVolumes(query string) ([]Volume, error) {
	if !c.Enabled() {
		return nil, ErrNoKey
	}

	params := url.Values{}
	params.Set("resources", "volume")
	params.Set("query", query)
	params.Set("limit", "20")
	params.Set("field_list", "id,name,start_year,publisher,count_of_issues,description,image")

	raw, err := c.doRequest("/search/", params)
	if err != nil {
		return nil, err
	}

	var items []volumeRaw
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("comicvine: decode search results: %w", err)
	}

	volumes := make([]Volume, 0, len(items))
	for _, it := range items {
		volumes = append(volumes, volumeFromRaw(it))
	}
	return volumes, nil
}

// GetVolume fetches one volume and its issue list.
func (c *Client) GetVolume(id int) (*Volume, []VolumeIssue, error) {
	if !c.Enabled() {
		return nil, nil, ErrNoKey
	}

	params := url.Values{}
	params.Set("field_list", "id,name,start_year,publisher,count_of_issues,description,image,issues")

	raw, err := c.doRequest(fmt.Sprintf("/volume/4050-%d/", id), params)
	if err != nil {
		return nil, nil, err
	}

	var item volumeRaw
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, nil, fmt.Errorf("comicvine: decode volume result: %w", err)
	}

	vol := volumeFromRaw(item)

	issues := make([]VolumeIssue, 0, len(item.Issues))
	for _, is := range item.Issues {
		issues = append(issues, VolumeIssue{
			ID:          is.ID,
			IssueNumber: is.IssueNumber,
			Name:        is.Name,
		})
	}

	return &vol, issues, nil
}

// GetIssue fetches full metadata of one issue.
func (c *Client) GetIssue(id int) (*Issue, error) {
	if !c.Enabled() {
		return nil, ErrNoKey
	}

	params := url.Values{}
	params.Set("field_list", "id,name,issue_number,cover_date,store_date,description,image,person_credits")

	raw, err := c.doRequest(fmt.Sprintf("/issue/4000-%d/", id), params)
	if err != nil {
		return nil, err
	}

	var item issueRaw
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, fmt.Errorf("comicvine: decode issue result: %w", err)
	}

	issue := &Issue{
		ID:          item.ID,
		Name:        item.Name,
		IssueNumber: item.IssueNumber,
		CoverDate:   normalizeDate(item.CoverDate),
		StoreDate:   normalizeDate(item.StoreDate),
		Description: StripHTML(item.Description),
	}

	if item.Image != nil {
		if item.Image.MediumURL != "" {
			issue.ImageURL = item.Image.MediumURL
		} else {
			issue.ImageURL = item.Image.SmallURL
		}
	}

	issue.Writers, issue.Artists = creditsFromPersons(item.PersonCredits)

	return issue, nil
}

// DownloadImage fetches a cover image (20 MB limit).
func (c *Client) DownloadImage(imgURL string) ([]byte, error) {
	if !c.Enabled() {
		return nil, ErrNoKey
	}

	c.throttle()

	req, err := http.NewRequest(http.MethodGet, imgURL, nil)
	if err != nil {
		return nil, fmt.Errorf("comicvine: build image request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("comicvine: image request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("comicvine: unexpected HTTP status %d for image", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") {
		return nil, fmt.Errorf("comicvine: unexpected content type %q for image", contentType)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBody))
	if err != nil {
		return nil, fmt.Errorf("comicvine: read image: %w", err)
	}

	return data, nil
}

// reBreakTags matches HTML constructs that should become a newline instead
// of being silently dropped, so paragraphs/lines don't run together.
var reBreakTags = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</li>`)

// reAnyTag matches any remaining HTML tag.
var reAnyTag = regexp.MustCompile(`(?s)<[^>]*>`)

// reExtraNewlines collapses runs of 3+ newlines down to two.
var reExtraNewlines = regexp.MustCompile(`\n{3,}`)

// StripHTML reduces an HTML fragment to readable plain text.
func StripHTML(s string) string {
	if s == "" {
		return ""
	}

	s = reBreakTags.ReplaceAllString(s, "\n")
	s = reAnyTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reExtraNewlines.ReplaceAllString(s, "\n\n")

	return strings.TrimSpace(s)
}
