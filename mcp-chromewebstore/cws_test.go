package main

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/homielab/homielab-mcp/internal/googleapi"
)

const testItemID = "abcdefghijklmnopabcdefghijklmnop"

func withTestServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	oldAPI, oldUpload := apiBase, uploadBase
	apiBase, uploadBase = srv.URL+"/v2", srv.URL+"/upload/v2"
	t.Cleanup(func() { apiBase, uploadBase = oldAPI, oldUpload })
}

func testClient() *cwsClient {
	return &cwsClient{googleapi.New(&http.Client{Timeout: 5 * time.Second}, "Chrome Web Store API")}
}

// withConfigDir points the token / last_item.json dir at a temp dir.
func withConfigDir(t *testing.T) {
	t.Helper()
	old := auth.BaseDir
	auth.BaseDir = t.TempDir()
	t.Cleanup(func() { auth.BaseDir = old })
	t.Setenv("CWS_MCP_PUBLISHER_ID", "")
}

func TestParseItemID(t *testing.T) {
	good := map[string]string{
		testItemID:              testItemID,
		"  " + testItemID + " ": testItemID,
		"https://chromewebstore.google.com/detail/my-ext/" + testItemID:             testItemID,
		"https://chromewebstore.google.com/detail/my-ext/" + testItemID + "?hl=vi":  testItemID,
		"https://chrome.google.com/webstore/devconsole/pub/" + testItemID + "/edit": testItemID,
	}
	for in, want := range good {
		got, err := parseItemID(in)
		if err != nil || got != want {
			t.Errorf("parseItemID(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", strings.Repeat("z", 32), "https://example.com/x"} {
		if _, err := parseItemID(in); err == nil {
			t.Errorf("parseItemID(%q) succeeded, want error", in)
		}
	}
}

func TestResolveItemRemembers(t *testing.T) {
	withConfigDir(t)
	if _, err := resolveItem("", testItemID); err == nil || !strings.Contains(err.Error(), "publisherId is required") {
		t.Fatalf("missing publisher: err = %v", err)
	}
	if _, err := resolveItem("pub-1", ""); err == nil || !strings.Contains(err.Error(), "itemId is required") {
		t.Fatalf("missing item: err = %v", err)
	}
	if _, err := resolveItem("bad id!", testItemID); err == nil {
		t.Fatal("bad publisher accepted")
	}
	it, err := resolveItem("pub-1", "https://chromewebstore.google.com/detail/x/"+testItemID)
	if err != nil || it != (item{"pub-1", testItemID}) {
		t.Fatalf("resolveItem = %+v, %v", it, err)
	}
	rememberItem(it)
	if got, err := resolveItem("", ""); err != nil || got != it {
		t.Fatalf("remembered = %+v, %v", got, err)
	}
	t.Setenv("CWS_MCP_PUBLISHER_ID", "pub-env")
	if got, _ := resolveItem("", ""); got.PublisherID != "pub-env" {
		t.Fatalf("env publisher not preferred over remembered: %+v", got)
	}
	if got, _ := resolveItem("pub-arg", ""); got.PublisherID != "pub-arg" {
		t.Fatalf("argument publisher not preferred: %+v", got)
	}
}

func TestLoadPackage(t *testing.T) {
	dir := t.TempDir()
	ext := filepath.Join(dir, "ext")
	files := map[string]string{
		"manifest.json":     `{"manifest_version":3,"name":"X","version":"1.2.3"}`,
		"js/background.js":  "console.log(1)",
		".git/HEAD":         "ref",
		".DS_Store":         "junk",
		"icons/icon128.png": "png",
	}
	for name, body := range files {
		p := filepath.Join(ext, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	data, ver, err := loadPackage(ext)
	if err != nil || ver != "1.2.3" {
		t.Fatalf("loadPackage(dir) = %q, %v", ver, err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	got := strings.Join(names, ",")
	for _, want := range []string{"manifest.json", "js/background.js", "icons/icon128.png"} {
		if !strings.Contains(got, want) {
			t.Errorf("zip lacks %s: %s", want, got)
		}
	}
	if strings.Contains(got, ".git") || strings.Contains(got, ".DS_Store") {
		t.Errorf("zip has dot files: %s", got)
	}

	zipPath := filepath.Join(dir, "ext.zip")
	if err := os.WriteFile(zipPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ver, err := loadPackage(zipPath); err != nil || ver != "1.2.3" {
		t.Fatalf("loadPackage(zip) = %q, %v", ver, err)
	}

	// A zip of the folder itself has manifest.json one level down.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("ext/manifest.json")
	w.Write([]byte(files["manifest.json"]))
	zw.Close()
	nested := filepath.Join(dir, "nested.zip")
	os.WriteFile(nested, buf.Bytes(), 0o644)
	if _, _, err := loadPackage(nested); err == nil || !strings.Contains(err.Error(), "no manifest.json at the root") {
		t.Fatalf("nested manifest: err = %v", err)
	}
	notZip := filepath.Join(dir, "x.zip")
	os.WriteFile(notZip, []byte("hello"), 0o644)
	if _, _, err := loadPackage(notZip); err == nil {
		t.Fatal("non-zip accepted")
	}
}

func TestMetricsQuery(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	q, err := metricsQuery([]string{"Installs", "page_views"}, "App-Version", "LAST_7_DAYS", "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	want := "dimensions=app-version&metrics=installs&metrics=page-views" +
		"&timeSpec.firstDate.day=25&timeSpec.firstDate.month=10&timeSpec.firstDate.year=2026" +
		"&timeSpec.lastDate.day=31&timeSpec.lastDate.month=10&timeSpec.lastDate.year=2026"
	if got := q.Encode(); got != want {
		t.Fatalf("query =\n%s\nwant\n%s", got, want)
	}
	q, err = metricsQuery(nil, "", "", "2026-01-01", "2026-01-31", now)
	if err != nil || strings.Join(q["metrics"], ",") != "installs,uninstalls,users" {
		t.Fatalf("defaults = %v, %v", q, err)
	}
	if _, err := metricsQuery([]string{"revenue"}, "", "", "", "", now); err == nil {
		t.Fatal("unknown metric accepted")
	}
	if _, err := metricsQuery(nil, "city", "", "", "", now); err == nil {
		t.Fatal("unknown dimension accepted")
	}
	if _, err := metricsQuery(nil, "", "", "2026-02-01", "2026-01-01", now); err == nil {
		t.Fatal("reversed range accepted")
	}
}

func TestFetchStatusAndRender(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/publishers/pub-1/items/"+testItemID+":fetchStatus" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Write([]byte(`{"itemId":"` + testItemID + `",
			"publishedItemRevisionStatus":{"state":"PUBLISHED","distributionChannels":[{"crxVersion":"1.0","deployPercentage":100}]},
			"submittedItemRevisionStatus":{"state":"PENDING_REVIEW","distributionChannels":[{"crxVersion":"1.1"}]},
			"lastAsyncUploadState":"SUCCEEDED","warned":true}`))
	})
	it := item{"pub-1", testItemID}
	st, err := testClient().fetchStatus(context.Background(), it)
	if err != nil {
		t.Fatal(err)
	}
	out := renderStatus(it, st)
	for _, want := range []string{"Published: PUBLISHED · version 1.0 at 100%", "Submitted: PENDING_REVIEW · version 1.1\n", "Last upload (24 h): SUCCEEDED", "WARNED"} {
		if !strings.Contains(out, want) {
			t.Errorf("render lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "TAKEN DOWN") {
		t.Errorf("render shows takedown:\n%s", out)
	}
}

func TestUpload(t *testing.T) {
	pkg := []byte("PK-fake-zip")
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/upload/v2/publishers/pub-1/items/"+testItemID+":upload" ||
			r.URL.Query().Get("uploadType") != "media" || r.Header.Get("Content-Type") != "application/zip" {
			t.Errorf("unexpected %s %s ct=%s", r.Method, r.URL, r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		if !bytes.Equal(body, pkg) {
			t.Errorf("body = %q", body)
		}
		w.Write([]byte(`{"itemId":"` + testItemID + `","crxVersion":"1.1","uploadState":"SUCCEEDED"}`))
	})
	up, err := testClient().upload(context.Background(), item{"pub-1", testItemID}, pkg)
	if err != nil || up.UploadState != "SUCCEEDED" || up.CrxVersion != "1.1" {
		t.Fatalf("upload = %+v, %v", up, err)
	}
}

func TestUploadErrorNotRetried(t *testing.T) {
	calls := 0
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":{"code":503,"message":"busy","status":"UNAVAILABLE"}}`))
	})
	_, err := testClient().upload(context.Background(), item{"pub-1", testItemID}, []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "busy") || calls != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls)
	}
}

func TestPublishBody(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v2/publishers/pub-1/items/"+testItemID+":publish" ||
			string(body) != `{"deployInfos":[{"deployPercentage":10}],"publishType":"STAGED_PUBLISH"}` {
			t.Errorf("unexpected %s body %s", r.URL.Path, body)
		}
		w.Write([]byte(`{"state":"PENDING_REVIEW","warningInfo":{"warnings":[{"reason":"X","description":"y"}]}}`))
	})
	body := map[string]any{"publishType": "STAGED_PUBLISH", "deployInfos": []map[string]int{{"deployPercentage": 10}}}
	var out publishResponse
	err := testClient().call(context.Background(), http.MethodPost, item{"pub-1", testItemID}.path()+":publish", nil, body, &out)
	if err != nil || out.State != "PENDING_REVIEW" || len(out.WarningInfo.Warnings) != 1 {
		t.Fatalf("publish = %+v, %v", out, err)
	}
}

func TestRenderMetrics(t *testing.T) {
	out := renderMetrics(metricsPage{MetricRows: []metricRow{{
		Date:            apiDate{2026, 10, 5},
		DimensionValues: []string{"VN"},
		MetricValues: []struct {
			Metric          string `json:"metric"`
			Value           string `json:"value"`
			UniqueUserCount string `json:"uniqueUserCount"`
		}{{Metric: "installs", Value: "12"}},
	}}, NextPageToken: "t2"}, []string{"installs", "users"}, true)
	for _, want := range []string{"| date | dimension | installs | users |", "| 2026-10-05 | VN | 12 | — |", `pageToken "t2"`} {
		if !strings.Contains(out, want) {
			t.Errorf("render lacks %q:\n%s", want, out)
		}
	}
}
