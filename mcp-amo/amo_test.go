package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// isolate points the config dir at a temp dir and clears credential env vars.
func isolate(t *testing.T) string {
	t.Helper()
	for _, v := range []string{"AMO_JWT_ISSUER", "AMO_JWT_SECRET", "WEB_EXT_API_KEY", "WEB_EXT_API_SECRET", "AMO_API_BASE"} {
		t.Setenv(v, "")
	}
	old := configBase
	configBase = t.TempDir()
	t.Cleanup(func() { configBase = old })
	dir, _ := configDir()
	return dir
}

func TestLoadCredentials(t *testing.T) {
	dir := isolate(t)
	if _, err := loadCredentials(); !errors.Is(err, errNoCredentials) || !strings.Contains(err.Error(), "mcp-amo setup") {
		t.Fatalf("no key: err = %v, want errNoCredentials with setup steps", err)
	}

	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(`{"issuer":"user:1:2","secret":"file-secret"}`), 0o600)
	c, err := loadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if c.Issuer != "user:1:2" || c.Secret != "file-secret" || c.APIBase != defaultAPIBase {
		t.Errorf("file creds = %+v", c)
	}

	// web-ext's names override the file; AMO_* override web-ext's.
	t.Setenv("WEB_EXT_API_KEY", "user:3:4")
	t.Setenv("WEB_EXT_API_SECRET", "webext-secret")
	if c, _ = loadCredentials(); c.Issuer != "user:3:4" {
		t.Errorf("WEB_EXT creds not used: %+v", c)
	}
	t.Setenv("AMO_JWT_ISSUER", "user:5:6")
	t.Setenv("AMO_JWT_SECRET", "amo-secret")
	t.Setenv("AMO_API_BASE", "https://addons.allizom.org/api/v5/")
	if c, _ = loadCredentials(); c.Issuer != "user:5:6" || c.Secret != "amo-secret" || c.APIBase != "https://addons.allizom.org/api/v5" {
		t.Errorf("AMO env creds = %+v", c)
	}

	t.Setenv("AMO_JWT_SECRET", "")
	if _, err := loadCredentials(); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("issuer without secret: err = %v", err)
	}
	t.Setenv("AMO_JWT_SECRET", "x")
	t.Setenv("AMO_API_BASE", "not a url")
	if _, err := loadCredentials(); err == nil {
		t.Error("bad API base accepted")
	}
}

func TestSignJWT(t *testing.T) {
	c := &credentials{Issuer: "user:1:2", Secret: "s3cret"}
	now := time.Unix(1_700_000_000, 0)
	tok, err := signJWT(c, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q has %d parts", tok, len(parts))
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) != parts[2] {
		t.Error("HS256 signature does not verify")
	}
	var claims struct {
		Iss string `json:"iss"`
		Jti string `json:"jti"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	data, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if err := json.Unmarshal(data, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Iss != "user:1:2" || claims.Iat != now.Unix() || claims.Exp-claims.Iat > 300 || claims.Exp <= claims.Iat || claims.Jti == "" {
		t.Errorf("claims = %+v", claims)
	}
	tok2, _ := signJWT(c, now)
	if tok2 == tok {
		t.Error("two tokens share a jti")
	}
}

func TestParseAddonRef(t *testing.T) {
	good := map[string]string{
		"12345":                                  "12345",
		"my-addon":                               "my-addon",
		" ublock-origin ":                        "ublock-origin",
		"{ec8030f7-c20a-464f-9b0e-13a3a9e97384}": "{ec8030f7-c20a-464f-9b0e-13a3a9e97384}",
		"uBlock0@raymondhill.net":                "uBlock0@raymondhill.net",
		"https://addons.mozilla.org/en-US/firefox/addon/ublock-origin/":                           "ublock-origin",
		"https://addons.mozilla.org/en-US/developers/addon/my-addon/versions?page=2":              "my-addon",
		"https://addons.mozilla.org/vi/firefox/addon/%7Bec8030f7-c20a-464f-9b0e-13a3a9e97384%7D/": "{ec8030f7-c20a-464f-9b0e-13a3a9e97384}",
	}
	for in, want := range good {
		got, err := parseAddonRef(in)
		if err != nil || got != want {
			t.Errorf("parseAddonRef(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"a b", "../x", "https://example.com/foo", "a/b"} {
		if _, err := parseAddonRef(in); err == nil {
			t.Errorf("parseAddonRef(%q) succeeded, want error", in)
		}
	}
}

func TestRememberAddon(t *testing.T) {
	isolate(t)
	if _, err := resolveAddon(""); err == nil {
		t.Fatal("empty addon with nothing remembered: want error")
	}
	rememberAddon("my-addon")
	if got, _ := resolveAddon(""); got != "my-addon" {
		t.Errorf("remembered = %q", got)
	}
	rememberAddon("")
	if _, err := resolveAddon(""); err == nil {
		t.Error("forgotten addon still resolves")
	}
}

func TestVersionRef(t *testing.T) {
	for in, want := range map[string]string{"123": "123", "1.2.0": "1.2.0", "v2": "v2", "2a": "v2a", " 1.0 ": "1.0"} {
		if got, err := versionRef(in); err != nil || got != want {
			t.Errorf("versionRef(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := versionRef(""); err == nil {
		t.Error("empty version accepted")
	}
}

func TestDecodeError(t *testing.T) {
	cases := map[string]string{
		`{"detail": "Authentication credentials were not provided."}`:                "Authentication credentials were not provided.",
		`{"version": {"upload": ["Upload already submitted."]}, "slug": ["Taken."]}`: "slug: Taken.; version.upload: Upload already submitted.",
		`{"non_field_errors": ["Bad."], "error": "Read only"}`:                       "Read only; Bad.",
		`<html>oops</html>`: "<html>oops</html>",
		`{"detail": "Disabled", "is_disabled_by_mozilla": true, "code": "x"}`: "Disabled; is_disabled_by_mozilla",
	}
	for body, want := range cases {
		if err := decodeError(400, []byte(body)); !strings.Contains(err.Error(), want) {
			t.Errorf("decodeError(%s) = %q, want it to contain %q", body, err, want)
		}
	}
	if err := decodeError(401, []byte(`{"detail":"x"}`)); !strings.Contains(err.Error(), "amo_auth_status") {
		t.Errorf("401 hint missing: %v", err)
	}
}

func TestTranslatedAndLinks(t *testing.T) {
	got := translatedArg(map[string]string{"en-US": "Hi", "fr": ""})
	if got["en-US"] != "Hi" || got["fr"] != nil {
		t.Errorf("translatedArg = %v", got)
	}
	if translatedArg(nil) != nil {
		t.Error("nil map should give nil")
	}
	s := func(v string) *string { return &v }
	tr := translated{"de": s("Hallo"), "en-US": s("Hello"), "_default": s("en-US"), "fr": nil}
	if tr.pick("de") != "Hallo" || tr.pick("vi") != "Hello" || strings.Join(tr.locales(), ",") != "de,en-US" {
		t.Errorf("pick/locales wrong: %q %q %v", tr.pick("de"), tr.pick("vi"), tr.locales())
	}
	for raw, want := range map[string]string{
		`{"url": {"en-US": "https://a.example"}, "outgoing": {"en-US": "https://o"}}`: "https://a.example",
		`{"url": "https://paypal.me/x", "outgoing": "https://o"}`:                     "https://paypal.me/x",
		`"https://plain"`: "https://plain",
		`null`:            "",
	} {
		if got := linkURL(json.RawMessage(raw), "en-US"); got != want {
			t.Errorf("linkURL(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestBodies(t *testing.T) {
	b, err := listingArgs{Name: map[string]string{"en-US": "X"}, Tags: []string{}, Slug: "my-x", Extra: map[string]any{"foo": 1}}.body()
	if err != nil || b["slug"] != "my-x" || b["foo"] != 1 || b["tags"] == nil {
		t.Errorf("listing body = %v, %v", b, err)
	}
	if _, err := (listingArgs{Slug: "1234"}).body(); err == nil {
		t.Error("all-digit slug accepted")
	}
	if _, err := (versionFields{License: "MIT", CustomLicenseText: map[string]string{"en-US": "x"}}).body(); err == nil {
		t.Error("license + custom license accepted")
	}
	if _, err := (versionFields{Compatibility: map[string]appRange{"chrome": {}}}).body(); err == nil {
		t.Error("unknown app accepted")
	}
	vb, _ := versionFields{Compatibility: map[string]appRange{"firefox": {Min: "115.0"}}, ReleaseNotes: map[string]string{"en-US": "n"}}.body()
	data, _ := json.Marshal(vb)
	if string(data) != `{"compatibility":{"firefox":{"min":"115.0"}},"release_notes":{"en-US":"n"}}` {
		t.Errorf("version body = %s", data)
	}
}

// writeExt makes an extension folder with a manifest and a dot dir.
func writeExt(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"manifest_version": 3, "version": "1.2.3",
		"browser_specific_settings": {"gecko": {"id": "x@example.com"}}}`), 0o644)
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref"), 0o644)
	os.WriteFile(filepath.Join(dir, "bg.js"), []byte("1"), 0o644)
	return dir
}

func TestLoadPackageFolder(t *testing.T) {
	p, err := loadPackage(writeExt(t))
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != "1.2.3" || p.GeckoID != "x@example.com" || !p.Manifest || !strings.HasSuffix(p.Name, ".zip") {
		t.Errorf("pkg = %+v", p)
	}
	zr, _ := zip.NewReader(bytes.NewReader(p.Data), int64(len(p.Data)))
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, ".") {
			t.Errorf("dot file %s was zipped", f.Name)
		}
	}
	notZip := filepath.Join(t.TempDir(), "x.xpi")
	os.WriteFile(notZip, []byte("nope"), 0o644)
	if _, err := loadPackage(notZip); err == nil {
		t.Error("non-zip accepted")
	}
	if _, _, err := loadSource(notZip); err == nil {
		t.Error("source .xpi accepted")
	}
}

// fakeAMO serves the endpoints of the upload → version flow.
func fakeAMO(t *testing.T) (*amoClient, *[]string) {
	t.Helper()
	var calls []string
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "JWT ") {
			w.WriteHeader(401)
			io.WriteString(w, `{"detail":"no auth"}`)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v5/addons/upload/":
			if err := r.ParseMultipartForm(1 << 20); err != nil || r.FormValue("channel") != "listed" {
				w.WriteHeader(400)
				io.WriteString(w, `{"channel":["bad"]}`)
				return
			}
			if _, _, err := r.FormFile("upload"); err != nil {
				w.WriteHeader(400)
				return
			}
			io.WriteString(w, `{"uuid":"0123456789abcdef0123456789abcdef","channel":"listed","processed":false}`)
		case r.Method == "GET" && r.URL.Path == "/api/v5/addons/upload/0123456789abcdef0123456789abcdef/":
			if polls.Add(1) < 2 {
				io.WriteString(w, `{"uuid":"0123456789abcdef0123456789abcdef","processed":false}`)
				return
			}
			io.WriteString(w, `{"uuid":"0123456789abcdef0123456789abcdef","channel":"listed","processed":true,"valid":true,"version":"1.2.3",
				"validation":{"errors":0,"warnings":1,"notices":0,"messages":[{"type":"warning","message":"Unsafe <code>innerHTML</code>","file":"bg.js","line":3}]}}`)
		case r.Method == "POST" && r.URL.Path == "/api/v5/addons/addon/my-addon/versions/":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["upload"] != "0123456789abcdef0123456789abcdef" {
				w.WriteHeader(400)
				io.WriteString(w, `{"upload":["missing"]}`)
				return
			}
			io.WriteString(w, `{"id":77,"version":"1.2.3","channel":"listed","file":{"status":"unreviewed"}}`)
		case r.Method == "PATCH" && r.URL.Path == "/api/v5/addons/addon/my-addon/versions/77/":
			if _, _, err := r.FormFile("source"); err != nil {
				w.WriteHeader(400)
				return
			}
			io.WriteString(w, `{"id":77}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"detail":"Not found."}`)
		}
	}))
	t.Cleanup(srv.Close)
	old := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = old })
	creds := &credentials{Issuer: "user:1:2", Secret: "s"}
	hc := &http.Client{Transport: &jwtTransport{creds: creds, base: http.DefaultTransport}, Timeout: 5 * time.Second}
	return wrap(hc, srv.URL+"/api/v5", true), &calls
}

func TestUploadAndCreateVersion(t *testing.T) {
	c, calls := fakeAMO(t)
	ctx := context.Background()
	dir := writeExt(t)
	uuid, report, err := resolveUpload(ctx, c, submitArgs{Path: dir, WaitSeconds: 10})
	if err != nil {
		t.Fatalf("resolveUpload: %v\n%s", err, report)
	}
	if uuid != "0123456789abcdef0123456789abcdef" || !strings.Contains(report, "VALID") || !strings.Contains(report, "Unsafe innerHTML [bg.js:3]") {
		t.Errorf("uuid %q, report:\n%s", uuid, report)
	}
	var v addonVersion
	if err := c.call(ctx, http.MethodPost, addonPath("my-addon")+"versions/", nil, map[string]any{"upload": uuid}, &v); err != nil {
		t.Fatal(err)
	}
	if v.ID != 77 || fileStatus(&v) != "unreviewed" {
		t.Errorf("version = %+v", v)
	}
	if err := attachSource(ctx, c, "my-addon", 77, dir); err != nil {
		t.Errorf("attachSource: %v", err)
	}
	if err := c.call(ctx, http.MethodGet, "/nope/", nil, nil, nil); err == nil || !strings.Contains(err.Error(), "404: Not found.") {
		t.Errorf("404 err = %v", err)
	}
	if len(*calls) < 6 {
		t.Errorf("calls = %v", *calls)
	}

	if _, _, err := resolveUpload(ctx, c, submitArgs{}); err == nil {
		t.Error("neither upload nor path: want error")
	}
	if _, _, err := resolveUpload(ctx, c, submitArgs{Path: dir, Channel: "beta"}); err == nil {
		t.Error("bad channel accepted")
	}
}

func TestRenderUploadInvalid(t *testing.T) {
	var up upload
	json.Unmarshal([]byte(`{"uuid":"u","processed":true,"valid":false,"validation":{"errors":1,"warnings":0,"notices":1,
		"messages":[{"type":"notice","message":"n"},{"type":"error","message":"Missing <b>manifest</b>","description":["Add one."]}]}}`), &up)
	out := renderUpload(up)
	if !strings.Contains(out, "INVALID") || !strings.Contains(out, "ERROR: Missing manifest — Add one.") ||
		strings.Index(out, "ERROR") > strings.Index(out, "NOTICE") {
		t.Errorf("renderUpload:\n%s", out)
	}
	if err := uploadReady(up); err == nil {
		t.Error("invalid upload ready")
	}
}
