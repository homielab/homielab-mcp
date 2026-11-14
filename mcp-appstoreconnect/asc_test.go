package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/googleapi"
)

// fakeAPI serves canned JSON per "METHOD /path" and records requests.
type fakeAPI struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	routes map[string]func(w http.ResponseWriter, r *http.Request, body map[string]any)
	calls  []string
	bodies map[string]map[string]any
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	dir := isolate(t)
	testKey(t, dir, "TESTKEY")
	f := &fakeAPI{t: t, routes: map[string]func(http.ResponseWriter, *http.Request, map[string]any){}, bodies: map[string]map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	old, oldBackoff := apiBase, googleapi.RetryBackoff
	apiBase, googleapi.RetryBackoff = f.srv.URL, time.Millisecond
	t.Cleanup(func() { apiBase, googleapi.RetryBackoff = old, oldBackoff })
	return f
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
		f.t.Errorf("%s %s: missing bearer JWT", r.Method, r.URL.Path)
	}
	key := r.Method + " " + r.URL.Path
	var body map[string]any
	if data, _ := io.ReadAll(r.Body); len(data) > 0 {
		json.Unmarshal(data, &body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, key+"?"+r.URL.RawQuery)
	f.bodies[key] = body
	h, ok := f.routes[key]
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"errors":[{"status":"404","code":"NOT_FOUND","title":"not found","detail":"no route %s"}]}`, key)
		return
	}
	h(w, r, body)
}

func (f *fakeAPI) json(key, body string) {
	f.routes[key] = func(w http.ResponseWriter, r *http.Request, _ map[string]any) { io.WriteString(w, body) }
}

func (f *fakeAPI) called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// session connects an MCP client to a server with all tools registered.
func session(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	registerTools(server)
	registerPrompts(server)
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

const appsJSON = `{"data":[
 {"type":"apps","id":"111","attributes":{"name":"Notes Pro","bundleId":"com.ex.notes","sku":"N1","primaryLocale":"en-US"}},
 {"type":"apps","id":"222","attributes":{"name":"Notes Lite","bundleId":"com.ex.noteslite","sku":"N2","primaryLocale":"en-US"}}]}`

func TestDecodeError(t *testing.T) {
	err := decodeError(409, []byte(`{"errors":[{"status":"409","code":"ENTITY_ERROR.ATTRIBUTE.INVALID","title":"An attribute value is invalid.","detail":"versionString exists","source":{"pointer":"/data/attributes/versionString"}}]}`))
	want := "App Store Connect API 409: ENTITY_ERROR.ATTRIBUTE.INVALID: An attribute value is invalid. — versionString exists (/data/attributes/versionString)"
	if err.Error() != want {
		t.Errorf("got  %q\nwant %q", err, want)
	}
	if err := decodeError(401, []byte(`{"errors":[{"code":"NOT_AUTHORIZED","title":"Authentication credentials are missing or invalid."}]}`)); !strings.Contains(err.Error(), "asc_auth_status") {
		t.Errorf("401 should hint at asc_auth_status: %v", err)
	}
	if err := decodeError(502, []byte("<html>")); !strings.Contains(err.Error(), "502: <html>") {
		t.Errorf("non-JSON: %v", err)
	}
}

func TestListPaginatesAndCaps(t *testing.T) {
	f := newFakeAPI(t)
	f.routes["GET /v1/apps"] = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.URL.Query().Get("cursor") == "" {
			fmt.Fprintf(w, `{"data":[{"type":"apps","id":"1"},{"type":"apps","id":"2"}],"links":{"next":"%s/v1/apps?cursor=x"}}`, f.srv.URL)
			return
		}
		io.WriteString(w, `{"data":[{"type":"apps","id":"3"}]}`)
	}
	c, err := newASCClient()
	if err != nil {
		t.Fatal(err)
	}
	all, _, err := list[appAttrs](context.Background(), c, "/v1/apps", nil, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("all = %d, %v", len(all), err)
	}
	f.calls = nil
	two, _, _ := list[appAttrs](context.Background(), c, "/v1/apps", nil, 2)
	if len(two) != 2 || len(f.calls) != 1 || !strings.Contains(f.calls[0], "limit=2") {
		t.Errorf("max 2: got %d items, calls %v", len(two), f.calls)
	}
}

func TestResolveApp(t *testing.T) {
	f := newFakeAPI(t)
	f.routes["GET /v1/apps"] = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if b := r.URL.Query().Get("filter[bundleId]"); b != "" && b != "com.ex.notes" {
			io.WriteString(w, `{"data":[]}`)
			return
		}
		io.WriteString(w, appsJSON)
	}
	f.json("GET /v1/apps/222", `{"data":{"type":"apps","id":"222","attributes":{"name":"Notes Lite","bundleId":"com.ex.noteslite"}}}`)
	c, _ := newASCClient()
	ctx := context.Background()

	if _, err := c.resolveApp(ctx, ""); err == nil || !strings.Contains(err.Error(), "app is required") {
		t.Errorf("empty, nothing remembered: %v", err)
	}
	if a, err := c.resolveApp(ctx, "com.ex.notes"); err != nil || a.ID != "111" {
		t.Errorf("bundle id: %+v %v", a, err)
	}
	if a, err := c.resolveApp(ctx, ""); err != nil || a.ID != "111" {
		t.Errorf("remembered: %+v %v", a, err)
	}
	if a, err := c.resolveApp(ctx, "222"); err != nil || a.Name != "Notes Lite" {
		t.Errorf("apple id: %+v %v", a, err)
	}
	if a, err := c.resolveApp(ctx, "notes lite"); err != nil || a.ID != "222" {
		t.Errorf("exact name: %+v %v", a, err)
	}
	if _, err := c.resolveApp(ctx, "notes"); err == nil || !strings.Contains(err.Error(), "several apps") {
		t.Errorf("ambiguous: %v", err)
	}
	if _, err := c.resolveApp(ctx, "calendar"); err == nil || !strings.Contains(err.Error(), "no app matches") {
		t.Errorf("unknown: %v", err)
	}
}

func TestNormPlatform(t *testing.T) {
	for in, want := range map[string]string{"": "IOS", "ios": "IOS", "macOS": "MAC_OS", "tv_os": "TV_OS", "visionos": "VISION_OS"} {
		if got, err := normPlatform(in); err != nil || got != want {
			t.Errorf("normPlatform(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := normPlatform("android"); err == nil {
		t.Error("android should fail")
	}
}

func TestAPIURL(t *testing.T) {
	old := apiBase
	apiBase = "https://api.example"
	t.Cleanup(func() { apiBase = old })
	u, err := apiURL("/v1/apps?limit=5", map[string]string{"filter[sku]": "X"})
	if err != nil || u != "https://api.example/v1/apps?filter%5Bsku%5D=X&limit=5" {
		t.Errorf("apiURL = %q, %v", u, err)
	}
	if u, _ := apiURL("https://api.example/v2/inAppPurchases/1", nil); u != "https://api.example/v2/inAppPurchases/1" {
		t.Errorf("full URL = %q", u)
	}
	for _, bad := range []string{"apps", "https://evil.example/v1/apps", "/../v1"} {
		if _, err := apiURL(bad, nil); err == nil {
			t.Errorf("apiURL(%q) should fail", bad)
		}
	}
}

func TestToolSchemasIncludeEmbeddedFields(t *testing.T) {
	newFakeAPI(t)
	cs := session(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	props := map[string]map[string]any{}
	for _, tool := range res.Tools {
		schema, _ := json.Marshal(tool.InputSchema)
		var s struct {
			Properties map[string]any `json:"properties"`
		}
		json.Unmarshal(schema, &s)
		props[tool.Name] = s.Properties
	}
	if len(res.Tools) != 26 {
		t.Errorf("%d tools, want 26", len(res.Tools))
	}
	for tool, fields := range map[string][]string{
		"asc_version_update":   {"app", "versionString", "newVersionString", "releaseType", "buildNumber"},
		"asc_version_create":   {"versionString", "copyright", "buildId"},
		"asc_build_distribute": {"buildNumber", "groups", "usesNonExemptEncryption"},
		"asc_review_submit":    {"app", "versionId", "platform"},
	} {
		for _, f := range fields {
			if _, ok := props[tool][f]; !ok {
				t.Errorf("%s schema lacks %q (has %v)", tool, f, props[tool])
			}
		}
	}
}

func TestBuildDistribute(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /v1/apps/111", `{"data":{"type":"apps","id":"111","attributes":{"name":"Notes Pro","bundleId":"com.ex.notes"}}}`)
	f.json("GET /v1/builds", `{"data":[{"type":"builds","id":"b45","attributes":{"version":"45","processingState":"VALID"},
		"relationships":{"preReleaseVersion":{"data":{"type":"preReleaseVersions","id":"p1"}}}}],
		"included":[{"type":"preReleaseVersions","id":"p1","attributes":{"version":"1.4.0","platform":"IOS"}}]}`)
	f.json("GET /v1/apps/111/betaGroups", `{"data":[{"type":"betaGroups","id":"g1","attributes":{"name":"Team","isInternalGroup":true}},
		{"type":"betaGroups","id":"g2","attributes":{"name":"Public Beta","isInternalGroup":false}}]}`)
	f.json("PATCH /v1/builds/b45", `{"data":{"type":"builds","id":"b45"}}`)
	f.json("GET /v1/betaBuildLocalizations", `{"data":[]}`)
	f.json("POST /v1/betaBuildLocalizations", `{"data":{"type":"betaBuildLocalizations","id":"l1"}}`)
	f.routes["POST /v1/betaGroups/g1/relationships/builds"] = func(w http.ResponseWriter, r *http.Request, _ map[string]any) { w.WriteHeader(204) }
	f.routes["POST /v1/betaGroups/g2/relationships/builds"] = func(w http.ResponseWriter, r *http.Request, _ map[string]any) { w.WriteHeader(204) }
	f.json("POST /v1/betaAppReviewSubmissions", `{"data":{"type":"betaAppReviewSubmissions","id":"s1"}}`)

	out, isErr := call(t, session(t), "asc_build_distribute", map[string]any{
		"app": "111", "buildNumber": "45", "groups": []string{"team", "Public Beta"},
		"whatToTest": "New sync", "usesNonExemptEncryption": false,
	})
	if isErr {
		t.Fatal(out)
	}
	for _, want := range []string{"1.4.0 (45)", "usesNonExemptEncryption = false", "What to Test (en-US)", "added to Team (internal)", "added to Public Beta (external)", "submitted for beta app review"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	loc := f.bodies["POST /v1/betaBuildLocalizations"]["data"].(map[string]any)
	if loc["attributes"].(map[string]any)["whatsNew"] != "New sync" || loc["attributes"].(map[string]any)["locale"] != "en-US" {
		t.Errorf("localization body = %v", loc)
	}
	if !f.called("GET /v1/builds?") || !strings.Contains(strings.Join(f.calls, " "), "filter%5Bversion%5D=45") {
		t.Errorf("build lookup calls: %v", f.calls)
	}
}

func TestReviewSubmitFlow(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /v1/apps/111", `{"data":{"type":"apps","id":"111","attributes":{"name":"Notes Pro","bundleId":"com.ex.notes"}}}`)
	f.json("GET /v1/apps/111/appStoreVersions", `{"data":[
		{"type":"appStoreVersions","id":"v2","attributes":{"platform":"IOS","versionString":"1.5.0","appVersionState":"PREPARE_FOR_SUBMISSION"},
		 "relationships":{"build":{"data":{"type":"builds","id":"b50"}}}},
		{"type":"appStoreVersions","id":"v1","attributes":{"platform":"IOS","versionString":"1.4.0","appVersionState":"READY_FOR_DISTRIBUTION"}}],
		"included":[{"type":"builds","id":"b50","attributes":{"version":"50"}}]}`)
	f.json("GET /v1/reviewSubmissions", `{"data":[]}`)
	f.json("POST /v1/reviewSubmissions", `{"data":{"type":"reviewSubmissions","id":"rs1","attributes":{"state":"READY_FOR_REVIEW"}}}`)
	f.json("POST /v1/reviewSubmissionItems", `{"data":{"type":"reviewSubmissionItems","id":"i1"}}`)
	f.json("PATCH /v1/reviewSubmissions/rs1", `{"data":{"type":"reviewSubmissions","id":"rs1","attributes":{"state":"WAITING_FOR_REVIEW"}}}`)

	out, isErr := call(t, session(t), "asc_review_submit", map[string]any{"app": "111"})
	if isErr {
		t.Fatal(out)
	}
	if !strings.Contains(out, "1.5.0 (build 50)") || !strings.Contains(out, "WAITING_FOR_REVIEW") {
		t.Errorf("output: %s", out)
	}
	item := f.bodies["POST /v1/reviewSubmissionItems"]["data"].(map[string]any)["relationships"].(map[string]any)
	if item["appStoreVersion"].(map[string]any)["data"].(map[string]any)["id"] != "v2" {
		t.Errorf("item relationships = %v", item)
	}
	if f.bodies["PATCH /v1/reviewSubmissions/rs1"]["data"].(map[string]any)["attributes"].(map[string]any)["submitted"] != true {
		t.Error("submission not marked submitted")
	}
}

func TestLocalizationUpdateCreatesAndPatches(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /v1/apps/111", `{"data":{"type":"apps","id":"111","attributes":{"name":"Notes Pro","bundleId":"com.ex.notes"}}}`)
	f.json("GET /v1/apps/111/appStoreVersions", `{"data":[{"type":"appStoreVersions","id":"v2","attributes":{"platform":"IOS","versionString":"1.5.0","appVersionState":"PREPARE_FOR_SUBMISSION"}}]}`)
	f.json("GET /v1/appStoreVersions/v2/appStoreVersionLocalizations", `{"data":[{"type":"appStoreVersionLocalizations","id":"vl-en","attributes":{"locale":"en-US"}}]}`)
	f.json("PATCH /v1/appStoreVersionLocalizations/vl-en", `{"data":{"type":"appStoreVersionLocalizations","id":"vl-en"}}`)
	f.json("GET /v1/apps/111/appInfos", `{"data":[{"type":"appInfos","id":"ai-live","attributes":{"state":"READY_FOR_DISTRIBUTION"}},{"type":"appInfos","id":"ai-edit","attributes":{"state":"PREPARE_FOR_SUBMISSION"}}]}`)
	f.json("GET /v1/appInfos/ai-edit/appInfoLocalizations", `{"data":[]}`)
	f.json("POST /v1/appInfoLocalizations", `{"data":{"type":"appInfoLocalizations","id":"il-vi"}}`)
	cs := session(t)

	if out, isErr := call(t, cs, "asc_localization_update", map[string]any{"app": "111", "locale": "en-US", "subtitle": strings.Repeat("x", 31)}); !isErr || !strings.Contains(out, "limit is 30") {
		t.Errorf("long subtitle: %s", out)
	}
	out, isErr := call(t, cs, "asc_localization_update", map[string]any{"app": "111", "locale": "en-US", "whatsNew": "Bug fixes"})
	if isErr || !strings.Contains(out, "set whatsNew") {
		t.Errorf("patch: %s", out)
	}
	out, isErr = call(t, cs, "asc_localization_update", map[string]any{"locale": "vi", "name": "Ghi chú"})
	if isErr || !strings.Contains(out, "App info (vi): set name") {
		t.Errorf("create: %s", out)
	}
	rel := f.bodies["POST /v1/appInfoLocalizations"]["data"].(map[string]any)["relationships"].(map[string]any)["appInfo"].(map[string]any)["data"].(map[string]any)
	if rel["id"] != "ai-edit" {
		t.Errorf("should write the editable appInfo, got %v", rel)
	}
}

func TestReviewReplyReplace(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /v1/customerReviews/r1/response", `{"data":{"type":"customerReviewResponses","id":"old","attributes":{"responseBody":"Thanks!"}}}`)
	f.routes["DELETE /v1/customerReviewResponses/old"] = func(w http.ResponseWriter, r *http.Request, _ map[string]any) { w.WriteHeader(204) }
	f.json("POST /v1/customerReviewResponses", `{"data":{"type":"customerReviewResponses","id":"new","attributes":{"state":"PENDING_PUBLISH"}}}`)
	cs := session(t)

	if out, isErr := call(t, cs, "asc_review_reply", map[string]any{"reviewId": "r1", "response": "Fixed in 1.5"}); !isErr || !strings.Contains(out, "replace:true") {
		t.Errorf("without replace: %s", out)
	}
	if f.called("POST /v1/customerReviewResponses") {
		t.Error("posted despite existing response")
	}
	out, isErr := call(t, cs, "asc_review_reply", map[string]any{"reviewId": "r1", "response": "Fixed in 1.5", "replace": true})
	if isErr || !strings.Contains(out, "PENDING_PUBLISH") || !f.called("DELETE /v1/customerReviewResponses/old") {
		t.Errorf("replace: %s", out)
	}
}

func TestSalesReportGzip(t *testing.T) {
	f := newFakeAPI(t)
	tsv := "Provider\tTitle\tProduct Type Identifier\tUnits\tDeveloper Proceeds\tCountry Code\tCurrency of Proceeds\n" +
		"APPLE\tNotes Pro\t1F\t10\t0\tUS\tUSD\n" +
		"APPLE\tNotes Pro\t7F\t4\t0\tVN\tUSD\n" +
		"APPLE\tPro Upgrade\tIA1\t2\t2.10\tUS\tUSD\n"
	f.routes["GET /v1/salesReports"] = func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Header.Get("Accept") != "application/a-gzip" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		if q := r.URL.Query(); q.Get("filter[vendorNumber]") != "8800" || q.Get("filter[reportDate]") != "2026-10-27" {
			t.Errorf("query = %v", q)
		}
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		io.WriteString(zw, tsv)
		zw.Close()
		w.Header().Set("Content-Type", "application/a-gzip")
		w.Write(b.Bytes())
	}
	out, isErr := call(t, session(t), "asc_sales_report", map[string]any{"vendorNumber": "8800", "reportDate": "2026-10-27"})
	if isErr {
		t.Fatal(out)
	}
	for _, want := range []string{"Total units: 16", "app downloads", "updates", "in-app purchases", "USD", "4.20", "| Notes Pro |"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestPhasedReleaseStatusWithoutOne(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /v1/apps/111", `{"data":{"type":"apps","id":"111","attributes":{"name":"Notes Pro","bundleId":"com.ex.notes"}}}`)
	f.json("GET /v1/apps/111/appStoreVersions", `{"data":[
		{"type":"appStoreVersions","id":"v1","attributes":{"platform":"IOS","versionString":"1.4.0","appVersionState":"READY_FOR_DISTRIBUTION","createdDate":"2026-09-01T00:00:00Z"}},
		{"type":"appStoreVersions","id":"v2","attributes":{"platform":"IOS","versionString":"1.5.0","appVersionState":"READY_FOR_DISTRIBUTION","createdDate":"2026-10-01T00:00:00Z"}}]}`)
	f.json("GET /v1/appStoreVersions/v2/appStoreVersionPhasedRelease", `{"data":null}`)
	out, isErr := call(t, session(t), "asc_phased_release", map[string]any{"app": "111"})
	if isErr || !strings.Contains(out, "No phased release") || !strings.Contains(out, "1.5.0") {
		t.Errorf("status: %s", out)
	}
}

func TestPromptsList(t *testing.T) {
	newFakeAPI(t)
	res, err := session(t).GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "asc_release_status", Arguments: map[string]string{"app": "com.ex.notes"}})
	if err != nil || !strings.Contains(res.Messages[0].Content.(*mcp.TextContent).Text, `"com.ex.notes"`) {
		t.Errorf("prompt: %v %v", res, err)
	}
}

func TestReportParsing(t *testing.T) {
	if s, _ := gunzipMaybe([]byte("plain")); s != "plain" {
		t.Errorf("plain = %q", s)
	}
	tb := parseTSV("A\tB\r\n1\t\r\n\n2\tx\n")
	if len(tb.Rows) != 2 || tb.col("b") != 1 {
		t.Errorf("table = %+v", tb)
	}
	if got := renderTablePreview(tb, 1); !strings.Contains(got, "| A |") || strings.Contains(got, "| B |") || !strings.Contains(got, "1 more rows") {
		t.Errorf("preview should drop the empty column and note more rows:\n%s", got)
	}
	if got := tb.csv(); got != "A,B\n1,\n2,x\n" {
		t.Errorf("csv = %q", got)
	}
}

func TestLocalizationsFallBackToNewestVersion(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /v1/apps/111", `{"data":{"type":"apps","id":"111","attributes":{"name":"Notes Pro","bundleId":"com.ex.notes"}}}`)
	f.json("GET /v1/apps/111/appStoreVersions", `{"data":[
		{"type":"appStoreVersions","id":"v1","attributes":{"platform":"IOS","versionString":"1.4.0","appVersionState":"READY_FOR_DISTRIBUTION","createdDate":"2026-09-01T00:00:00Z"}},
		{"type":"appStoreVersions","id":"v2","attributes":{"platform":"IOS","versionString":"1.5.0","appVersionState":"READY_FOR_DISTRIBUTION","createdDate":"2026-10-01T00:00:00Z"}}]}`)
	f.json("GET /v1/appStoreVersions/v2/appStoreVersionLocalizations", `{"data":[{"type":"appStoreVersionLocalizations","id":"l","attributes":{"locale":"en-US","promotionalText":"Hi"}}]}`)
	f.json("PATCH /v1/appStoreVersionLocalizations/l", `{"data":{"type":"appStoreVersionLocalizations","id":"l"}}`)
	f.json("GET /v1/apps/111/appInfos", `{"data":[{"type":"appInfos","id":"ai","attributes":{"state":"READY_FOR_DISTRIBUTION"}}]}`)
	f.json("GET /v1/appInfos/ai/appInfoLocalizations", `{"data":[]}`)
	cs := session(t)

	if out, isErr := call(t, cs, "asc_localizations", map[string]any{"app": "111"}); isErr || !strings.Contains(out, "1.5.0") || !strings.Contains(out, "promotional text: Hi") {
		t.Errorf("read should use the newest live version: %s", out)
	}
	if out, isErr := call(t, cs, "asc_localization_update", map[string]any{"locale": "en-US", "promotionalText": "Sale"}); isErr {
		t.Errorf("promo text on a live version: %s", out)
	}
	if out, isErr := call(t, cs, "asc_localization_update", map[string]any{"locale": "en-US", "whatsNew": "x"}); !isErr || !strings.Contains(out, "no editable") {
		t.Errorf("whatsNew needs an editable version: %s", out)
	}
}

func TestStripLinks(t *testing.T) {
	in := `{"data":[{"type":"apps","id":"1","attributes":{"n":12345678901234567890},"links":{"self":"x"},
		"relationships":{"builds":{"links":{"self":"y"},"meta":{"paging":{}}},"app":{"data":{"type":"apps","id":"2"},"links":{"self":"z"}}}}],
		"links":{"next":"n"}}`
	got := string(stripLinks([]byte(in)))
	want := `{"data":[{"attributes":{"n":12345678901234567890},"id":"1","relationships":{"app":{"data":{"id":"2","type":"apps"}}},"type":"apps"}],"links":{"next":"n"}}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
