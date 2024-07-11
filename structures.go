package main

type Page struct {
	Title    string
	Body     string
	ImageUrl string
}
type MainMenuPage struct {
	Comics []IssueEntry
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
	Id           int        `json:"id"`
	VolumeName   string     `json:"volume_name"`
	Name         string     `json:"name"`
	ApiDetailUrl string     `json:"api_detail_url"`
	IssueNumber  string     `json:"issue_number"`
	Image        ImageEntry `json:"image"`
	Description  string     `json:"description"`
	StoreDate    string     `json:"store_date"`
	Path         string     `json:"path"`
	ImageUri     string     `json:"image_uri"`
	Processed    bool       `json:"processed"`
	DiskSize     string     `json:"disk_size"`
}
