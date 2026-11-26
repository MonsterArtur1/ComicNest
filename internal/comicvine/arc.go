package comicvine

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// issueBatchSize is how many issue ids one filtered /issues/ request may name
// (ComicVine caps a page at 100 results).
const issueBatchSize = 100

// Credit is one entry of an issue's character_credits / story_arc_credits.
type Credit struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// cleanCredits drops nameless entries and duplicates (same id, or same
// case-insensitive name when there is no id), keeping ComicVine's order.
func cleanCredits(in []Credit) []Credit {
	var out []Credit
	seenID := make(map[int]bool)
	seenName := make(map[string]bool)
	for _, c := range in {
		c.Name = strings.TrimSpace(c.Name)
		if c.Name == "" {
			continue
		}
		key := strings.ToLower(c.Name)
		if (c.ID != 0 && seenID[c.ID]) || (c.ID == 0 && seenName[key]) {
			continue
		}
		seenID[c.ID] = true
		seenName[key] = true
		out = append(out, c)
	}
	return out
}

// ArcIssue is one entry of a story arc's issue list.
type ArcIssue struct {
	ID          int
	Name        string
	IssueNumber string
	VolumeID    int
	VolumeName  string
	CoverDate   string // "2006-01-02" or ""; the store date when there is no cover date
}

// StoryArc is a ComicVine story arc with its issue list in ComicVine's order.
type StoryArc struct {
	ID          int
	Name        string
	Description string // plain text (HTML stripped)
	Publisher   string
	ImageURL    string
	URL         string
	Issues      []ArcIssue

	// EnrichErr is set when the follow-up request that fills in cover dates and
	// missing volume/number details failed. The arc itself is still usable.
	EnrichErr error
}

// arcIssueRaw is the wire format of one issue of a story arc (or of a
// filtered /issues/ result).
type arcIssueRaw struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	IssueNumber string `json:"issue_number"`
	CoverDate   string `json:"cover_date"`
	StoreDate   string `json:"store_date"`
	Volume      *struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"volume"`
}

func (r arcIssueRaw) toArcIssue() ArcIssue {
	a := ArcIssue{ID: r.ID, Name: r.Name, IssueNumber: r.IssueNumber, CoverDate: normalizeDate(r.CoverDate)}
	if a.CoverDate == "" {
		a.CoverDate = normalizeDate(r.StoreDate)
	}
	if r.Volume != nil {
		a.VolumeID, a.VolumeName = r.Volume.ID, r.Volume.Name
	}
	return a
}

// storyArcRaw is the wire format of a story_arc result.
type storyArcRaw struct {
	ID            int             `json:"id"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Image         *imageField     `json:"image"`
	Publisher     *publisherField `json:"publisher"`
	Issues        []arcIssueRaw   `json:"issues"`
	SiteDetailURL string          `json:"site_detail_url"`
}

// GetStoryArc fetches a story arc and its issue list. ComicVine's arc issue
// stubs are thin (and sometimes lack volume/number), so the list is completed
// with one filtered /issues/ request per 100 issues for the cover dates and
// whatever the stubs left empty; a failure there leaves the stub data and is
// reported in EnrichErr.
func (c *Client) GetStoryArc(id int) (*StoryArc, error) {
	if !c.Enabled() {
		return nil, ErrNoKey
	}

	params := url.Values{}
	params.Set("field_list", "id,name,description,image,publisher,issues,site_detail_url")

	raw, err := c.doRequest(fmt.Sprintf("/story_arc/4045-%d/", id), params)
	if err != nil {
		return nil, err
	}
	var item storyArcRaw
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, fmt.Errorf("comicvine: decode story arc result: %w", err)
	}

	arc := &StoryArc{
		ID:          item.ID,
		Name:        item.Name,
		Description: StripHTML(item.Description),
		URL:         item.SiteDetailURL,
	}
	if item.Publisher != nil {
		arc.Publisher = item.Publisher.Name
	}
	if item.Image != nil {
		if item.Image.MediumURL != "" {
			arc.ImageURL = item.Image.MediumURL
		} else {
			arc.ImageURL = item.Image.SmallURL
		}
	}

	ids := make([]int, 0, len(item.Issues))
	seen := make(map[int]bool)
	for _, it := range item.Issues {
		if it.ID == 0 || seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		ids = append(ids, it.ID)
		arc.Issues = append(arc.Issues, it.toArcIssue())
	}

	if len(ids) > 0 {
		details, err := c.GetIssuesBrief(ids)
		if err != nil {
			arc.EnrichErr = err
			return arc, nil
		}
		for i, is := range arc.Issues {
			d, ok := details[is.ID]
			if !ok {
				continue
			}
			if is.IssueNumber == "" {
				arc.Issues[i].IssueNumber = d.IssueNumber
			}
			if is.Name == "" {
				arc.Issues[i].Name = d.Name
			}
			if is.VolumeName == "" {
				arc.Issues[i].VolumeID, arc.Issues[i].VolumeName = d.VolumeID, d.VolumeName
			}
			if d.CoverDate != "" {
				arc.Issues[i].CoverDate = d.CoverDate
			}
		}
	}
	return arc, nil
}

// GetIssuesBrief fetches id, name, number, dates and volume of many issues
// at once (a filtered /issues/ list, 100 per request). Ids ComicVine does not
// know are simply absent from the result.
func (c *Client) GetIssuesBrief(ids []int) (map[int]ArcIssue, error) {
	if !c.Enabled() {
		return nil, ErrNoKey
	}
	out := make(map[int]ArcIssue, len(ids))
	for start := 0; start < len(ids); start += issueBatchSize {
		chunk := ids[start:min(start+issueBatchSize, len(ids))]
		parts := make([]string, len(chunk))
		for i, id := range chunk {
			parts[i] = strconv.Itoa(id)
		}
		params := url.Values{}
		params.Set("field_list", "id,name,issue_number,cover_date,store_date,volume")
		params.Set("filter", "id:"+strings.Join(parts, "|"))
		params.Set("limit", strconv.Itoa(issueBatchSize))

		raw, err := c.doRequest("/issues/", params)
		if err != nil {
			return nil, err
		}
		var items []arcIssueRaw
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("comicvine: decode issue list: %w", err)
		}
		for _, it := range items {
			out[it.ID] = it.toArcIssue()
		}
	}
	return out, nil
}
