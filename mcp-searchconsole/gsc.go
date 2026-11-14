package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/homielab/homielab-mcp/internal/googleapi"
)

var apiBase = "https://www.googleapis.com/webmasters/v3"

var inspectPath = "https://searchconsole.googleapis.com/v1/urlInspection/index:inspect"

type gscClient struct {
	*googleapi.Client
}

func newGSCClient(ctx context.Context) (*gscClient, error) {
	hc, err := auth.HTTPClient(ctx)
	if err != nil {
		return nil, err
	}
	return &gscClient{googleapi.New(hc, "Search Console API")}, nil
}

// encodeSiteURL encodes a property URL for use as a path segment:
// "https://www.example.com/" → "https%3A%2F%2Fwww.example.com%2F",
// "sc-domain:example.com" stays readable (":" is legal in a path segment).
func encodeSiteURL(site string) string {
	return url.PathEscape(site)
}

// ---- resources ----

// flexInt decodes an int64 the API sends either as a number or, per the
// proto3 JSON mapping, as a string ("12").
type flexInt int64

func (n *flexInt) UnmarshalJSON(b []byte) error {
	v, err := strconv.ParseInt(strings.Trim(string(b), `"`), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid integer %s", b)
	}
	*n = flexInt(v)
	return nil
}

type siteEntry struct {
	SiteURL         string `json:"siteUrl"`
	PermissionLevel string `json:"permissionLevel"`
}

type sitemapContent struct {
	Type      string  `json:"type"`
	Submitted flexInt `json:"submitted"`
	Indexed   flexInt `json:"indexed"`
}

type sitemapEntry struct {
	Path              string           `json:"path"`
	LastSubmitted     string           `json:"lastSubmitted"`
	IsSitemapsIndexed bool             `json:"isSitemapsIndexed"`
	Type              string           `json:"type"`
	Warnings          flexInt          `json:"warnings"`
	Errors            flexInt          `json:"errors"`
	IsPending         bool             `json:"isPending"`
	Contents          []sitemapContent `json:"contents"`
}

type analyticsRow struct {
	Keys        []string `json:"keys"`
	Clicks      float64  `json:"clicks"`
	Impressions float64  `json:"impressions"`
	Ctr         float64  `json:"ctr"`
	Position    float64  `json:"position"`
}

type analyticsResponse struct {
	Rows                    []analyticsRow `json:"rows"`
	ResponseAggregationType string         `json:"responseAggregationType"`
}

// inspectionResult mirrors the documented UrlInspectionResult; fields the API
// omits simply stay empty and are skipped in rendering.
type inspectionResult struct {
	InspectionResultLink string `json:"inspectionResultLink"`
	IndexStatusResult    *struct {
		Verdict                      string   `json:"verdict"`
		CoverageState                string   `json:"coverageState"`
		RobotsTxtState               string   `json:"robotsTxtState"`
		IndexingState                string   `json:"indexingState"`
		LastCrawlTime                string   `json:"lastCrawlTime"`
		PageFetchState               string   `json:"pageFetchState"`
		GoogleCanonical              string   `json:"googleCanonical"`
		UserCanonical                string   `json:"userCanonical"`
		Sitemap                      []string `json:"sitemap"`
		ReferringRobotsTxtUserAgents []string `json:"referringRobotsTxtUserAgents"`
		CrawledAs                    string   `json:"crawledAs"`
	} `json:"indexStatusResult"`
	AmpResult *struct {
		Verdict               string `json:"verdict"`
		AmpURL                string `json:"ampUrl"`
		AMPIndexStatusVerdict string `json:"ampIndexStatusVerdict"`
		LastCrawlTime         string `json:"lastCrawlTime"`
	} `json:"ampResult"`
	MobileUsabilityResult *struct {
		Verdict string `json:"verdict"`
		Issues  []struct {
			IssueType string `json:"issueType"`
		} `json:"issues"`
	} `json:"mobileUsabilityResult"`
	RichResultsResult *struct {
		Verdict       string `json:"verdict"`
		DetectedItems []struct {
			RichResultType string `json:"richResultType"`
			Items          []struct {
				Name   string `json:"name"`
				Issues []struct {
					IssueMessage string `json:"issueMessage"`
					Severity     string `json:"severity"`
				} `json:"issues"`
			} `json:"items"`
		} `json:"detectedItems"`
	} `json:"richResultsResult"`
}

// ---- API calls ----

func (c *gscClient) listSites(ctx context.Context) ([]siteEntry, error) {
	body, _, err := c.Do(ctx, http.MethodGet, googleapi.JoinURL(apiBase, "/sites"), nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		SiteEntry []siteEntry `json:"siteEntry"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out.SiteEntry, nil
}

// listSitemaps lists the property's sitemaps, or the children of
// sitemapIndex when it is set.
func (c *gscClient) listSitemaps(ctx context.Context, site, sitemapIndex string) ([]sitemapEntry, error) {
	u := googleapi.JoinURL(apiBase, "/sites/"+encodeSiteURL(site)+"/sitemaps")
	if sitemapIndex != "" {
		u += "?" + url.Values{"sitemapIndex": {sitemapIndex}}.Encode()
	}
	body, _, err := c.Do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Sitemap []sitemapEntry `json:"sitemap"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out.Sitemap, nil
}

func (c *gscClient) getSitemap(ctx context.Context, site, feedpath string) (sitemapEntry, error) {
	var out sitemapEntry
	body, _, err := c.Do(ctx, http.MethodGet, googleapi.JoinURL(apiBase, "/sites/"+encodeSiteURL(site)+"/sitemaps/"+encodeSiteURL(feedpath)), nil)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(body, &out)
	return out, err
}

// analyticsQuery posts a Search Analytics query and returns the parsed rows.
func (c *gscClient) analyticsQuery(ctx context.Context, site string, q analyticsQueryBody) (analyticsResponse, error) {
	var out analyticsResponse
	body, _, err := c.Do(ctx, http.MethodPost, googleapi.JoinURL(apiBase, "/sites/"+encodeSiteURL(site)+"/searchAnalytics/query"), q)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(body, &out)
	return out, err
}

type analyticsFilter struct {
	Dimension  string `json:"dimension"`
	Expression string `json:"expression"`
	Operator   string `json:"operator,omitempty"`
}

type analyticsQueryBody struct {
	StartDate             string                 `json:"startDate"`
	EndDate               string                 `json:"endDate"`
	Dimensions            []string               `json:"dimensions,omitempty"`
	DimensionFilterGroups []dimensionFilterGroup `json:"dimensionFilterGroups,omitempty"`
	Type                  string                 `json:"type,omitempty"`
	AggregationType       string                 `json:"aggregationType,omitempty"`
	RowLimit              int                    `json:"rowLimit,omitempty"`
	StartRow              int                    `json:"startRow,omitempty"`
	DataState             string                 `json:"dataState,omitempty"`
}

type dimensionFilterGroup struct {
	GroupType string            `json:"groupType"`
	Filters   []analyticsFilter `json:"filters"`
}

// urlInspect calls the URL Inspection API (separate v1 base path).
func (c *gscClient) urlInspect(ctx context.Context, site, inspectionURL, languageCode string) (inspectionResult, error) {
	var out struct {
		Result inspectionResult `json:"inspectionResult"`
	}
	body := map[string]string{
		"inspectionUrl": inspectionURL,
		"siteUrl":       site,
	}
	if languageCode != "" {
		body["languageCode"] = languageCode
	}
	data, _, err := c.Do(ctx, http.MethodPost, inspectPath, body)
	if err != nil {
		return out.Result, err
	}
	err = json.Unmarshal(data, &out)
	return out.Result, err
}

// ---- writes (scope webmasters) ----

// addSite adds a property to the account; it stays unverified until
// ownership is verified.
func (c *gscClient) addSite(ctx context.Context, site string) error {
	_, _, err := c.Do(ctx, http.MethodPut, googleapi.JoinURL(apiBase, "/sites/"+encodeSiteURL(site)), nil)
	return err
}

// deleteSite removes a property from the account.
func (c *gscClient) deleteSite(ctx context.Context, site string) error {
	_, _, err := c.Do(ctx, http.MethodDelete, googleapi.JoinURL(apiBase, "/sites/"+encodeSiteURL(site)), nil)
	return err
}

func (c *gscClient) submitSitemap(ctx context.Context, site, feedpath string) error {
	_, _, err := c.Do(ctx, http.MethodPut, googleapi.JoinURL(apiBase, "/sites/"+encodeSiteURL(site)+"/sitemaps/"+encodeSiteURL(feedpath)), nil)
	return err
}

func (c *gscClient) deleteSitemap(ctx context.Context, site, feedpath string) error {
	_, _, err := c.Do(ctx, http.MethodDelete, googleapi.JoinURL(apiBase, "/sites/"+encodeSiteURL(site)+"/sitemaps/"+encodeSiteURL(feedpath)), nil)
	return err
}

// ---- site resolution ----

// resolveSite maps user input ("example.com", "https://example.com",
// "sc-domain:example.com", or an exact property URL) to a real property in
// the account. Empty input picks the only property, or errors with the list.
func (c *gscClient) resolveSite(ctx context.Context, site string) (string, error) {
	sites, err := c.listSites(ctx)
	if err != nil {
		return "", err
	}
	if len(sites) == 0 {
		return "", fmt.Errorf("no Search Console properties accessible for this Google login")
	}
	if strings.TrimSpace(site) == "" {
		if len(sites) == 1 {
			return sites[0].SiteURL, nil
		}
		return "", fmt.Errorf("%d properties accessible; pass siteUrl explicitly. Available: %s",
			len(sites), siteList(sites))
	}
	s := strings.TrimSpace(site)
	for _, e := range sites { // exact
		if e.SiteURL == s {
			return e.SiteURL, nil
		}
	}
	if strings.HasPrefix(s, "sc-domain:") { // sc-domain without match
		return "", noSuchSiteErr(s, sites)
	}
	host := strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	host = strings.TrimSuffix(host, "/")
	hosts := []string{host}
	if strings.HasPrefix(host, "www.") {
		hosts = append(hosts, strings.TrimPrefix(host, "www."))
	} else {
		hosts = append(hosts, "www."+host)
	}
	for _, h := range hosts {
		for _, cand := range []string{
			"sc-domain:" + h,
			"https://" + h + "/",
			"https://" + h,
			"http://" + h + "/",
			"http://" + h,
		} {
			for _, e := range sites {
				if e.SiteURL == cand {
					return e.SiteURL, nil
				}
			}
		}
	}
	return "", noSuchSiteErr(s, sites)
}

func noSuchSiteErr(s string, sites []siteEntry) error {
	return fmt.Errorf("no property matches %q. Available: %s", s, siteList(sites))
}

func siteList(sites []siteEntry) string {
	names := make([]string, len(sites))
	for i, e := range sites {
		names[i] = e.SiteURL
	}
	return strings.Join(names, ", ")
}

// ---- date presets (sugar over the API's required explicit dates) ----
