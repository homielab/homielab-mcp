package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/homielab/homielab-mcp/internal/googleapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func withTestServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	oldAPI, oldUpload, oldResumable := apiBase, uploadBase, resumableBase
	apiBase = srv.URL
	uploadBase = srv.URL
	resumableBase = srv.URL
	t.Cleanup(func() { apiBase, uploadBase, resumableBase = oldAPI, oldUpload, oldResumable })
}

func testClient() *playClient {
	return &playClient{googleapi.New(&http.Client{Timeout: 5 * time.Second}, "Play Developer API")}
}

// ---- package names ----

func TestValidatePackage(t *testing.T) {
	for _, ok := range []string{"com.example.app", "com.company.app_name", "io.github.x1.y2", "org.a.B"} {
		if err := validatePackage(ok); err != nil {
			t.Errorf("validatePackage(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "nodot", ".starts.with.dot", "com..double", "com.1starts.with.digit", "has space", "../etc"} {
		if err := validatePackage(bad); err == nil {
			t.Errorf("validatePackage(%q) should fail", bad)
		}
	}
}

func TestResolvePackageRemembers(t *testing.T) {
	dir := t.TempDir()
	old := auth.BaseDir
	auth.BaseDir = dir
	t.Cleanup(func() { auth.BaseDir = old })

	if _, err := resolvePackage(""); err == nil || !strings.Contains(err.Error(), "packageName is required") {
		t.Errorf("empty with no memory should error with guidance, got %v", err)
	}
	pkg, err := resolvePackage("com.example.app")
	if err != nil || pkg != "com.example.app" {
		t.Fatalf("resolvePackage = %q, %v", pkg, err)
	}
	// remembered on disk
	if got := rememberedPackage(); got != "com.example.app" {
		t.Errorf("remembered = %q", got)
	}
	// invalid input not remembered
	if _, err := resolvePackage("no dot"); err == nil {
		t.Error("invalid package should error")
	}
	if got := rememberedPackage(); got != "com.example.app" {
		t.Errorf("invalid input must not overwrite memory, got %q", got)
	}
	// omitted → remembered
	pkg, err = resolvePackage("")
	if err != nil || pkg != "com.example.app" {
		t.Errorf("resolvePackage(\"\") = %q, %v; want remembered", pkg, err)
	}
}

// ---- arg validation ----

func TestNormalizeReleaseStatus(t *testing.T) {
	cases := map[string]string{
		"completed": "completed", "COMPLETED": "completed",
		"inProgress": "inProgress", "in_progress": "inProgress", "IN PROGRESS": "inProgress",
		"halted": "halted", "draft": "draft",
	}
	for in, want := range cases {
		got, err := normalizeReleaseStatus(in)
		if err != nil || got != want {
			t.Errorf("normalizeReleaseStatus(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := normalizeReleaseStatus(""); err == nil || !strings.Contains(err.Error(), "status is required") {
		t.Errorf("empty status should error with guidance: %v", err)
	}
	if _, err := normalizeReleaseStatus("published"); err == nil {
		t.Error("unknown status should error")
	}
}

func TestNormalizeUserFraction(t *testing.T) {
	cases := map[float64]float64{0: 0, 0.1: 0.1, 1: 1, 10: 0.1, 100: 1, 25.5: 0.255}
	for in, want := range cases {
		got, err := normalizeUserFraction(in)
		if err != nil || got != want {
			t.Errorf("normalizeUserFraction(%v) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []float64{-1, 150} {
		if _, err := normalizeUserFraction(bad); err == nil {
			t.Errorf("normalizeUserFraction(%v) should error", bad)
		}
	}
}

func TestBuildTrackBody(t *testing.T) {
	body, err := buildTrackBody(trackUpdateArgs{
		Track: "production", Status: "in progress", VersionCodes: int64List{"5", "6"},
		UserFraction: 10, InAppUpdatePriority: 3,
		ReleaseNotes: []localizedText{{Language: "en-US", Text: "Bug fixes"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(body)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["track"] != "production" {
		t.Errorf("track = %v", m["track"])
	}
	rels := m["releases"].([]any)
	rel := rels[0].(map[string]any)
	if rel["status"] != "inProgress" {
		t.Errorf("status = %v", rel["status"])
	}
	vc := rel["versionCodes"].([]any)
	if len(vc) != 2 || vc[0] != "5" || vc[1] != "6" {
		t.Errorf("versionCodes = %v (must be strings for the API)", vc)
	}
	if rel["userFraction"] != 0.1 {
		t.Errorf("userFraction = %v, want 0.1 (percent converted)", rel["userFraction"])
	}
	if got := rel["inAppUpdatePriority"].(float64); got != 3 {
		t.Errorf("priority = %v", got)
	}
	if rel["name"] != nil {
		t.Errorf("name should be omitted, got %v", rel["name"])
	}

	if _, err := buildTrackBody(trackUpdateArgs{Track: "p", Status: "completed", VersionCodes: int64List{"1"}, UserFraction: 0.5}); err == nil {
		t.Error("userFraction with completed status should error")
	}
	if _, err := buildTrackBody(trackUpdateArgs{Track: "p", Status: "draft"}); err == nil || !strings.Contains(err.Error(), "versionCodes is required") {
		t.Errorf("missing versionCodes should error: %v", err)
	}
	if _, err := buildTrackBody(trackUpdateArgs{Track: "p", Status: "draft", VersionCodes: int64List{"1"}, InAppUpdatePriority: 9}); err == nil {
		t.Error("priority 9 should error")
	}
	if _, err := buildTrackBody(trackUpdateArgs{Track: "p", Status: "draft", VersionCodes: int64List{"1"}, ReleaseNotes: []localizedText{{Language: "en-US"}}}); err == nil {
		t.Error("release note without text should error")
	}
}

func TestInt64ListUnmarshal(t *testing.T) {
	var l int64List
	if err := json.Unmarshal([]byte(`[1, "2", 300000000000]`), &l); err != nil {
		t.Fatal(err)
	}
	if len(l) != 3 || l[0] != "1" || l[1] != "2" || l[2] != "300000000000" {
		t.Errorf("int64List = %v", l)
	}
	var bad int64List
	if err := json.Unmarshal([]byte(`[true]`), &bad); err == nil {
		t.Error("boolean items should error")
	}
}

func TestNormalizeImageType(t *testing.T) {
	cases := map[string]string{
		"phoneScreenshots":     "phoneScreenshots",
		"PHONE_SCREENSHOTS":    "phoneScreenshots",
		"phone screenshots":    "phoneScreenshots",
		"icon":                 "icon",
		"FEATURE_GRAPHIC":      "featureGraphic",
		"sevenInchScreenshots": "sevenInchScreenshots",
	}
	for in, want := range cases {
		got, err := normalizeImageType(in)
		if err != nil || got != want {
			t.Errorf("normalizeImageType(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := normalizeImageType("banner"); err == nil || !strings.Contains(err.Error(), "allowed:") {
		t.Errorf("unknown type should error with allowed list: %v", err)
	}
}

func TestToEpochMS(t *testing.T) {
	// 2026-01-31T00:00:00Z → 1769817600 s
	got, err := toEpochMS("2026-01-31T00:00:00Z")
	if err != nil || got != "1769817600000" {
		t.Errorf("toEpochMS(RFC3339) = %q, %v", got, err)
	}
	got, err = toEpochMS("2026-01-31")
	if err != nil || got != "1769817600000" {
		t.Errorf("toEpochMS(date) = %q, %v", got, err)
	}
	got, err = toEpochMS("1769827200000")
	if err != nil || got != "1769827200000" {
		t.Errorf("toEpochMS(epoch) = %q, %v", got, err)
	}
	if got, _ := toEpochMS(""); got != "" {
		t.Errorf("empty should stay empty, got %q", got)
	}
	if _, err := toEpochMS("yesterday"); err == nil {
		t.Error("garbage should error")
	}
}

// ---- edit lifecycle ----

func TestEditLifecycleCommit(t *testing.T) {
	var ops []string
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		ops = append(ops, r.Method+" "+r.URL.EscapedPath()+r.URL.RawQuery)
		switch {
		case r.Method == "POST" && r.URL.Path == "/applications/com.example.app/edits":
			w.Write([]byte(`{"id":"e1","expiryTimeSeconds":"999"}`))
		case r.Method == "PUT" && r.URL.Path == "/applications/com.example.app/edits/e1/tracks/production":
			w.Write([]byte(`{"track":"production","releases":[{"status":"completed","versionCodes":["5"]}]}`))
		case r.Method == "POST" && r.URL.Path == "/applications/com.example.app/edits/e1:commit":
			w.Write([]byte(`{"id":"e1"}`))
		case r.Method == "DELETE" && r.URL.Path == "/applications/com.example.app/edits/e1":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"message":"gone","status":"NOT_FOUND"}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(400)
		}
	})
	c := testClient()
	var got track
	err := c.withEdit(context.Background(), "com.example.app", editOptions{Commit: true}, func(editID string) error {
		if editID != "e1" {
			t.Errorf("editID = %q", editID)
		}
		var err error
		got, err = c.updateTrack(context.Background(), "com.example.app", editID, "production", map[string]any{})
		return err
	})
	if err != nil {
		t.Fatalf("withEdit: %v", err)
	}
	if len(got.Releases) != 1 || got.Releases[0].Status != "completed" {
		t.Errorf("decoded %+v", got)
	}
	// POST edits → PUT track → POST :commit → DELETE (404 ignored)
	want := []string{
		"POST /applications/com.example.app/edits",
		"PUT /applications/com.example.app/edits/e1/tracks/production",
		"POST /applications/com.example.app/edits/e1:commit",
		"DELETE /applications/com.example.app/edits/e1",
	}
	if strings.Join(ops, " | ") != strings.Join(want, " | ") {
		t.Errorf("ops = %v\nwant %v", ops, want)
	}
}

func TestEditLifecycleValidateOnly(t *testing.T) {
	var ops []string
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		ops = append(ops, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == "POST" && r.URL.Path == "/applications/com.example.app/edits":
			w.Write([]byte(`{"id":"e1"}`))
		case r.Method == "POST" && r.URL.Path == "/applications/com.example.app/edits/e1:validate":
			w.Write([]byte(`{"id":"e1"}`))
		case r.Method == "DELETE":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(400)
		}
	})
	c := testClient()
	err := c.withEdit(context.Background(), "com.example.app", editOptions{Commit: true, ValidateOnly: true}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ops, "|") != "POST /applications/com.example.app/edits|POST /applications/com.example.app/edits/e1:validate|DELETE /applications/com.example.app/edits/e1" {
		t.Errorf("ops = %v", ops)
	}
}

func TestEditLifecycleCleanupOnFnError(t *testing.T) {
	var deleted bool
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/applications/com.example.app/edits":
			w.Write([]byte(`{"id":"e1"}`))
		case r.Method == "DELETE":
			deleted = true
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(400)
		}
	})
	c := testClient()
	err := c.withEdit(context.Background(), "com.example.app", editOptions{Commit: true}, func(string) error {
		return os.ErrPermission
	})
	if err == nil {
		t.Fatal("fn error should propagate")
	}
	if !deleted {
		t.Error("edit must be deleted even when fn fails (release the account-wide edit lock)")
	}
}

func TestEditConflictHint(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":{"message":"Another edit is in progress","status":"CONFLICT"}}`))
	})
	c := testClient()
	err := c.withEdit(context.Background(), "com.example.app", editOptions{}, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "only one edit can be open") {
		t.Errorf("conflict should carry a hint, got %v", err)
	}
}

// ---- uploads ----

func TestResumableBundleUpload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.aab")
	content := []byte("PK\x03\x04 fake bundle payload")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	var gotBody []byte
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		gotBody = data
		w.Write([]byte(`{"versionCode": 42, "sha1": "abc", "sha256": "def"}`))
	}))
	defer srv2.Close()

	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/bundles") && r.URL.Query().Get("uploadType") == "resumable" {
			if got, want := r.Header.Get("X-Upload-Content-Length"), fmt.Sprintf("%d", len(content)); got != want {
				t.Errorf("X-Upload-Content-Length = %q, want %q", got, want)
			}
			if got := r.Header.Get("X-Upload-Content-Type"); got != "application/octet-stream" {
				t.Errorf("X-Upload-Content-Type = %q", got)
			}
			w.Header().Set("Location", srv2.URL+"/upload-session")
			w.WriteHeader(http.StatusOK)
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(400)
	})

	c := testClient()
	raw, err := c.uploadFileResumable(context.Background(), "/applications/com.example.app/edits/e1/bundles", path, nil)
	if err != nil {
		t.Fatal(err)
	}
	var b bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if b.VersionCode != 42 {
		t.Errorf("versionCode = %d", b.VersionCode)
	}
	if string(gotBody) != string(content) {
		t.Errorf("uploaded body = %q, want original bytes", gotBody)
	}
}

func TestValidateArtifactFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app.aab")
	if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateArtifactFile(p, ".aab"); err != nil {
		t.Errorf("valid aab: %v", err)
	}
	if err := validateArtifactFile(p, ".apk"); err == nil {
		t.Error("wrong extension should error")
	}
	if err := validateArtifactFile("", ".apk"); err == nil {
		t.Error("empty path should error")
	}
	if err := validateArtifactFile(filepath.Join(dir, "missing.aab"), ".aab"); err == nil {
		t.Error("missing file should error")
	}
	if err := validateArtifactFile(dir, ".aab"); err == nil {
		t.Error("directory should error")
	}
	empty := filepath.Join(dir, "empty.apk")
	os.WriteFile(empty, nil, 0o644)
	if err := validateArtifactFile(empty, ".apk"); err == nil {
		t.Error("empty file should error")
	}
}

func TestReadImageFile(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "icon.png")
	os.WriteFile(png, []byte("pngdata"), 0o644)
	ct, data, err := readImageFile(png)
	if err != nil || ct != "image/png" || string(data) != "pngdata" {
		t.Errorf("readImageFile = %q, %q, %v", ct, data, err)
	}
	txt := filepath.Join(dir, "icon.txt")
	os.WriteFile(txt, []byte("x"), 0o644)
	if _, _, err := readImageFile(txt); err == nil {
		t.Error("non-image extension should error")
	}
}

func TestUploadBytesNoRetryOnHTTPError(t *testing.T) {
	old := googleapi.RetryBackoff
	googleapi.RetryBackoff = time.Millisecond
	t.Cleanup(func() { googleapi.RetryBackoff = old })
	var calls int32
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"boom","status":"INTERNAL"}}`))
	})
	c := testClient()
	_, err := c.uploadBytes(context.Background(), c.HTTP, http.MethodPost, apiBase+"/x", "image/png", 1, strings.NewReader("d"))
	if err == nil {
		t.Fatal("500 must surface as error")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls = %d; media uploads must NOT retry after a server response (duplicate risk)", calls)
	}
}

// ---- retry & decode ----

func TestListReviewsDecodesPagination(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("maxResults") != "10" {
			t.Errorf("maxResults = %q", r.URL.Query().Get("maxResults"))
		}
		w.Write([]byte(`{"reviews":[{"reviewId":"r1","authorName":"An","comments":[{"userComment":{"text":"Great app","starRating":5,"lastModified":{"seconds":"1730000000","nanos":0}}},{"developerComment":{"text":"Thanks!","lastModified":{"seconds":"1730000100","nanos":0}}}]}],"tokenPagination":{"nextPageToken":"tok2"}}`))
	})
	c := testClient()
	reviews, next, err := c.listReviews(context.Background(), "com.example.app", reviewsArgs{MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	if next != "tok2" {
		t.Errorf("next = %q", next)
	}
	if len(reviews) != 1 || reviews[0].Comments[0].UserComment.StarRating != 5 {
		t.Errorf("decoded %+v", reviews)
	}
	out := renderReviews("com.example.app", reviews, next)
	if !strings.Contains(out, "5★") || !strings.Contains(out, "Thanks!") || !strings.Contains(out, "tok2") {
		t.Errorf("rendering wrong:\n%s", out)
	}
}

// ---- renders ----

func TestRenderTracks(t *testing.T) {
	codes := make([]string, 15)
	for i := range codes {
		codes[i] = string(rune('1' + i))
	}
	out := renderTracks("com.example.app", []track{
		{Track: "production", Releases: []trackRelease{
			{Status: "completed", VersionCodes: []string{"1", "2"}},
			{Name: "1.2.1", Status: "inProgress", VersionCodes: codes, UserFraction: 0.1, InAppUpdatePriority: 2,
				ReleaseNotes: []localizedText{{Language: "vi", Text: "Sửa lỗi"}}},
		}},
		{Track: "internal", Releases: []trackRelease{{Status: "draft", VersionCodes: []string{"9"}}}},
	})
	if !strings.Contains(out, "▶ production") || !strings.Contains(out, "▶ internal") {
		t.Errorf("track headers missing:\n%s", out)
	}
	if !strings.Contains(out, "rollout=10.0%") || !strings.Contains(out, "priority=2") {
		t.Errorf("fraction/priority missing:\n%s", out)
	}
	if !strings.Contains(out, "+5 more") {
		t.Errorf("version code truncation missing:\n%s", out)
	}
	if !strings.Contains(out, "note vi: Sửa lỗi") {
		t.Errorf("release notes missing:\n%s", out)
	}
	if !strings.Contains(out, "🚀") {
		t.Errorf("inProgress emoji missing:\n%s", out)
	}
	empty := renderTracks("com.example.app", nil)
	if !strings.Contains(empty, "0 track(s)") {
		t.Errorf("empty render:\n%s", empty)
	}
}

func TestRenderTrackUpdateValidateOnly(t *testing.T) {
	out := renderTrackUpdate("production", track{Track: "production", Releases: []trackRelease{{Status: "halted", VersionCodes: []string{"1"}}}}, true)
	if !strings.Contains(out, "VALIDATED ONLY") || !strings.Contains(out, "NOT committed") {
		t.Errorf("validate-only banner missing:\n%s", out)
	}
	if !strings.Contains(out, "halted") {
		t.Errorf("release missing:\n%s", out)
	}
}

func TestRenderVoided(t *testing.T) {
	out := renderVoided("com.example.app", []voidedPurchase{{
		OrderID: "GPA.3311-1234", PurchaseToken: "tokenlongvalue123456789",
		PurchaseTimeMillis: "1730000000000", VoidedTimeMillis: "1730100000000",
		VoidedReason: 10, VoidedSource: 2, VoidedQuantity: 1,
	}}, tokenPagination{NextPageToken: "p2"})
	if !strings.Contains(out, "GPA.3311-1234") || !strings.Contains(out, "bought by mistake") || !strings.Contains(out, "user") {
		t.Errorf("rendering wrong:\n%s", out)
	}
	if !strings.Contains(out, `token="p2"`) {
		t.Errorf("pagination hint missing:\n%s", out)
	}
	empty := renderVoided("com.example.app", nil, tokenPagination{})
	if !strings.Contains(empty, "No voided purchases") {
		t.Errorf("empty render:\n%s", empty)
	}
}

func TestRenderSubscriptionPurchase(t *testing.T) {
	out := renderSubscriptionPurchase(subscriptionPurchaseV2{
		SubscriptionState:    "SUBSCRIPTION_STATE_IN_GRACE_PERIOD",
		AcknowledgementState: "ACKNOWLEDGEMENT_STATE_ACKNOWLEDGED",
		RegionCode:           "VN", StartTime: "2026-10-01T00:00:00Z",
		LinkedPurchaseToken: "old-token",
		LineItems: []subscriptionLineItem{{
			ProductID: "premium", ExpiryTime: "2026-11-01T00:00:00Z",
			OfferDetails:     &offerDetails{BasePlanID: "monthly"},
			AutoRenewingPlan: &autoRenewingPlan{AutoRenewEnabled: false, RecurringPrice: money{Units: "2", CurrencyCode: "USD"}},
		}},
	})
	if !strings.Contains(out, "grace period") || !strings.Contains(out, "acknowledged") {
		t.Errorf("state rendering wrong:\n%s", out)
	}
	if !strings.Contains(out, "replaced by an upgrade/downgrade") {
		t.Errorf("linked token warning missing:\n%s", out)
	}
	if !strings.Contains(out, "auto-renew: OFF") || !strings.Contains(out, "monthly") {
		t.Errorf("line item rendering wrong:\n%s", out)
	}
}

func TestRenderSubscriptions(t *testing.T) {
	out := renderSubscriptions("com.example.app", []subscription{{
		ProductID: "premium",
		Listings:  []subscriptionListing{{LanguageCode: "en-US", Title: "Premium"}},
		BasePlans: []basePlan{
			{BasePlanID: "monthly", State: "ACTIVE", AutoRenewingBasePlanType: map[string]any{}},
			{BasePlanID: "yearly", State: "DRAFT", PrepaidBasePlanType: map[string]any{}},
		},
	}}, "next-token")
	if !strings.Contains(out, "premium — Premium") || !strings.Contains(out, "monthly: active (auto-renewing)") || !strings.Contains(out, "yearly: draft (prepaid)") {
		t.Errorf("rendering wrong:\n%s", out)
	}
	if !strings.Contains(out, "pageToken=\"next-token\"") {
		t.Errorf("pagination hint missing:\n%s", out)
	}
}

// ---- registration smoke test (jsonschema reflection over arg types) ----

func TestServerBuilds(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	registerTools(s)
	registerPrompts(s)
}

// Image endpoints have no "/images/" segment: .../listings/{language}/{imageType}.
func TestImagePaths(t *testing.T) {
	var got []string
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		w.Write([]byte(`{"images":[],"image":{"id":"i1"}}`))
	})
	c, ctx := testClient(), context.Background()
	if _, err := c.listImages(ctx, "com.x", "e1", "en-US", "phoneScreenshots"); err != nil {
		t.Fatal(err)
	}
	if err := c.deleteImages(ctx, "com.x", "e1", "en-US", "phoneScreenshots", "i1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.uploadImage(ctx, "com.x", "e1", "en-US", "icon", "image/png", []byte("png")); err != nil {
		t.Fatal(err)
	}
	want := "GET /applications/com.x/edits/e1/listings/en-US/phoneScreenshots\n" +
		"DELETE /applications/com.x/edits/e1/listings/en-US/phoneScreenshots/i1\n" +
		"POST /applications/com.x/edits/e1/listings/en-US/icon"
	if strings.Join(got, "\n") != want {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}

func TestTruncateRuneSafe(t *testing.T) {
	long := strings.Repeat("tiếng Việt ", 50)
	got := truncate(long, 30)
	if !utf8.ValidString(got) {
		t.Errorf("truncate produced invalid UTF-8: %q", got)
	}
	if r := len([]rune(strings.TrimSuffix(got, "…"))); r > 30 {
		t.Errorf("truncate kept %d runes, want <= 30", r)
	}
	if got := truncate("ngắn", 30); got != "ngắn" {
		t.Errorf("short string changed: %q", got)
	}
}
