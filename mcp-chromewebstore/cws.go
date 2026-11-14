package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/homielab/homielab-mcp/internal/googleapi"
)

var (
	apiBase    = "https://chromewebstore.googleapis.com/v2"
	uploadBase = "https://chromewebstore.googleapis.com/upload/v2"
)

// uploadTimeout bounds one package upload (the API accepts up to 2 GB).
const uploadTimeout = 15 * time.Minute

type cwsClient struct {
	*googleapi.Client
}

func newCWSClient(ctx context.Context) (*cwsClient, error) {
	hc, err := auth.HTTPClient(ctx)
	if err != nil {
		return nil, err
	}
	return &cwsClient{googleapi.New(hc, "Chrome Web Store API")}, nil
}

// ---- resources ----

type distributionChannel struct {
	CrxVersion       string `json:"crxVersion"`
	DeployPercentage *int   `json:"deployPercentage"`
}

type revisionStatus struct {
	State                string                `json:"state"`
	DistributionChannels []distributionChannel `json:"distributionChannels"`
}

type itemStatus struct {
	Name                        string          `json:"name"`
	ItemID                      string          `json:"itemId"`
	PublicKey                   string          `json:"publicKey"`
	PublishedItemRevisionStatus *revisionStatus `json:"publishedItemRevisionStatus"`
	SubmittedItemRevisionStatus *revisionStatus `json:"submittedItemRevisionStatus"`
	LastAsyncUploadState        string          `json:"lastAsyncUploadState"`
	Warned                      bool            `json:"warned"`
	TakenDown                   bool            `json:"takenDown"`
}

type uploadResponse struct {
	Name        string `json:"name"`
	ItemID      string `json:"itemId"`
	CrxVersion  string `json:"crxVersion"`
	UploadState string `json:"uploadState"`
}

type warning struct {
	Reason      string `json:"reason"`
	Description string `json:"description"`
}

type publishResponse struct {
	Name        string `json:"name"`
	ItemID      string `json:"itemId"`
	State       string `json:"state"`
	WarningInfo struct {
		Warnings []warning `json:"warnings"`
	} `json:"warningInfo"`
}

type review struct {
	Name       string `json:"name"`
	Rating     int    `json:"rating"`
	Comment    string `json:"comment"`
	CreateTime string `json:"createTime"`
	AuthorInfo struct {
		DisplayName string `json:"displayName"`
	} `json:"authorInfo"`
}

type reviewsPage struct {
	Reviews       []review `json:"reviews"`
	NextPageToken string   `json:"nextPageToken"`
}

type ratingSummary struct {
	Rating              float64 `json:"rating"`
	RatingCount         string  `json:"ratingCount"`
	NonEmptyReviewCount string  `json:"nonEmptyReviewCount"`
	FiveStar            string  `json:"fiveStarRatingCount"`
	FourStar            string  `json:"fourStarRatingCount"`
	ThreeStar           string  `json:"threeStarRatingCount"`
	TwoStar             string  `json:"twoStarRatingCount"`
	OneStar             string  `json:"oneStarRatingCount"`
}

type apiDate struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

func (d apiDate) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day) }

type metricRow struct {
	Date            apiDate  `json:"date"`
	DimensionValues []string `json:"dimensionValues"`
	MetricValues    []struct {
		Metric          string `json:"metric"`
		Value           string `json:"value"`
		UniqueUserCount string `json:"uniqueUserCount"`
	} `json:"metricValues"`
}

type metricsPage struct {
	MetricRows    []metricRow `json:"metricRows"`
	NextPageToken string      `json:"nextPageToken"`
}

// ---- API calls ----

// item identifies one store item: publishers/{publisherId}/items/{itemId}.
type item struct {
	PublisherID string `json:"publisherId"`
	ItemID      string `json:"itemId"`
}

func (it item) path() string {
	return "/publishers/" + url.PathEscape(it.PublisherID) + "/items/" + url.PathEscape(it.ItemID)
}

// call sends method to apiBase+path (path already includes any ":verb"),
// with body JSON-encoded when non-nil, and decodes the response into out.
func (c *cwsClient) call(ctx context.Context, method, path string, q url.Values, body, out any) error {
	u := googleapi.JoinURL(apiBase, path)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	data, _, err := c.Do(ctx, method, u, body)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (c *cwsClient) fetchStatus(ctx context.Context, it item) (itemStatus, error) {
	var s itemStatus
	err := c.call(ctx, http.MethodGet, it.path()+":fetchStatus", nil, nil, &s)
	return s, err
}

// upload sends a zip package as a simple media upload. The bytes are not
// retried once a response arrived (a retry could create a second upload);
// only transport failures are retried.
func (c *cwsClient) upload(ctx context.Context, it item, pkg []byte) (uploadResponse, error) {
	u := googleapi.JoinURL(uploadBase, it.path()+":upload") + "?uploadType=media"
	hc := *c.HTTP
	hc.Timeout = uploadTimeout
	var out uploadResponse
	var err error
	for attempt := 1; attempt <= googleapi.MaxRetries; attempt++ {
		if attempt > 1 {
			if werr := googleapi.WaitBackoff(ctx, attempt, 0); werr != nil {
				return out, werr
			}
			log.Printf("retrying upload %s after transport error: %v", u, err)
		}
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(pkg))
		if rerr != nil {
			return out, rerr
		}
		req.Header.Set("Content-Type", "application/zip")
		resp, derr := hc.Do(req)
		if derr != nil {
			err = derr
			continue
		}
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, googleapi.MaxResponseBody))
		resp.Body.Close()
		if rerr != nil {
			return out, rerr
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return out, c.Error(resp.StatusCode, data)
		}
		return out, json.Unmarshal(data, &out)
	}
	return out, err
}

// ---- item resolution ----
//
// The API has no "list my items" endpoint, so every tool needs a publisher
// id and an item id. The last pair used successfully is remembered in the
// config dir and used when the arguments are omitted.

var (
	itemIDRE      = regexp.MustCompile(`^[a-p]{32}$`)
	itemIDInURLRE = regexp.MustCompile(`(?:^|/)([a-p]{32})(?:[/?#]|$)`)
	publisherRE   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

func lastItemPath() (string, error) {
	dir, err := auth.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "last_item.json"), nil
}

func rememberedItem() item {
	var it item
	p, err := lastItemPath()
	if err != nil {
		return it
	}
	if data, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(data, &it)
	}
	return it
}

// rememberItem stores the pair after a call that the API accepted.
func rememberItem(it item) {
	p, err := lastItemPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	data, _ := json.Marshal(it)
	_ = os.WriteFile(p, data, 0o600)
}

// parseItemID accepts a 32-letter extension id or a store / dashboard URL
// that contains one.
func parseItemID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if itemIDRE.MatchString(s) {
		return s, nil
	}
	if m := itemIDInURLRE.FindStringSubmatch(s); m != nil {
		return m[1], nil
	}
	return "", fmt.Errorf("itemId %q must be a 32-letter extension id (a-p) or a Chrome Web Store URL containing one", s)
}

// resolveItem fills omitted arguments from CWS_MCP_PUBLISHER_ID and the
// remembered pair, and validates the result.
func resolveItem(publisherID, itemID string) (item, error) {
	last := rememberedItem()
	it := item{PublisherID: strings.TrimSpace(publisherID)}
	if it.PublisherID == "" {
		it.PublisherID = strings.TrimSpace(os.Getenv("CWS_MCP_PUBLISHER_ID"))
	}
	if it.PublisherID == "" {
		it.PublisherID = last.PublisherID
	}
	if it.PublisherID == "" {
		return it, errors.New("publisherId is required: Developer Dashboard (chrome.google.com/webstore/devconsole) → Publisher → Settings → Publisher ID. " +
			"It is remembered after the first successful call; CWS_MCP_PUBLISHER_ID sets a default")
	}
	if !publisherRE.MatchString(it.PublisherID) {
		return it, fmt.Errorf("publisherId %q has unexpected characters; copy it from Developer Dashboard → Publisher → Settings", it.PublisherID)
	}
	if strings.TrimSpace(itemID) == "" {
		if last.ItemID == "" {
			return it, errors.New("itemId is required: the 32-letter extension id (from the store URL or the Developer Dashboard). It is remembered after the first successful call")
		}
		it.ItemID = last.ItemID
		return it, nil
	}
	id, err := parseItemID(itemID)
	if err != nil {
		return it, err
	}
	it.ItemID = id
	return it, nil
}

// ---- packages ----

// maxPackageSize matches the API's media upload limit.
const maxPackageSize = 2 << 30

// loadPackage returns the zip bytes and manifest version of a .zip file or,
// for a directory, a zip of its contents built in memory (dot files and
// directories such as .git are left out). The manifest must be at the root.
func loadPackage(path string) ([]byte, string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	var data []byte
	if fi.IsDir() {
		if data, err = zipDir(path); err != nil {
			return nil, "", err
		}
	} else {
		if fi.Size() > maxPackageSize {
			return nil, "", fmt.Errorf("%s is %d bytes; the upload limit is 2 GB", path, fi.Size())
		}
		if data, err = os.ReadFile(path); err != nil {
			return nil, "", err
		}
	}
	ver, err := manifestVersion(data)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	return data, ver, nil
}

func zipDir(dir string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	if buf.Len() > maxPackageSize {
		return nil, fmt.Errorf("zipped folder is %d bytes; the upload limit is 2 GB", buf.Len())
	}
	return buf.Bytes(), nil
}

// manifestVersion reads "version" from manifest.json at the zip root.
func manifestVersion(zipData []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return "", fmt.Errorf("not a zip file: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != "manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		var m struct {
			Version string `json:"version"`
		}
		// Chrome accepts manifests plain JSON rejects (comments): the store
		// is the judge, so an unreadable manifest is not an error here.
		if err := json.NewDecoder(io.LimitReader(rc, 4<<20)).Decode(&m); err != nil {
			return "(unreadable manifest.json)", nil
		}
		if m.Version == "" {
			return "", errors.New("manifest.json has no version")
		}
		return m.Version, nil
	}
	return "", errors.New("no manifest.json at the root of the package (zip the extension folder's contents, not the folder)")
}
