package firefoxaddons

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
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homielab/homielab-mcp/internal/googleapi"
)

// uploadTimeout bounds one multipart upload (package, source, image).
const uploadTimeout = 15 * time.Minute

type amoClient struct {
	*googleapi.Client
	base   string
	authed bool
}

// newAMOClient returns a client signed with the configured API key.
func newAMOClient() (*amoClient, error) {
	c, err := loadCredentials()
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Transport: &jwtTransport{creds: c, base: http.DefaultTransport}}
	return wrap(hc, c.APIBase, true), nil
}

// newPublicClient signs requests when a key is configured and is anonymous
// otherwise (search, public add-ons, ratings need no key).
func newPublicClient() (*amoClient, error) {
	c, err := newAMOClient()
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, errNoCredentials) {
		return nil, err
	}
	base := firstNonEmpty(os.Getenv("AMO_API_BASE"), defaultAPIBase)
	if err := checkAPIBase(base); err != nil {
		return nil, err
	}
	return wrap(&http.Client{}, strings.TrimSuffix(base, "/"), false), nil
}

func wrap(hc *http.Client, base string, authed bool) *amoClient {
	g := googleapi.New(hc, "AMO API")
	g.DecodeError = decodeError
	return &amoClient{Client: g, base: base, authed: authed}
}

// decodeError reads AMO's (Django REST framework) error bodies:
// {"detail": "...", "code": "..."}, {"error": "..."} or field errors
// {"field": ["msg"], "version": {"upload": ["msg"]}, "non_field_errors": [...]}.
func decodeError(status int, body []byte) error {
	var v any
	msg := ""
	if json.Unmarshal(body, &v) == nil {
		msg = strings.Join(flattenErrors("", v), "; ")
	}
	if msg == "" {
		msg = googleapi.Snippet(body)
	}
	out := fmt.Sprintf("AMO API %d: %s", status, msg)
	switch status {
	case http.StatusUnauthorized:
		out += "\nThe JWT was rejected or missing: check the issuer and secret (amo_auth_status), and that the Mac's clock is right."
	case http.StatusForbidden:
		out += "\nThis account is not allowed to do this (not an author/owner of the add-on, or the developer agreement is not accepted on AMO)."
	case http.StatusServiceUnavailable:
		out += "\nAMO may be in read-only maintenance mode: retry later (amo_auth_status shows the site notice)."
	}
	return errors.New(out)
}

func flattenErrors(prefix string, v any) []string {
	join := func(k string) string {
		switch {
		case prefix == "":
			return k
		case k == "":
			return prefix
		}
		return prefix + "." + k
	}
	var out []string
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			switch k {
			case "detail", "error", "non_field_errors":
				out = append(out, flattenErrors(prefix, x[k])...)
			case "code", "is_disabled_by_developer", "is_disabled_by_mozilla":
				if b, ok := x[k].(bool); ok && b {
					out = append(out, join(k))
				}
			default:
				out = append(out, flattenErrors(join(k), x[k])...)
			}
		}
	case []any:
		for _, e := range x {
			out = append(out, flattenErrors(prefix, e)...)
		}
	case string:
		if prefix != "" {
			return []string{prefix + ": " + x}
		}
		return []string{x}
	case nil:
	default:
		out = append(out, fmt.Sprintf("%s: %v", prefix, x))
	}
	return out
}

// url builds base+path (path relative to /api/v5, trailing slash added).
func (c *amoClient) url(path string, q url.Values) string {
	u := googleapi.JoinURL(c.base, path)
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// call sends a JSON request and decodes the response into out (if non-nil).
func (c *amoClient) call(ctx context.Context, method, path string, q url.Values, body, out any) error {
	data, _, err := c.Do(ctx, method, c.url(path, q), body)
	if err != nil || out == nil || len(data) == 0 {
		return err
	}
	return json.Unmarshal(data, out)
}

// filePart is one file field of a multipart request.
type filePart struct {
	Field, Name string
	Data        []byte
}

// multipartCall sends fields and files as multipart/form-data. Uploads are
// not retried once a response arrived (a retry could submit twice); only
// transport failures are retried.
func (c *amoClient) multipartCall(ctx context.Context, method, path string, fields map[string]string, files []filePart, out any) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := mw.WriteField(k, fields[k]); err != nil {
			return err
		}
	}
	for _, f := range files {
		w, err := mw.CreateFormFile(f.Field, f.Name)
		if err != nil {
			return err
		}
		if _, err := w.Write(f.Data); err != nil {
			return err
		}
	}
	if err := mw.Close(); err != nil {
		return err
	}
	payload := buf.Bytes()
	u := c.url(path, nil)
	hc := *c.HTTP
	hc.Timeout = uploadTimeout
	var err error
	for attempt := 1; attempt <= googleapi.MaxRetries; attempt++ {
		if attempt > 1 {
			if werr := googleapi.WaitBackoff(ctx, attempt, 0); werr != nil {
				return werr
			}
			log.Printf("retrying %s %s after transport error: %v", method, u, err)
		}
		req, rerr := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
		if rerr != nil {
			return rerr
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
		resp, derr := hc.Do(req)
		if derr != nil {
			err = derr
			continue
		}
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, googleapi.MaxResponseBody))
		resp.Body.Close()
		if rerr != nil {
			return rerr
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return decodeError(resp.StatusCode, data)
		}
		if out == nil || len(data) == 0 {
			return nil
		}
		return json.Unmarshal(data, out)
	}
	return err
}

// ---- resources ----

// translated is a localized field: locale → text (null = missing).
type translated map[string]*string

// pick returns the text in locale, else the default locale, en-US, or any.
func (t translated) pick(locales ...string) string {
	for _, l := range append(locales, "en-US") {
		if v := t[l]; v != nil && *v != "" {
			return *v
		}
	}
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := t[k]; v != nil && *v != "" && !strings.HasPrefix(k, "_") {
			return *v
		}
	}
	return ""
}

func (t translated) locales() []string {
	var out []string
	for k, v := range t {
		if v != nil && !strings.HasPrefix(k, "_") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

type profile struct {
	ID               int    `json:"id"`
	Username         string `json:"username"`
	Name             string `json:"name"`
	Email            string `json:"email"`
	IsAddonDeveloper bool   `json:"is_addon_developer"`
	NumAddonsListed  int    `json:"num_addons_listed"`
	URL              string `json:"url"`
}

type siteStatus struct {
	ReadOnly                  bool    `json:"read_only"`
	Notice                    *string `json:"notice"`
	SubmitNotificationWarning *string `json:"submit_notification_warning"`
}

type fileInfo struct {
	ID                                int             `json:"id"`
	Created                           string          `json:"created"`
	Hash                              string          `json:"hash"`
	Size                              int64           `json:"size"`
	Status                            json.RawMessage `json:"status"`
	URL                               string          `json:"url"`
	Permissions                       []string        `json:"permissions"`
	OptionalPermissions               []string        `json:"optional_permissions"`
	HostPermissions                   []string        `json:"host_permissions"`
	DataCollectionPermissions         []string        `json:"data_collection_permissions"`
	OptionalDataCollectionPermissions []string        `json:"optional_data_collection_permissions"`
}

type license struct {
	IsCustom bool       `json:"is_custom"`
	Name     translated `json:"name"`
	Slug     *string    `json:"slug"`
	URL      *string    `json:"url"`
}

type appRange struct {
	Min string `json:"min,omitempty" jsonschema:"Minimum app version, e.g. 115.0. Omit for the manifest value or the default."`
	Max string `json:"max,omitempty" jsonschema:"Maximum app version, e.g. *. Omit for the manifest value or the default."`
}

type addonVersion struct {
	ID            int                 `json:"id"`
	Version       string              `json:"version"`
	Channel       string              `json:"channel"`
	Compatibility map[string]appRange `json:"compatibility"`
	EditURL       string              `json:"edit_url"`
	File          *fileInfo           `json:"file"`
	IsDisabled    bool                `json:"is_disabled"`
	License       *license            `json:"license"`
	ReleaseNotes  translated          `json:"release_notes"`
	Reviewed      *string             `json:"reviewed"`
	Source        *string             `json:"source"`
	ApprovalNotes string              `json:"approval_notes"`
}

type preview struct {
	ID       int        `json:"id"`
	Caption  translated `json:"caption"`
	ImageURL string     `json:"image_url"`
	Position int        `json:"position"`
}

type author struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

type addon struct {
	ID                    int             `json:"id"`
	GUID                  string          `json:"guid"`
	Slug                  string          `json:"slug"`
	Name                  translated      `json:"name"`
	Summary               translated      `json:"summary"`
	Description           translated      `json:"description"`
	DeveloperComments     translated      `json:"developer_comments"`
	DefaultLocale         string          `json:"default_locale"`
	Type                  string          `json:"type"`
	Status                string          `json:"status"`
	IsDisabled            bool            `json:"is_disabled"`
	IsExperimental        bool            `json:"is_experimental"`
	RequiresPayment       bool            `json:"requires_payment"`
	Authors               []author        `json:"authors"`
	AverageDailyUsers     int             `json:"average_daily_users"`
	WeeklyDownloads       int             `json:"weekly_downloads"`
	Categories            json.RawMessage `json:"categories"`
	Tags                  []string        `json:"tags"`
	Channels              []string        `json:"channels"`
	Created               string          `json:"created"`
	LastUpdated           string          `json:"last_updated"`
	CurrentVersion        *addonVersion   `json:"current_version"`
	LatestUnlistedVersion *addonVersion   `json:"latest_unlisted_version"`
	Homepage              json.RawMessage `json:"homepage"`
	SupportURL            json.RawMessage `json:"support_url"`
	SupportEmail          translated      `json:"support_email"`
	ContributionsURL      json.RawMessage `json:"contributions_url"`
	HasEULA               bool            `json:"has_eula"`
	HasPrivacyPolicy      bool            `json:"has_privacy_policy"`
	Previews              []preview       `json:"previews"`
	Promoted              []struct {
		Category string   `json:"category"`
		Apps     []string `json:"apps"`
	} `json:"promoted"`
	Ratings struct {
		Average         float64        `json:"average"`
		BayesianAverage float64        `json:"bayesian_average"`
		Count           int            `json:"count"`
		TextCount       int            `json:"text_count"`
		GroupedCounts   map[string]int `json:"grouped_counts"`
	} `json:"ratings"`
	URL     string        `json:"url"`
	EditURL string        `json:"edit_url"`
	IconURL string        `json:"icon_url"`
	Version *addonVersion `json:"version"`
}

type validationMessage struct {
	Type        string   `json:"type"`
	Message     string   `json:"message"`
	Description any      `json:"description"`
	File        string   `json:"file"`
	Line        *int     `json:"line"`
	Tier        int      `json:"tier"`
	ID          []string `json:"id"`
}

type upload struct {
	UUID       string `json:"uuid"`
	Channel    string `json:"channel"`
	Processed  bool   `json:"processed"`
	Submitted  bool   `json:"submitted"`
	Valid      bool   `json:"valid"`
	Version    string `json:"version"`
	URL        string `json:"url"`
	Validation *struct {
		Errors   int                 `json:"errors"`
		Warnings int                 `json:"warnings"`
		Notices  int                 `json:"notices"`
		Messages []validationMessage `json:"messages"`
	} `json:"validation"`
}

type rating struct {
	ID      int     `json:"id"`
	Body    *string `json:"body"`
	Score   int     `json:"score"`
	Created string  `json:"created"`
	User    struct {
		Name string `json:"name"`
	} `json:"user"`
	Version *struct {
		Version string `json:"version"`
	} `json:"version"`
	Reply *struct {
		Body    *string `json:"body"`
		Created string  `json:"created"`
	} `json:"reply"`
	IsDeveloperReply bool `json:"is_developer_reply"`
}

// page is one DRF paginated response.
type page[T any] struct {
	Count          int            `json:"count"`
	Next           *string        `json:"next"`
	PageCount      int            `json:"page_count"`
	Results        []T            `json:"results"`
	GroupedRatings map[string]int `json:"grouped_ratings"`
}

// ---- API calls ----

func (c *amoClient) profile(ctx context.Context) (profile, error) {
	var p profile
	err := c.call(ctx, http.MethodGet, "/accounts/profile/", nil, nil, &p)
	return p, err
}

func addonPath(ref string) string { return "/addons/addon/" + url.PathEscape(ref) + "/" }

func versionPath(addonRef, versionRef string) string {
	return addonPath(addonRef) + "versions/" + url.PathEscape(versionRef) + "/"
}

func (c *amoClient) getAddon(ctx context.Context, ref string, q url.Values) (addon, error) {
	var a addon
	err := c.call(ctx, http.MethodGet, addonPath(ref), q, nil, &a)
	return a, err
}

func (c *amoClient) getUpload(ctx context.Context, uuid string) (upload, error) {
	var u upload
	err := c.call(ctx, http.MethodGet, "/addons/upload/"+url.PathEscape(uuid)+"/", nil, nil, &u)
	return u, err
}

// pollInterval is the wait between upload status checks; overridable in tests.
var pollInterval = 3 * time.Second

// uploadPackage sends a package to /addons/upload/ and, when wait > 0,
// polls until the validator processed it or wait elapses.
func (c *amoClient) uploadPackage(ctx context.Context, pkg pkgFile, channel string, wait time.Duration) (upload, error) {
	var up upload
	err := c.multipartCall(ctx, http.MethodPost, "/addons/upload/", map[string]string{"channel": channel},
		[]filePart{{Field: "upload", Name: pkg.Name, Data: pkg.Data}}, &up)
	if err != nil {
		return up, err
	}
	return c.waitUpload(ctx, up, wait)
}

func (c *amoClient) waitUpload(ctx context.Context, up upload, wait time.Duration) (upload, error) {
	deadline := time.Now().Add(wait)
	for !up.Processed && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return up, ctx.Err()
		case <-time.After(pollInterval):
		}
		next, err := c.getUpload(ctx, up.UUID)
		if err != nil {
			return up, err
		}
		up = next
	}
	return up, nil
}

// ---- add-on resolution ----
//
// "addon" accepts a numeric id, slug, guid or AMO URL; the last one used
// successfully is remembered in the config dir.

var (
	guidRE      = regexp.MustCompile(`^(\{[0-9A-Fa-f-]{36}\}|[A-Za-z0-9._+-]*@[A-Za-z0-9._-]+)$`)
	slugRE      = regexp.MustCompile(`^[\p{L}\p{N}_~-]+$`)
	addonURLRE  = regexp.MustCompile(`/addon/([^/?#]+)`)
	versionIDRE = regexp.MustCompile(`^\d+$`)
	uuidRE      = regexp.MustCompile(`^[0-9a-fA-F]{32}$|^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

func lastAddonPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "last_addon.json"), nil
}

func rememberedAddon() string {
	p, err := lastAddonPath()
	if err != nil {
		return ""
	}
	var v struct {
		Addon string `json:"addon"`
	}
	if data, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(data, &v)
	}
	return v.Addon
}

// rememberAddon stores ref after a call that the API accepted; "" forgets it.
func rememberAddon(ref string) {
	p, err := lastAddonPath()
	if err != nil {
		return
	}
	if ref == "" {
		_ = os.Remove(p)
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	data, _ := json.Marshal(map[string]string{"addon": ref})
	_ = os.WriteFile(p, data, 0o600)
}

// parseAddonRef accepts an id, slug, guid or an AMO / Developer Hub URL.
func parseAddonRef(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err == nil {
			if m := addonURLRE.FindStringSubmatch(u.Path); m != nil {
				if ref, err := url.PathUnescape(m[1]); err == nil {
					return ref, nil
				}
			}
		}
		return "", fmt.Errorf("addon URL %q has no /addon/<slug>/ part", s)
	}
	if versionIDRE.MatchString(s) || guidRE.MatchString(s) || slugRE.MatchString(s) {
		return s, nil
	}
	return "", fmt.Errorf("addon %q must be a numeric id, slug, guid ({…} or name@domain) or AMO URL", s)
}

func resolveAddon(ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		if last := rememberedAddon(); last != "" {
			return last, nil
		}
		return "", errors.New("addon is required: its numeric id, slug, guid or AMO URL (amo_my_addons lists yours). It is remembered after the first successful call")
	}
	return parseAddonRef(ref)
}

// versionRef maps a version argument to the URL form: an id stays as is, a
// number gets a "v" prefix unless it contains a dot (AMO reads dotless
// values as ids).
func versionRef(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", errors.New("version is required: a version id or number (e.g. 1.2.0)")
	case versionIDRE.MatchString(s), strings.Contains(s, "."), strings.HasPrefix(s, "v"):
		return s, nil
	}
	return "v" + s, nil
}

// ---- packages ----

// maxPackageSize is AMO's add-on file size limit.
const maxPackageSize = 200 << 20

type pkgFile struct {
	Name     string
	Data     []byte
	Version  string // manifest version, "" if unreadable
	GeckoID  string // browser_specific_settings.gecko.id
	FromDir  bool
	Manifest bool // manifest.json found at the root
}

// loadPackage reads an .xpi/.zip (or, for a directory, zips its contents in
// memory, leaving out dot files and dirs such as .git).
func loadPackage(path string) (pkgFile, error) {
	path = expandHome(path)
	fi, err := os.Stat(path)
	if err != nil {
		return pkgFile{}, err
	}
	p := pkgFile{Name: filepath.Base(path)}
	if fi.IsDir() {
		if p.Data, err = zipDir(path, maxPackageSize); err != nil {
			return p, err
		}
		p.Name = strings.TrimSuffix(filepath.Base(filepath.Clean(path)), "/") + ".zip"
		p.FromDir = true
	} else {
		if fi.Size() > maxPackageSize {
			return p, fmt.Errorf("%s is %d bytes; AMO's limit is 200 MB", path, fi.Size())
		}
		if p.Data, err = os.ReadFile(path); err != nil {
			return p, err
		}
	}
	if err := readManifest(&p); err != nil {
		return p, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// readFile reads a local file of at most max bytes.
func readFile(path string, max int64) (string, []byte, error) {
	path = expandHome(path)
	fi, err := os.Stat(path)
	if err != nil {
		return "", nil, err
	}
	if fi.IsDir() {
		return "", nil, fmt.Errorf("%s is a directory", path)
	}
	if fi.Size() > max {
		return "", nil, fmt.Errorf("%s is %d bytes; the limit is %d MB", path, fi.Size(), max>>20)
	}
	data, err := os.ReadFile(path)
	return filepath.Base(path), data, err
}

// loadSource returns a source archive: a .zip/.tar.gz/.tgz/.tar.bz2 file, or
// a folder zipped in memory (dot files left out).
func loadSource(path string) (string, []byte, error) {
	path = expandHome(path)
	fi, err := os.Stat(path)
	if err != nil {
		return "", nil, err
	}
	if fi.IsDir() {
		data, err := zipDir(path, maxPackageSize)
		return filepath.Base(filepath.Clean(path)) + "-source.zip", data, err
	}
	name := strings.ToLower(fi.Name())
	ok := false
	for _, ext := range []string{".zip", ".tar.gz", ".tgz", ".tar.bz2", ".tar.xz", ".xz"} {
		ok = ok || strings.HasSuffix(name, ext)
	}
	if !ok {
		return "", nil, fmt.Errorf("source %s must be a .zip, .tar.gz, .tgz or .tar.bz2 archive, or a folder", path)
	}
	return readFile(path, maxPackageSize)
}

func zipDir(dir string, max int) ([]byte, error) {
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
	if buf.Len() > max {
		return nil, fmt.Errorf("zipped folder %s is %d bytes; the limit is %d MB", dir, buf.Len(), max>>20)
	}
	return buf.Bytes(), nil
}

// readManifest fills Version and GeckoID from manifest.json at the zip root.
// Dictionaries and language packs may lack one: AMO is the judge, so only a
// non-zip file is an error.
func readManifest(p *pkgFile) error {
	zr, err := zip.NewReader(bytes.NewReader(p.Data), int64(len(p.Data)))
	if err != nil {
		return fmt.Errorf("not a zip/xpi file: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != "manifest.json" {
			continue
		}
		p.Manifest = true
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		var m struct {
			Version                string `json:"version"`
			BrowserSpecificSetting struct {
				Gecko struct {
					ID string `json:"id"`
				} `json:"gecko"`
			} `json:"browser_specific_settings"`
			Applications struct {
				Gecko struct {
					ID string `json:"id"`
				} `json:"gecko"`
			} `json:"applications"`
		}
		if json.NewDecoder(io.LimitReader(rc, 4<<20)).Decode(&m) == nil {
			p.Version = m.Version
			p.GeckoID = firstNonEmpty(m.BrowserSpecificSetting.Gecko.ID, m.Applications.Gecko.ID)
		}
		return nil
	}
	return nil
}

// translatedArg converts a locale → text argument for the API: "" deletes
// that translation (null).
func translatedArg(m map[string]string) map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if v == "" {
			out[k] = nil
		} else {
			out[k] = v
		}
	}
	return out
}

// rawText returns a JSON value (string or number) as text.
func rawText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.Trim(string(raw), `"`)
}

// linkURL reads an "outgoing link" field: {"url": <string|translated>, ...},
// a plain string, or a translated object.
func linkURL(raw json.RawMessage, locale string) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		URL json.RawMessage `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil && len(obj.URL) > 0 {
		return linkURL(obj.URL, locale)
	}
	var t translated
	if json.Unmarshal(raw, &t) == nil {
		return t.pick(locale)
	}
	return ""
}

// categoryList reads categories: a list of slugs, or the older
// {app: [slugs]} object.
func categoryList(raw json.RawMessage) []string {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var byApp map[string][]string
	if json.Unmarshal(raw, &byApp) == nil {
		seen := map[string]bool{}
		for _, app := range []string{"firefox", "android"} {
			for _, c := range byApp[app] {
				if !seen[c] {
					seen[c] = true
					list = append(list, c)
				}
			}
		}
	}
	return list
}

func itoa(n int) string { return strconv.Itoa(n) }
