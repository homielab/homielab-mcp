package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/homielab/homielab-mcp/internal/googleapi"
)

var apiBase = "https://api.appstoreconnect.apple.com"

type ascClient struct {
	*googleapi.Client
	creds *credentials
}

func newASCClient() (*ascClient, error) {
	hc, creds, err := httpClient()
	if err != nil {
		return nil, err
	}
	c := googleapi.New(hc, "App Store Connect API")
	c.DecodeError = decodeError
	return &ascClient{Client: c, creds: creds}, nil
}

// decodeError reads the JSON:API {"errors": [{status, code, title, detail}]} body.
func decodeError(status int, body []byte) error {
	var e struct {
		Errors []struct {
			Code   string `json:"code"`
			Title  string `json:"title"`
			Detail string `json:"detail"`
			Source *struct {
				Pointer   string `json:"pointer"`
				Parameter string `json:"parameter"`
			} `json:"source"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &e) != nil || len(e.Errors) == 0 {
		return fmt.Errorf("App Store Connect API %d: %s", status, googleapi.Snippet(body))
	}
	var parts []string
	for _, x := range e.Errors {
		s := x.Code + ": " + x.Title
		if x.Detail != "" && x.Detail != x.Title {
			s += " — " + x.Detail
		}
		if x.Source != nil && x.Source.Pointer+x.Source.Parameter != "" {
			s += " (" + x.Source.Pointer + x.Source.Parameter + ")"
		}
		parts = append(parts, s)
	}
	msg := fmt.Sprintf("App Store Connect API %d: %s", status, strings.Join(parts, "; "))
	switch status {
	case http.StatusUnauthorized:
		msg += "\nThe JWT was rejected: check the key id, issuer id and .p8 file (asc_auth_status), and that the Mac's clock is right."
	case http.StatusForbidden:
		msg += "\nThe API key's role does not allow this call (writes need Admin or App Manager; reports need Sales or Finance)."
	}
	return errors.New(msg)
}

// ---- JSON:API documents ----

type ref struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type relationship struct {
	Data json.RawMessage `json:"data"` // ref, []ref or null
}

// refs decodes a relationship's linkage as a list.
func (r relationship) refs() []ref {
	if len(r.Data) == 0 || string(r.Data) == "null" {
		return nil
	}
	var many []ref
	if json.Unmarshal(r.Data, &many) == nil {
		return many
	}
	var one ref
	if json.Unmarshal(r.Data, &one) == nil && one.ID != "" {
		return []ref{one}
	}
	return nil
}

// resource is one JSON:API resource with typed attributes.
type resource[A any] struct {
	Type          string                  `json:"type"`
	ID            string                  `json:"id"`
	Attributes    A                       `json:"attributes"`
	Relationships map[string]relationship `json:"relationships"`
}

// rel returns the first id linked under name ("" when none).
func (r resource[A]) rel(name string) string {
	if refs := r.Relationships[name].refs(); len(refs) > 0 {
		return refs[0].ID
	}
	return ""
}

// rawResource keeps attributes undecoded (for included resources).
type rawResource = resource[json.RawMessage]

// included indexes included resources by type and id.
type included map[string]rawResource

func (in included) add(rs []rawResource) {
	for _, r := range rs {
		in[r.Type+"/"+r.ID] = r
	}
}

// get decodes the attributes of an included resource into v; false if absent.
func (in included) get(typ, id string, v any) bool {
	r, ok := in[typ+"/"+id]
	if !ok || id == "" {
		return false
	}
	return json.Unmarshal(r.Attributes, v) == nil
}

type document struct {
	Data     json.RawMessage `json:"data"`
	Included []rawResource   `json:"included"`
	Links    struct {
		Next string `json:"next"`
	} `json:"links"`
}

func (c *ascClient) url(path string, q url.Values) string {
	u := googleapi.JoinURL(apiBase, path)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// get fetches one resource.
func get[A any](ctx context.Context, c *ascClient, path string, q url.Values) (resource[A], included, error) {
	var out resource[A]
	body, _, err := c.Do(ctx, http.MethodGet, c.url(path, q), nil)
	if err != nil {
		return out, nil, err
	}
	var doc document
	if err := json.Unmarshal(body, &doc); err != nil {
		return out, nil, fmt.Errorf("decoding %s: %w", path, err)
	}
	if len(doc.Data) == 0 || string(doc.Data) == "null" {
		return out, included{}, nil
	}
	if err := json.Unmarshal(doc.Data, &out); err != nil {
		return out, nil, fmt.Errorf("decoding %s: %w", path, err)
	}
	in := included{}
	in.add(doc.Included)
	return out, in, nil
}

// maxPageSize is the API's limit cap.
const maxPageSize = 200

// list follows links.next until max items (0 = all, capped at 10,000).
func list[A any](ctx context.Context, c *ascClient, path string, q url.Values, max int) ([]resource[A], included, error) {
	if max <= 0 {
		max = 10000
	}
	q2 := url.Values{}
	for k, v := range q {
		q2[k] = v
	}
	if q2.Get("limit") == "" {
		q2.Set("limit", fmt.Sprint(min(max, maxPageSize)))
	}
	next := c.url(path, q2)
	var out []resource[A]
	in := included{}
	for next != "" && len(out) < max {
		body, _, err := c.Do(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, nil, err
		}
		var doc document
		if err := json.Unmarshal(body, &doc); err != nil {
			return nil, nil, fmt.Errorf("decoding %s: %w", path, err)
		}
		var page []resource[A]
		if err := json.Unmarshal(doc.Data, &page); err != nil {
			return nil, nil, fmt.Errorf("decoding %s: %w", path, err)
		}
		out = append(out, page...)
		in.add(doc.Included)
		next = doc.Links.Next
	}
	if len(out) > max {
		out = out[:max]
	}
	return out, in, nil
}

// send POSTs or PATCHes a JSON:API body and decodes the returned resource.
func send[A any](ctx context.Context, c *ascClient, method, path string, body any) (resource[A], error) {
	var out resource[A]
	data, _, err := c.Do(ctx, method, c.url(path, nil), body)
	if err != nil {
		return out, err
	}
	if len(data) == 0 {
		return out, nil
	}
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return out, fmt.Errorf("decoding %s: %w", path, err)
	}
	if len(doc.Data) > 0 && string(doc.Data) != "null" {
		err = json.Unmarshal(doc.Data, &out)
	}
	return out, err
}

// payload builds {"data": {type, id?, attributes?, relationships?}}.
func payload(typ, id string, attrs map[string]any, rels map[string]ref) map[string]any {
	data := map[string]any{"type": typ}
	if id != "" {
		data["id"] = id
	}
	if len(attrs) > 0 {
		data["attributes"] = attrs
	}
	if len(rels) > 0 {
		r := map[string]any{}
		for name, x := range rels {
			r[name] = map[string]any{"data": x}
		}
		data["relationships"] = r
	}
	return map[string]any{"data": data}
}

// linkage builds a to-many relationship body {"data": [{type, id}, ...]}.
func linkage(typ string, ids []string) map[string]any {
	data := make([]ref, len(ids))
	for i, id := range ids {
		data[i] = ref{Type: typ, ID: id}
	}
	return map[string]any{"data": data}
}

// ---- attributes ----

type appAttrs struct {
	Name          string `json:"name"`
	BundleID      string `json:"bundleId"`
	SKU           string `json:"sku"`
	PrimaryLocale string `json:"primaryLocale"`
}

type buildAttrs struct {
	Version         string `json:"version"`
	UploadedDate    string `json:"uploadedDate"`
	ExpirationDate  string `json:"expirationDate"`
	Expired         bool   `json:"expired"`
	MinOsVersion    string `json:"minOsVersion"`
	ProcessingState string `json:"processingState"`
	AudienceType    string `json:"buildAudienceType"`
	UsesEncryption  *bool  `json:"usesNonExemptEncryption"`
}

type preReleaseAttrs struct {
	Version  string `json:"version"`
	Platform string `json:"platform"`
}

type buildBetaDetailAttrs struct {
	InternalBuildState string `json:"internalBuildState"`
	ExternalBuildState string `json:"externalBuildState"`
}

type betaGroupAttrs struct {
	Name              string `json:"name"`
	CreatedDate       string `json:"createdDate"`
	IsInternalGroup   bool   `json:"isInternalGroup"`
	AllBuilds         bool   `json:"hasAccessToAllBuilds"`
	PublicLinkEnabled bool   `json:"publicLinkEnabled"`
	PublicLink        string `json:"publicLink"`
	FeedbackEnabled   bool   `json:"feedbackEnabled"`
}

type betaTesterAttrs struct {
	FirstName  string `json:"firstName"`
	LastName   string `json:"lastName"`
	Email      string `json:"email"`
	InviteType string `json:"inviteType"`
	State      string `json:"state"`
}

type versionAttrs struct {
	Platform            string `json:"platform"`
	VersionString       string `json:"versionString"`
	AppVersionState     string `json:"appVersionState"`
	AppStoreState       string `json:"appStoreState"`
	ReleaseType         string `json:"releaseType"`
	EarliestReleaseDate string `json:"earliestReleaseDate"`
	Copyright           string `json:"copyright"`
	CreatedDate         string `json:"createdDate"`
}

// state prefers appVersionState (appStoreState is deprecated).
func (v versionAttrs) state() string { return firstNonEmpty(v.AppVersionState, v.AppStoreState) }

type versionLocAttrs struct {
	Locale          string `json:"locale"`
	Description     string `json:"description"`
	Keywords        string `json:"keywords"`
	MarketingURL    string `json:"marketingUrl"`
	PromotionalText string `json:"promotionalText"`
	SupportURL      string `json:"supportUrl"`
	WhatsNew        string `json:"whatsNew"`
}

type appInfoAttrs struct {
	State         string `json:"state"`
	AppStoreState string `json:"appStoreState"`
}

type appInfoLocAttrs struct {
	Locale           string `json:"locale"`
	Name             string `json:"name"`
	Subtitle         string `json:"subtitle"`
	PrivacyPolicyURL string `json:"privacyPolicyUrl"`
}

type reviewSubmissionAttrs struct {
	Platform      string `json:"platform"`
	SubmittedDate string `json:"submittedDate"`
	State         string `json:"state"`
}

type phasedReleaseAttrs struct {
	State            string `json:"phasedReleaseState"`
	StartDate        string `json:"startDate"`
	TotalPauseDays   int    `json:"totalPauseDuration"`
	CurrentDayNumber int    `json:"currentDayNumber"`
}

type customerReviewAttrs struct {
	Rating      int    `json:"rating"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	Nickname    string `json:"reviewerNickname"`
	CreatedDate string `json:"createdDate"`
	Territory   string `json:"territory"`
}

type reviewResponseAttrs struct {
	ResponseBody     string `json:"responseBody"`
	LastModifiedDate string `json:"lastModifiedDate"`
	State            string `json:"state"`
}

type iapAttrs struct {
	Name           string `json:"name"`
	ProductID      string `json:"productId"`
	Type           string `json:"inAppPurchaseType"`
	State          string `json:"state"`
	FamilySharable bool   `json:"familySharable"`
}

type subscriptionGroupAttrs struct {
	ReferenceName string `json:"referenceName"`
}

type subscriptionAttrs struct {
	Name       string `json:"name"`
	ProductID  string `json:"productId"`
	State      string `json:"state"`
	Period     string `json:"subscriptionPeriod"`
	GroupLevel int    `json:"groupLevel"`
}

type betaLocAttrs struct {
	Locale   string `json:"locale"`
	WhatsNew string `json:"whatsNew"`
}

// ---- apps ----

func (c *ascClient) listApps(ctx context.Context, bundleID string) ([]resource[appAttrs], error) {
	q := url.Values{"sort": {"name"}}
	if bundleID != "" {
		q.Set("filter[bundleId]", bundleID)
	}
	apps, _, err := list[appAttrs](ctx, c, "/v1/apps", q, 0)
	return apps, err
}

// The server remembers the last resolved app so later calls may omit "app".

type rememberedApp struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	BundleID string `json:"bundleId"`
}

func lastAppPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "last_app.json"), nil
}

func loadRememberedApp() (rememberedApp, bool) {
	var a rememberedApp
	p, err := lastAppPath()
	if err != nil {
		return a, false
	}
	data, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(data, &a) != nil || a.ID == "" {
		return a, false
	}
	return a, true
}

func rememberApp(a rememberedApp) {
	p, err := lastAppPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	data, _ := json.Marshal(a)
	_ = os.WriteFile(p, data, 0o600)
}

var digitsRE = regexp.MustCompile(`^[0-9]+$`)

// resolveApp turns an Apple ID, bundle id or app name (or "" = last used)
// into the app, and remembers it.
func (c *ascClient) resolveApp(ctx context.Context, app string) (rememberedApp, error) {
	app = strings.TrimSpace(app)
	if app == "" {
		if a, ok := loadRememberedApp(); ok {
			return a, nil
		}
		return rememberedApp{}, errors.New("app is required (Apple ID, bundle id or app name — asc_apps lists them); later calls may omit it")
	}
	var found resource[appAttrs]
	switch {
	case digitsRE.MatchString(app):
		a, _, err := get[appAttrs](ctx, c, "/v1/apps/"+app, nil)
		if err != nil {
			return rememberedApp{}, err
		}
		found = a
	default:
		apps, err := c.listApps(ctx, app)
		if err != nil {
			return rememberedApp{}, err
		}
		if i := slices.IndexFunc(apps, func(a resource[appAttrs]) bool { return a.Attributes.BundleID == app }); i >= 0 {
			found = apps[i]
			break
		}
		// filter[bundleId] matched nothing exactly: try names.
		all, err := c.listApps(ctx, "")
		if err != nil {
			return rememberedApp{}, err
		}
		var matches []resource[appAttrs]
		for _, a := range all {
			if strings.EqualFold(a.Attributes.Name, app) || strings.EqualFold(a.Attributes.BundleID, app) {
				matches = append(matches, a)
			}
		}
		if len(matches) == 0 {
			for _, a := range all {
				if strings.Contains(strings.ToLower(a.Attributes.Name), strings.ToLower(app)) {
					matches = append(matches, a)
				}
			}
		}
		switch len(matches) {
		case 0:
			return rememberedApp{}, fmt.Errorf("no app matches %q (asc_apps lists the apps this key can see)", app)
		case 1:
			found = matches[0]
		default:
			var names []string
			for _, m := range matches {
				names = append(names, fmt.Sprintf("%s (%s, id %s)", m.Attributes.Name, m.Attributes.BundleID, m.ID))
			}
			return rememberedApp{}, fmt.Errorf("%q matches several apps: %s — pass the id or bundle id", app, strings.Join(names, "; "))
		}
	}
	a := rememberedApp{ID: found.ID, Name: found.Attributes.Name, BundleID: found.Attributes.BundleID}
	rememberApp(a)
	return a, nil
}

func (a rememberedApp) String() string {
	return fmt.Sprintf("%s (%s, id %s)", a.Name, a.BundleID, a.ID)
}

// ---- platforms ----

var platforms = []string{"IOS", "MAC_OS", "TV_OS", "VISION_OS"}

// normPlatform accepts "ios", "macos", "mac_os", "tvos", "visionos"; "" = IOS.
func normPlatform(p string) (string, error) {
	p = strings.ToUpper(strings.TrimSpace(p))
	switch p {
	case "":
		return "IOS", nil
	case "MACOS", "MAC":
		return "MAC_OS", nil
	case "TVOS":
		return "TV_OS", nil
	case "VISIONOS":
		return "VISION_OS", nil
	}
	if !slices.Contains(platforms, p) {
		return "", fmt.Errorf("platform %q: want one of %s", p, strings.Join(platforms, ", "))
	}
	return p, nil
}

// ---- builds ----

// resolveBuild finds a build by id, or by build number (+ marketing version)
// within the app; buildNumber "latest" = newest upload.
func (c *ascClient) resolveBuild(ctx context.Context, appID, buildID, buildNumber, version, platform string) (resource[buildAttrs], included, error) {
	if buildID != "" {
		return get[buildAttrs](ctx, c, "/v1/builds/"+buildID, url.Values{"include": {"preReleaseVersion,buildBetaDetail"}})
	}
	if buildNumber == "" {
		return resource[buildAttrs]{}, nil, errors.New("buildId or buildNumber is required (\"latest\" = newest upload)")
	}
	q := url.Values{
		"filter[app]": {appID},
		"sort":        {"-uploadedDate"},
		"include":     {"preReleaseVersion,buildBetaDetail"},
	}
	if buildNumber != "latest" {
		q.Set("filter[version]", buildNumber)
	}
	if version != "" {
		q.Set("filter[preReleaseVersion.version]", version)
	}
	if platform != "" {
		q.Set("filter[preReleaseVersion.platform]", platform)
	}
	builds, in, err := list[buildAttrs](ctx, c, "/v1/builds", q, 2)
	if err != nil {
		return resource[buildAttrs]{}, nil, err
	}
	switch {
	case len(builds) == 0:
		return resource[buildAttrs]{}, nil, fmt.Errorf("no build %s found (asc_builds lists them)", buildNumber)
	case len(builds) > 1 && buildNumber != "latest":
		return resource[buildAttrs]{}, nil, fmt.Errorf("build number %s exists in several versions/platforms: pass version, platform or buildId", buildNumber)
	}
	return builds[0], in, nil
}

// buildLabel is "1.2.0 (45)".
func buildLabel(b resource[buildAttrs], in included) string {
	var pre preReleaseAttrs
	if in.get("preReleaseVersions", b.rel("preReleaseVersion"), &pre) {
		return fmt.Sprintf("%s (%s)", pre.Version, b.Attributes.Version)
	}
	return "build " + b.Attributes.Version
}

// setWhatToTest creates or updates the TestFlight "What to Test" text.
func (c *ascClient) setWhatToTest(ctx context.Context, buildID, locale, text string) error {
	locs, _, err := list[betaLocAttrs](ctx, c, "/v1/betaBuildLocalizations", url.Values{"filter[build]": {buildID}, "filter[locale]": {locale}}, 1)
	if err != nil {
		return err
	}
	attrs := map[string]any{"whatsNew": text}
	if len(locs) > 0 {
		_, err = send[betaLocAttrs](ctx, c, http.MethodPatch, "/v1/betaBuildLocalizations/"+locs[0].ID, payload("betaBuildLocalizations", locs[0].ID, attrs, nil))
		return err
	}
	attrs["locale"] = locale
	_, err = send[betaLocAttrs](ctx, c, http.MethodPost, "/v1/betaBuildLocalizations", payload("betaBuildLocalizations", "", attrs, map[string]ref{"build": {Type: "builds", ID: buildID}}))
	return err
}

// resolveGroups maps group ids or names to beta groups of the app.
func (c *ascClient) resolveGroups(ctx context.Context, appID string, names []string) ([]resource[betaGroupAttrs], error) {
	groups, _, err := list[betaGroupAttrs](ctx, c, "/v1/apps/"+appID+"/betaGroups", nil, 0)
	if err != nil {
		return nil, err
	}
	var out []resource[betaGroupAttrs]
	for _, n := range names {
		i := slices.IndexFunc(groups, func(g resource[betaGroupAttrs]) bool {
			return g.ID == n || strings.EqualFold(g.Attributes.Name, n)
		})
		if i < 0 {
			var have []string
			for _, g := range groups {
				have = append(have, g.Attributes.Name)
			}
			return nil, fmt.Errorf("no beta group %q in this app (groups: %s)", n, strings.Join(have, ", "))
		}
		out = append(out, groups[i])
	}
	return out, nil
}

// ---- App Store versions ----

// editableStates are versions whose metadata can still change.
var editableStates = []string{"PREPARE_FOR_SUBMISSION", "DEVELOPER_REJECTED", "REJECTED", "METADATA_REJECTED", "INVALID_BINARY", "WAITING_FOR_REVIEW", "READY_FOR_REVIEW"}

// resolveVersion finds an App Store version by id, by version string, or
// (both empty) the app's editable version on platform; with fallbackLatest,
// the newest version when none is editable.
func (c *ascClient) resolveVersion(ctx context.Context, appID, versionID, versionString, platform string, fallbackLatest bool) (resource[versionAttrs], included, error) {
	q := url.Values{"include": {"build"}}
	if versionID != "" {
		return get[versionAttrs](ctx, c, "/v1/appStoreVersions/"+versionID, q)
	}
	q.Set("filter[platform]", platform)
	if versionString != "" {
		q.Set("filter[versionString]", versionString)
	}
	versions, in, err := list[versionAttrs](ctx, c, "/v1/apps/"+appID+"/appStoreVersions", q, 20)
	if err != nil {
		return resource[versionAttrs]{}, nil, err
	}
	if versionString != "" {
		if len(versions) == 0 {
			return resource[versionAttrs]{}, nil, fmt.Errorf("no %s version %s (asc_versions lists them; asc_version_create makes one)", platform, versionString)
		}
		return versions[0], in, nil
	}
	for _, v := range versions {
		if slices.Contains(editableStates, v.Attributes.state()) {
			return v, in, nil
		}
	}
	if fallbackLatest && len(versions) > 0 {
		return newestVersion(versions), in, nil
	}
	return resource[versionAttrs]{}, nil, fmt.Errorf("no editable %s version (states %s): create one with asc_version_create or pass versionString", platform, strings.Join(editableStates, ", "))
}

func newestVersion(vs []resource[versionAttrs]) resource[versionAttrs] {
	return slices.MaxFunc(vs, func(a, b resource[versionAttrs]) int {
		return strings.Compare(a.Attributes.CreatedDate, b.Attributes.CreatedDate)
	})
}

func versionLabel(v resource[versionAttrs]) string {
	return fmt.Sprintf("%s %s (%s, id %s)", v.Attributes.Platform, v.Attributes.VersionString, v.Attributes.state(), v.ID)
}

// editableAppInfo picks the appInfo whose localizations can change: the one
// not live, else the only one.
func (c *ascClient) editableAppInfo(ctx context.Context, appID string) (resource[appInfoAttrs], error) {
	infos, _, err := list[appInfoAttrs](ctx, c, "/v1/apps/"+appID+"/appInfos", nil, 0)
	if err != nil {
		return resource[appInfoAttrs]{}, err
	}
	if len(infos) == 0 {
		return resource[appInfoAttrs]{}, errors.New("the app has no appInfo")
	}
	for _, i := range infos {
		s := firstNonEmpty(i.Attributes.State, i.Attributes.AppStoreState)
		if s != "READY_FOR_DISTRIBUTION" && s != "READY_FOR_SALE" && s != "REPLACED_WITH_NEW_INFO" {
			return i, nil
		}
	}
	return infos[0], nil
}

// ---- review submissions ----

// submitForReview adds the version to the app's open review submission (or a
// new one) and submits it.
func (c *ascClient) submitForReview(ctx context.Context, appID string, v resource[versionAttrs]) (resource[reviewSubmissionAttrs], error) {
	open, _, err := list[reviewSubmissionAttrs](ctx, c, "/v1/reviewSubmissions", url.Values{
		"filter[app]":      {appID},
		"filter[platform]": {v.Attributes.Platform},
		"filter[state]":    {"READY_FOR_REVIEW"},
	}, 1)
	if err != nil {
		return resource[reviewSubmissionAttrs]{}, err
	}
	var sub resource[reviewSubmissionAttrs]
	if len(open) > 0 {
		sub = open[0]
	} else {
		sub, err = send[reviewSubmissionAttrs](ctx, c, http.MethodPost, "/v1/reviewSubmissions", payload("reviewSubmissions", "",
			map[string]any{"platform": v.Attributes.Platform}, map[string]ref{"app": {Type: "apps", ID: appID}}))
		if err != nil {
			return sub, fmt.Errorf("creating the review submission: %w", err)
		}
	}
	_, err = send[json.RawMessage](ctx, c, http.MethodPost, "/v1/reviewSubmissionItems", payload("reviewSubmissionItems", "", nil, map[string]ref{
		"reviewSubmission": {Type: "reviewSubmissions", ID: sub.ID},
		"appStoreVersion":  {Type: "appStoreVersions", ID: v.ID},
	}))
	// A version already in this submission answers 409; submitting is still right.
	if err != nil && !strings.Contains(err.Error(), " 409:") {
		return sub, fmt.Errorf("adding the version to review submission %s: %w", sub.ID, err)
	}
	return send[reviewSubmissionAttrs](ctx, c, http.MethodPatch, "/v1/reviewSubmissions/"+sub.ID, payload("reviewSubmissions", sub.ID, map[string]any{"submitted": true}, nil))
}

// ---- raw API ----

var apiPathRE = regexp.MustCompile(`^/v[0-9]+/`)

// apiURL validates an API path (or a full URL on apiBase) and adds query.
func apiURL(path string, query map[string]string) (string, error) {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, strings.TrimSuffix(apiBase, "/"))
	if !apiPathRE.MatchString(path) {
		return "", fmt.Errorf("path %q: want an API path like /v1/apps", path)
	}
	u, err := url.Parse(apiBase + path)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// maxRawOutput caps asc_get / asc_request output.
const maxRawOutput = 100_000

// getRaw GETs u; with maxItems > 0 it follows links.next and merges data.
func (c *ascClient) getRaw(ctx context.Context, u string, maxItems int) (string, error) {
	body, _, err := c.Do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	body = stripLinks(body)
	if maxItems <= 0 {
		return prettyJSON(body), nil
	}
	var doc struct {
		Data     []json.RawMessage `json:"data"`
		Included []json.RawMessage `json:"included,omitempty"`
		Links    struct {
			Next string `json:"next"`
		} `json:"links"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return prettyJSON(body), nil // not a list
	}
	for doc.Links.Next != "" && len(doc.Data) < maxItems {
		next := doc.Links.Next
		doc.Links.Next = ""
		page, _, err := c.Do(ctx, http.MethodGet, next, nil)
		if err != nil {
			return "", err
		}
		var p struct {
			Data     []json.RawMessage `json:"data"`
			Included []json.RawMessage `json:"included"`
			Links    struct {
				Next string `json:"next"`
			} `json:"links"`
		}
		if err := json.Unmarshal(stripLinks(page), &p); err != nil {
			return "", err
		}
		doc.Data = append(doc.Data, p.Data...)
		doc.Included = append(doc.Included, p.Included...)
		doc.Links.Next = p.Links.Next
	}
	if len(doc.Data) > maxItems {
		doc.Data = doc.Data[:maxItems]
	}
	out, err := json.Marshal(map[string]any{"data": doc.Data, "included": doc.Included, "count": len(doc.Data), "more": doc.Links.Next != ""})
	if err != nil {
		return "", err
	}
	return prettyJSON(out), nil
}

// stripLinks drops the self/related "links" and relationship "meta" that
// every resource carries, keeping the top-level links (pagination).
func stripLinks(data []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return data
	}
	var walk func(v any, depth int)
	walk = func(v any, depth int) {
		switch t := v.(type) {
		case map[string]any:
			if depth > 0 {
				delete(t, "links")
			}
			if rels, ok := t["relationships"].(map[string]any); ok {
				for name, r := range rels {
					if rm, ok := r.(map[string]any); ok {
						delete(rm, "meta")
						delete(rm, "links")
						if len(rm) == 0 {
							delete(rels, name)
						}
					}
				}
				if len(rels) == 0 {
					delete(t, "relationships")
				}
			}
			for _, x := range t {
				walk(x, depth+1)
			}
		case []any:
			for _, x := range t {
				walk(x, depth+1)
			}
		}
	}
	walk(v, 0)
	out, err := json.Marshal(v)
	if err != nil {
		return data
	}
	return out
}

// prettyJSON indents data, truncated to maxRawOutput.
func prettyJSON(data []byte) string {
	var b bytes.Buffer
	if json.Indent(&b, data, "", "  ") != nil {
		b.Reset()
		b.Write(data)
	}
	s := b.String()
	if len(s) > maxRawOutput {
		s = s[:maxRawOutput] + fmt.Sprintf("\n… truncated (%d bytes total): narrow with fields[...], filter[...] or limit", len(s))
	}
	return s
}
