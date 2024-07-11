package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

var jsonClient = &http.Client{Timeout: 10 * time.Second}

func getJson(url string, target interface{}) error {
	r, err := jsonClient.Get(url)
	if err != nil {
		return err
	}
	defer r.Body.Close()

	return json.NewDecoder(r.Body).Decode(target)
}

func SearchComic(issue *IssueEntry) {

	volsResponse := new(VolumesResponse)
	//volsUrl := "https://comicvine.gamespot.com/api/volumes/?api_key=38f4732067d47702b21621d27a828a5b7a51dde1&format=json&filter=name:" + name
	fmt.Println(issue.VolumeName + " " + url.PathEscape(issue.VolumeName))
	volsUrl := "https://comicvine.gamespot.com/api/search/?api_key=38f4732067d47702b21621d27a828a5b7a51dde1&format=json&resources=volume&query=" + url.PathEscape(issue.VolumeName)
	getJson(volsUrl, volsResponse)

	fmt.Printf("Found %v series", len(volsResponse.Results))

	singleVolUrl := volsResponse.Results[0].ApiDetailUrl + "?api_key=38f4732067d47702b21621d27a828a5b7a51dde1&format=json"
	singleVolResponse := new(VolumeResponse)
	getJson(singleVolUrl, singleVolResponse)
	for _, element := range singleVolResponse.Results.Issues {
		if element.IssueNumber == issue.IssueNumber {
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
			issue.ApiDetailUrl = issueResp.Results.ApiDetailUrl
			issue.Description = issueResp.Results.Description
			issue.Image = issueResp.Results.Image
			issue.ImageUri = issueResp.Results.Image.ThumbUrl
			issue.IssueNumber = issueResp.Results.IssueNumber
			issue.VolumeName = singleVolResponse.Results.Name
			issue.Name = issueResp.Results.Name
			issue.StoreDate = issueResp.Results.StoreDate
			issue.Processed = true
			//return issueResp.Results
		}
	}

}
