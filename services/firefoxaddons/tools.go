package firefoxaddons

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/mcpkit"
	"github.com/homielab/homielab-mcp/internal/render"
)

func textResult(s string) (*mcp.CallToolResult, any, error) { return mcpkit.TextResult(s) }

func errResult(err error) (*mcp.CallToolResult, any, error) { return mcpkit.ErrorResult(err) }

// errRes is errResult as a bare result, for helpers that return early.
func errRes(err error) *mcp.CallToolResult {
	res, _, _ := errResult(err)
	return res
}

// addonArgs are the arguments every add-on tool takes.
type addonArgs struct {
	Addon string `json:"addon,omitempty" jsonschema:"Add-on numeric id, slug, guid ({…} or name@domain) or AMO URL. Omit to use the last one used."`
}

// withAddon returns a signed client and the resolved add-on, or an error result.
func withAddon(a addonArgs) (*amoClient, string, *mcp.CallToolResult) {
	ref, err := resolveAddon(a.Addon)
	if err != nil {
		return nil, "", errRes(err)
	}
	c, err := newAMOClient()
	if err != nil {
		return nil, "", errRes(err)
	}
	return c, ref, nil
}

// listingArgs are the add-on metadata fields shared by create and edit.
type listingArgs struct {
	Name              map[string]string `json:"name,omitempty" jsonschema:"Add-on name by locale, e.g. {\"en-US\": \"My Add-on\"}. An empty string deletes that translation."`
	Summary           map[string]string `json:"summary,omitempty" jsonschema:"Short summary (max 250 chars) by locale."`
	Description       map[string]string `json:"description,omitempty" jsonschema:"Long description by locale; some Markdown allowed."`
	DeveloperComments map[string]string `json:"developerComments,omitempty" jsonschema:"Developer comments by locale."`
	Homepage          map[string]string `json:"homepage,omitempty" jsonschema:"Homepage URL by locale."`
	SupportEmail      map[string]string `json:"supportEmail,omitempty" jsonschema:"Support email by locale."`
	SupportURL        map[string]string `json:"supportUrl,omitempty" jsonschema:"Support site URL by locale."`
	Categories        []string          `json:"categories,omitempty" jsonschema:"Category slugs, e.g. [\"privacy-security\"] (max 3; other cannot be combined). Replaces the current ones."`
	Tags              []string          `json:"tags,omitempty" jsonschema:"Tag names (from AMO's fixed tag list). Replaces the current ones."`
	Slug              string            `json:"slug,omitempty" jsonschema:"URL slug: letters, digits, -, _, ~; not all digits."`
	DefaultLocale     string            `json:"defaultLocale,omitempty" jsonschema:"Fallback locale for the translated fields, e.g. en-US."`
	ContributionsURL  *string           `json:"contributionsUrl,omitempty" jsonschema:"Donation page URL (only some services are allowed); empty string clears it."`
	IsExperimental    *bool             `json:"isExperimental,omitempty" jsonschema:"Mark the add-on as experimental."`
	RequiresPayment   *bool             `json:"requiresPayment,omitempty" jsonschema:"The add-on needs payment, paid services or extra hardware."`
	Extra             map[string]any    `json:"extra,omitempty" jsonschema:"Other API fields to send as is (snake_case names from the AMO docs)."`
}

func (l listingArgs) body() (map[string]any, error) {
	b := map[string]any{}
	set := func(k string, m map[string]string) {
		if t := translatedArg(m); t != nil {
			b[k] = t
		}
	}
	set("name", l.Name)
	set("summary", l.Summary)
	set("description", l.Description)
	set("developer_comments", l.DeveloperComments)
	set("homepage", l.Homepage)
	set("support_email", l.SupportEmail)
	set("support_url", l.SupportURL)
	if l.Categories != nil {
		b["categories"] = l.Categories
	}
	if l.Tags != nil {
		b["tags"] = l.Tags
	}
	if l.Slug != "" {
		if !slugRE.MatchString(l.Slug) || versionIDRE.MatchString(l.Slug) {
			return nil, fmt.Errorf("slug %q: only letters, digits, -, _ and ~, and not all digits", l.Slug)
		}
		b["slug"] = l.Slug
	}
	if l.DefaultLocale != "" {
		b["default_locale"] = l.DefaultLocale
	}
	if l.ContributionsURL != nil {
		b["contributions_url"] = *l.ContributionsURL
	}
	if l.IsExperimental != nil {
		b["is_experimental"] = *l.IsExperimental
	}
	if l.RequiresPayment != nil {
		b["requires_payment"] = *l.RequiresPayment
	}
	for k, v := range l.Extra {
		b[k] = v
	}
	return b, nil
}

// versionFields are the version metadata shared by create and edit.
type versionFields struct {
	ReleaseNotes      map[string]string   `json:"releaseNotes,omitempty" jsonschema:"Release notes by locale, e.g. {\"en-US\": \"Fixes …\"}."`
	ApprovalNotes     *string             `json:"approvalNotes,omitempty" jsonschema:"Notes for Mozilla reviewers only (build steps for the source code, test accounts, …)."`
	License           string              `json:"license,omitempty" jsonschema:"Predefined license slug, e.g. MPL-2.0, GPL-3.0-or-later, MIT, all-rights-reserved (themes: cc-all-rights-reserved, …). Listed versions inherit the previous license when omitted."`
	CustomLicenseName map[string]string   `json:"customLicenseName,omitempty" jsonschema:"Custom license name by locale (not for themes); use with customLicenseText instead of license."`
	CustomLicenseText map[string]string   `json:"customLicenseText,omitempty" jsonschema:"Custom license text by locale."`
	Compatibility     map[string]appRange `json:"compatibility,omitempty" jsonschema:"Apps the version supports: firefox and/or android, each with optional min/max, e.g. {\"firefox\": {}, \"android\": {\"min\": \"120.0\"}}. Empty objects use the manifest or defaults."`
}

func (v versionFields) body() (map[string]any, error) {
	b := map[string]any{}
	if t := translatedArg(v.ReleaseNotes); t != nil {
		b["release_notes"] = t
	}
	if v.ApprovalNotes != nil {
		b["approval_notes"] = *v.ApprovalNotes
	}
	if v.License != "" && (v.CustomLicenseName != nil || v.CustomLicenseText != nil) {
		return nil, errors.New("pass either license or customLicenseName/customLicenseText, not both")
	}
	if v.License != "" {
		b["license"] = v.License
	}
	if v.CustomLicenseName != nil || v.CustomLicenseText != nil {
		cl := map[string]any{}
		if t := translatedArg(v.CustomLicenseName); t != nil {
			cl["name"] = t
		}
		if t := translatedArg(v.CustomLicenseText); t != nil {
			cl["text"] = t
		}
		b["custom_license"] = cl
	}
	if v.Compatibility != nil {
		for app := range v.Compatibility {
			if app != "firefox" && app != "android" {
				return nil, fmt.Errorf("compatibility app %q must be firefox or android", app)
			}
		}
		b["compatibility"] = v.Compatibility
	}
	return b, nil
}

// submitArgs pick the package of a new version: an existing upload or a path.
type submitArgs struct {
	Upload      string `json:"upload,omitempty" jsonschema:"UUID of a valid upload (from amo_upload). Or pass path instead."`
	Path        string `json:"path,omitempty" jsonschema:"Local .xpi/.zip or extension folder (zipped in memory; dot files left out): uploaded and validated first."`
	Channel     string `json:"channel,omitempty" jsonschema:"listed (default: AMO listing, Mozilla review) or unlisted (signed for self-distribution). Only used with path."`
	SourcePath  string `json:"sourcePath,omitempty" jsonschema:"Source code archive (.zip/.tar.gz) or folder, required by Mozilla when the package is minified, bundled or generated."`
	WaitSeconds int    `json:"waitSeconds,omitempty" jsonschema:"Max seconds to wait for validation of a path upload, 10-600 (default 180)."`
}

func channelArg(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "listed":
		return "listed", nil
	case "unlisted":
		return "unlisted", nil
	}
	return "", fmt.Errorf("channel %q must be listed or unlisted", s)
}

func waitArg(n, def int) (time.Duration, error) {
	if n == 0 {
		n = def
	}
	if n < 0 || n > 600 {
		return 0, fmt.Errorf("waitSeconds must be 0-600, got %d", n)
	}
	return time.Duration(n) * time.Second, nil
}

// resolveUpload returns a processed, valid upload UUID for a version or
// add-on submission, uploading args.Path first when given. The second value
// is a report of the upload step.
func resolveUpload(ctx context.Context, c *amoClient, args submitArgs) (string, string, error) {
	if (args.Upload == "") == (args.Path == "") {
		return "", "", errors.New("pass exactly one of upload (an upload UUID) or path (a package file or folder)")
	}
	if args.Upload != "" {
		up, err := c.getUpload(ctx, args.Upload)
		if err != nil {
			return "", "", err
		}
		if err := uploadReady(up); err != nil {
			return "", renderUpload(up), err
		}
		return up.UUID, "", nil
	}
	ch, err := channelArg(args.Channel)
	if err != nil {
		return "", "", err
	}
	wait, err := waitArg(args.WaitSeconds, 180)
	if err != nil {
		return "", "", err
	}
	pkg, err := loadPackage(args.Path)
	if err != nil {
		return "", "", err
	}
	up, err := c.uploadPackage(ctx, pkg, ch, wait)
	if err != nil {
		return "", "", fmt.Errorf("upload of %s failed: %w", args.Path, err)
	}
	report := fmt.Sprintf("Uploaded %s (%s, version %s, %s channel).\n", args.Path, humanBytes(len(pkg.Data)), render.OrDash(pkg.Version), ch) + renderUpload(up)
	if err := uploadReady(up); err != nil {
		return "", report, err
	}
	return up.UUID, report, nil
}

func uploadReady(up upload) error {
	switch {
	case !up.Processed:
		return fmt.Errorf("upload %s is still being validated: call amo_upload_status {\"uuid\": %q, \"waitSeconds\": 120}, then retry with \"upload\": %q", up.UUID, up.UUID, up.UUID)
	case !up.Valid:
		return fmt.Errorf("upload %s failed validation: fix the errors and upload again", up.UUID)
	case up.Submitted:
		return fmt.Errorf("upload %s was already used for a version: upload the package again", up.UUID)
	}
	return nil
}

// attachSource uploads a source archive to a version (multipart PATCH).
func attachSource(ctx context.Context, c *amoClient, ref string, versionID int, path string) error {
	name, data, err := loadSource(path)
	if err != nil {
		return err
	}
	return c.multipartCall(ctx, http.MethodPatch, versionPath(ref, itoa(versionID)), nil,
		[]filePart{{Field: "source", Name: name, Data: data}}, nil)
}

var (
	addonSorts   = []string{"created", "-created", "last_updated", "-last_updated", "average_daily_users", "-average_daily_users", "weekly_downloads", "-weekly_downloads", "ratings.bayesian_average", "-ratings.bayesian_average", "hotness", "-hotness", "id", "-id"}
	searchSorts  = []string{"relevance", "users", "downloads", "rating", "ratings", "created", "updated", "hotness", "recommended", "random"}
	addonTypes   = []string{"extension", "statictheme", "dictionary", "language", "search"}
	versionLists = []string{"all_with_unlisted", "all_without_unlisted", "enterprise_only", "public"}
)

func setPage(q url.Values, page, size, max int) error {
	if page < 0 {
		return fmt.Errorf("page must be ≥ 1, got %d", page)
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if size == 0 {
		return nil
	}
	if size < 1 || size > max {
		return fmt.Errorf("pageSize must be 1-%d, got %d", max, size)
	}
	q.Set("page_size", strconv.Itoa(size))
	return nil
}

func registerTools(s *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	notDestructive, destructive, openWorld := false, true, true
	writeLike := &mcp.ToolAnnotations{DestructiveHint: &notDestructive}
	releaseLike := &mcp.ToolAnnotations{DestructiveHint: &notDestructive, OpenWorldHint: &openWorld}
	deleteLike := &mcp.ToolAnnotations{DestructiveHint: &destructive}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "amo_auth_status",
		Title:       "AMO Auth Status",
		Description: "Check the AMO API key: which server, which account it signs in as, and the site status (read-only maintenance, notices for developers).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ mcpkit.EmptyArgs) (*mcp.CallToolResult, any, error) {
		var b strings.Builder
		c, err := newAMOClient()
		if err != nil {
			return errResult(err)
		}
		fmt.Fprintf(&b, "Server: %s\n", c.base)
		var st siteStatus
		if err := c.call(ctx, http.MethodGet, "/site/", nil, nil, &st); err == nil {
			if st.ReadOnly {
				b.WriteString("Site is READ-ONLY (maintenance): writes fail until it ends.\n")
			}
			if st.Notice != nil && *st.Notice != "" {
				fmt.Fprintf(&b, "Site notice: %s\n", *st.Notice)
			}
			if st.SubmitNotificationWarning != nil && *st.SubmitNotificationWarning != "" {
				fmt.Fprintf(&b, "Submission notice: %s\n", *st.SubmitNotificationWarning)
			}
		}
		p, err := c.profile(ctx)
		if err != nil {
			return errResult(fmt.Errorf("%s%w", b.String(), err))
		}
		fmt.Fprintf(&b, "Key works: signed in as %s (user id %d", p.Username, p.ID)
		if p.Name != "" {
			fmt.Fprintf(&b, ", %s", p.Name)
		}
		fmt.Fprintf(&b, "). Listed add-ons: %d.\n", p.NumAddonsListed)
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "amo_my_addons",
		Title: "My Add-ons",
		Description: "Add-ons you are an author of (listed and unlisted): id, slug, guid, name, type, status, channels, current version, daily users, weekly downloads, rating. " +
			"Sort by id (default), created, last_updated, average_daily_users, weekly_downloads, … (prefix - for descending).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		Type     string `json:"type,omitempty" jsonschema:"Filter by type: extension, statictheme, dictionary, language, search (comma-separated for several)."`
		Sort     string `json:"sort,omitempty" jsonschema:"Sort field, e.g. -average_daily_users or -last_updated."`
		Page     int    `json:"page,omitempty" jsonschema:"1-based page."`
		PageSize int    `json:"pageSize,omitempty" jsonschema:"Add-ons per page, 1-50 (default 50)."`
	}) (*mcp.CallToolResult, any, error) {
		q := url.Values{}
		if args.Type != "" {
			q.Set("type", args.Type)
		}
		if args.Sort != "" {
			for _, s := range strings.Split(args.Sort, ",") {
				if !slices.Contains(addonSorts, strings.TrimSpace(s)) {
					return errResult(fmt.Errorf("sort %q must be one of %s", s, strings.Join(addonSorts, ", ")))
				}
			}
			q.Set("sort", args.Sort)
		}
		if args.PageSize == 0 {
			args.PageSize = 50
		}
		if err := setPage(q, args.Page, args.PageSize, 50); err != nil {
			return errResult(err)
		}
		c, err := newAMOClient()
		if err != nil {
			return errResult(err)
		}
		var out page[addon]
		if err := c.call(ctx, http.MethodGet, "/addons/addon/", q, nil, &out); err != nil {
			return errResult(err)
		}
		return textResult(renderAddonList(out, args.Page, true))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "amo_addon",
		Title: "Add-on Details",
		Description: "Details of an add-on: names and summary, status, type, guid, categories, tags, links, current and latest unlisted version, users, downloads, " +
			"ratings per star, screenshots (preview ids), EULA/privacy flags. Public add-ons work without an API key.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		addonArgs
		Raw bool `json:"raw,omitempty" jsonschema:"Return the full API JSON instead of the summary."`
	}) (*mcp.CallToolResult, any, error) {
		ref, err := resolveAddon(args.Addon)
		if err != nil {
			return errResult(err)
		}
		c, err := newPublicClient()
		if err != nil {
			return errResult(err)
		}
		q := url.Values{"show_grouped_ratings": {"true"}}
		if args.Raw {
			data, _, err := c.Do(ctx, http.MethodGet, c.url(addonPath(ref), q), nil)
			if err != nil {
				return errResult(err)
			}
			rememberAddon(ref)
			return textResult(string(data))
		}
		a, err := c.getAddon(ctx, ref, q)
		if err != nil {
			return errResult(err)
		}
		rememberAddon(ref)
		return textResult(renderAddon(a))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "amo_addon_edit",
		Title: "Edit Add-on Listing",
		Description: "Edit an add-on's AMO metadata: name, summary, description, developer comments, homepage, support email/URL, categories, tags, slug, default locale, " +
			"contributions URL, experimental / requires-payment flags. Translated fields take {locale: text}; locales not passed stay unchanged. " +
			"isDisabled:true hides the add-on from AMO and stops updates (confirm with the user).",
		Annotations: writeLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		addonArgs
		listingArgs
		IsDisabled *bool `json:"isDisabled,omitempty" jsonschema:"Disable (true) or re-enable (false) the add-on. Confirm with the user."`
	}) (*mcp.CallToolResult, any, error) {
		body, err := args.listingArgs.body()
		if err != nil {
			return errResult(err)
		}
		if args.IsDisabled != nil {
			body["is_disabled"] = *args.IsDisabled
		}
		if len(body) == 0 {
			return errResult(errors.New("nothing to change: pass at least one field"))
		}
		c, ref, res := withAddon(args.addonArgs)
		if res != nil {
			return res, nil, nil
		}
		var a addon
		if err := c.call(ctx, http.MethodPatch, addonPath(ref), nil, body, &a); err != nil {
			return errResult(err)
		}
		rememberAddon(ref)
		keys := make([]string, 0, len(body))
		for k := range body {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		return textResult(fmt.Sprintf("Updated %s: %s.\n\n%s", ref, strings.Join(keys, ", "), renderAddon(a)))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "amo_addon_icon",
		Title:       "Set Add-on Icon",
		Description: "Upload the add-on icon (square JPEG or PNG, 128×128 recommended; AMO resizes it to 32/64/128 in the background), or clear:true to go back to the default icon.",
		Annotations: writeLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		addonArgs
		Path  string `json:"path,omitempty" jsonschema:"Local .png or .jpg file."`
		Clear bool   `json:"clear,omitempty" jsonschema:"Remove the custom icon instead."`
	}) (*mcp.CallToolResult, any, error) {
		if (args.Path == "") == !args.Clear {
			return errResult(errors.New("pass path (an image file) or clear:true"))
		}
		var files []filePart
		if args.Path != "" {
			name, data, err := readImage(args.Path, 4<<20)
			if err != nil {
				return errResult(err)
			}
			files = append(files, filePart{Field: "icon", Name: name, Data: data})
		}
		c, ref, res := withAddon(args.addonArgs)
		if res != nil {
			return res, nil, nil
		}
		var a addon
		var err error
		if args.Clear {
			err = c.call(ctx, http.MethodPatch, addonPath(ref), nil, map[string]any{"icon": nil}, &a)
		} else {
			err = c.multipartCall(ctx, http.MethodPatch, addonPath(ref), nil, files, &a)
		}
		if err != nil {
			return errResult(err)
		}
		rememberAddon(ref)
		if args.Clear {
			return textResult(fmt.Sprintf("Icon of %s reset to the default.", ref))
		}
		return textResult(fmt.Sprintf("Icon of %s uploaded; resized copies appear shortly: %s", ref, a.IconURL))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "amo_addon_create",
		Title: "Create Add-on",
		Description: "Create a new add-on from a package: path (uploaded and validated first) or a valid upload UUID. " +
			"Listed (default) needs name, summary, categories and a license (e.g. MPL-2.0); it then goes to Mozilla review. " +
			"Unlisted only needs the package: it is signed for self-distribution. The manifest needs browser_specific_settings.gecko.id or AMO assigns a guid.",
		Annotations: releaseLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		submitArgs
		listingArgs
		versionFields
	}) (*mcp.CallToolResult, any, error) {
		body, err := args.listingArgs.body()
		if err != nil {
			return errResult(err)
		}
		vb, err := args.versionFields.body()
		if err != nil {
			return errResult(err)
		}
		c, err := newAMOClient()
		if err != nil {
			return errResult(err)
		}
		uuid, report, err := resolveUpload(ctx, c, args.submitArgs)
		if err != nil {
			return errResult(joinReport(report, err))
		}
		vb["upload"] = uuid
		body["version"] = vb
		var a addon
		if err := c.call(ctx, http.MethodPost, "/addons/addon/", nil, body, &a); err != nil {
			return errResult(joinReport(report, err))
		}
		ref := firstNonEmpty(a.Slug, itoa(a.ID))
		rememberAddon(ref)
		var b strings.Builder
		b.WriteString(report)
		fmt.Fprintf(&b, "\nCreated add-on %s (id %d, guid %s).\n", a.Name.pick(a.DefaultLocale), a.ID, a.GUID)
		if a.Version != nil {
			fmt.Fprintf(&b, "Version %s (id %d, %s): file %s.\n", a.Version.Version, a.Version.ID, a.Version.Channel, fileStatus(a.Version))
			if args.SourcePath != "" {
				if err := attachSource(ctx, c, ref, a.Version.ID, args.SourcePath); err != nil {
					fmt.Fprintf(&b, "Source upload FAILED: %v — retry with amo_version_edit {\"version\": \"%d\", \"sourcePath\": …}.\n", err, a.Version.ID)
				} else {
					b.WriteString("Source code attached.\n")
				}
			}
		}
		fmt.Fprintf(&b, "Edit page: %s\n", a.EditURL)
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "amo_addon_delete",
		Title: "Delete Add-on",
		Description: "PERMANENTLY delete an add-on: all versions and files are deleted and soft-blocked, and its guid can never be submitted again. " +
			"Owner only. confirm must equal the add-on's slug or guid. Confirm with the user first.",
		Annotations: deleteLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		addonArgs
		Confirm string `json:"confirm" jsonschema:"The add-on's slug or guid, typed out to confirm the deletion."`
	}) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.Addon) == "" {
			return errResult(errors.New("addon is required for a deletion (the remembered add-on is not used)"))
		}
		c, ref, res := withAddon(args.addonArgs)
		if res != nil {
			return res, nil, nil
		}
		a, err := c.getAddon(ctx, ref, nil)
		if err != nil {
			return errResult(err)
		}
		if args.Confirm == "" || (args.Confirm != a.Slug && args.Confirm != a.GUID) {
			return errResult(fmt.Errorf("confirm must be the slug %q or guid %q of %q", a.Slug, a.GUID, a.Name.pick(a.DefaultLocale)))
		}
		var tok struct {
			DeleteConfirm string `json:"delete_confirm"`
		}
		if err := c.call(ctx, http.MethodGet, addonPath(ref)+"delete_confirm/", nil, nil, &tok); err != nil {
			return errResult(err)
		}
		if err := c.call(ctx, http.MethodDelete, addonPath(ref), url.Values{"delete_confirm": {tok.DeleteConfirm}}, nil, nil); err != nil {
			return errResult(err)
		}
		rememberAddon("")
		return textResult(fmt.Sprintf("Deleted add-on %s (id %d, guid %s).", a.Slug, a.ID, a.GUID))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "amo_upload",
		Title: "Upload Package",
		Description: "Upload an .xpi/.zip or an extension folder to AMO for validation, without creating a version. Waits for the validator (waitSeconds) and returns the upload UUID, " +
			"errors and warnings. Use the UUID with amo_version_create or amo_addon_create (or pass path to those tools directly).",
		Annotations: writeLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		Path        string `json:"path" jsonschema:"Local .xpi/.zip or extension folder."`
		Channel     string `json:"channel,omitempty" jsonschema:"listed (default) or unlisted; must match the version you create."`
		WaitSeconds int    `json:"waitSeconds,omitempty" jsonschema:"Max seconds to wait for validation, 0-600 (default 180)."`
	}) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.Path) == "" {
			return errResult(errors.New("path is required"))
		}
		ch, err := channelArg(args.Channel)
		if err != nil {
			return errResult(err)
		}
		wait, err := waitArg(args.WaitSeconds, 180)
		if err != nil {
			return errResult(err)
		}
		pkg, err := loadPackage(args.Path)
		if err != nil {
			return errResult(err)
		}
		c, err := newAMOClient()
		if err != nil {
			return errResult(err)
		}
		up, err := c.uploadPackage(ctx, pkg, ch, wait)
		if err != nil {
			return errResult(fmt.Errorf("upload of %s failed: %w", args.Path, err))
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Uploaded %s (%s, version %s, %s channel)", args.Path, humanBytes(len(pkg.Data)), render.OrDash(pkg.Version), ch)
		if pkg.GeckoID != "" {
			fmt.Fprintf(&b, ", gecko id %s", pkg.GeckoID)
		}
		b.WriteString(".\n")
		if !pkg.Manifest {
			b.WriteString("Note: no manifest.json at the package root.\n")
		}
		b.WriteString(renderUpload(up))
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "amo_upload_status",
		Title:       "Upload Status",
		Description: "Validation status of an upload by UUID (processed, valid, submitted, errors/warnings); waitSeconds polls until processed. Without uuid: your recent uploads.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		UUID        string `json:"uuid,omitempty" jsonschema:"Upload UUID. Omit to list recent uploads."`
		WaitSeconds int    `json:"waitSeconds,omitempty" jsonschema:"Poll up to this many seconds until processed, 0-600 (default 0)."`
	}) (*mcp.CallToolResult, any, error) {
		c, err := newAMOClient()
		if err != nil {
			return errResult(err)
		}
		if args.UUID == "" {
			var out page[upload]
			if err := c.call(ctx, http.MethodGet, "/addons/upload/", url.Values{"page_size": {"25"}}, nil, &out); err != nil {
				return errResult(err)
			}
			return textResult(renderUploadList(out))
		}
		if !uuidRE.MatchString(args.UUID) {
			return errResult(fmt.Errorf("uuid %q is not an upload UUID", args.UUID))
		}
		wait, err := waitArg(args.WaitSeconds, 0)
		if err != nil {
			return errResult(err)
		}
		up, err := c.getUpload(ctx, args.UUID)
		if err != nil {
			return errResult(err)
		}
		if up, err = c.waitUpload(ctx, up, wait); err != nil {
			return errResult(err)
		}
		return textResult(renderUpload(up))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "amo_versions",
		Title: "Add-on Versions",
		Description: "Versions of an add-on, newest first: id, number, channel, file status (public / unreviewed / disabled), disabled flag, review date, compatibility, size. " +
			"Default filter all_with_unlisted (needs authorship); public versions of any add-on work without a key with filter public.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		addonArgs
		Filter   string `json:"filter,omitempty" jsonschema:"all_with_unlisted (default), all_without_unlisted, enterprise_only or public."`
		Page     int    `json:"page,omitempty" jsonschema:"1-based page."`
		PageSize int    `json:"pageSize,omitempty" jsonschema:"Versions per page, 1-50 (default 25)."`
	}) (*mcp.CallToolResult, any, error) {
		ref, err := resolveAddon(args.Addon)
		if err != nil {
			return errResult(err)
		}
		filter := firstNonEmpty(args.Filter, "all_with_unlisted")
		if !slices.Contains(versionLists, filter) {
			return errResult(fmt.Errorf("filter %q must be one of %s", filter, strings.Join(versionLists, ", ")))
		}
		q := url.Values{}
		if filter != "public" {
			q.Set("filter", filter)
		}
		if err := setPage(q, args.Page, args.PageSize, 50); err != nil {
			return errResult(err)
		}
		c, err := newPublicClient()
		if err != nil {
			return errResult(err)
		}
		var out page[addonVersion]
		if err := c.call(ctx, http.MethodGet, addonPath(ref)+"versions/", q, nil, &out); err != nil {
			return errResult(err)
		}
		rememberAddon(ref)
		return textResult(renderVersionList(ref, out, args.Page))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "amo_version",
		Title: "Version Details",
		Description: "One version: channel, file status, size, hash, download URL, permissions (incl. host and data collection), compatibility, license, release notes, " +
			"reviewer approval notes and source code link (own add-ons).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		addonArgs
		Version string `json:"version" jsonschema:"Version id or number (1.2.0; prefix v for a dotless number, e.g. v2)."`
	}) (*mcp.CallToolResult, any, error) {
		ref, err := resolveAddon(args.Addon)
		if err != nil {
			return errResult(err)
		}
		vref, err := versionRef(args.Version)
		if err != nil {
			return errResult(err)
		}
		c, err := newPublicClient()
		if err != nil {
			return errResult(err)
		}
		var v addonVersion
		if err := c.call(ctx, http.MethodGet, versionPath(ref, vref), nil, nil, &v); err != nil {
			return errResult(err)
		}
		rememberAddon(ref)
		return textResult(renderVersion(v))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "amo_version_create",
		Title: "Submit Version",
		Description: "Submit a new version of an existing add-on: path (an .xpi/.zip or extension folder, uploaded and validated first) or a valid upload UUID, plus release notes, " +
			"reviewer notes, license, compatibility and sourcePath (needed when code is minified/bundled). Listed versions go to Mozilla review, unlisted ones are signed. " +
			"Listed versions need name, summary, categories and a license on the add-on. Confirm with the user before submitting a listed version.",
		Annotations: releaseLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		addonArgs
		submitArgs
		versionFields
	}) (*mcp.CallToolResult, any, error) {
		vb, err := args.versionFields.body()
		if err != nil {
			return errResult(err)
		}
		c, ref, res := withAddon(args.addonArgs)
		if res != nil {
			return res, nil, nil
		}
		uuid, report, err := resolveUpload(ctx, c, args.submitArgs)
		if err != nil {
			return errResult(joinReport(report, err))
		}
		vb["upload"] = uuid
		var v addonVersion
		if err := c.call(ctx, http.MethodPost, addonPath(ref)+"versions/", nil, vb, &v); err != nil {
			return errResult(joinReport(report, err))
		}
		rememberAddon(ref)
		var b strings.Builder
		b.WriteString(report)
		fmt.Fprintf(&b, "\nCreated version %s (id %d, %s channel) of %s. File status: %s.\n", v.Version, v.ID, v.Channel, ref, fileStatus(&v))
		if args.SourcePath != "" {
			if err := attachSource(ctx, c, ref, v.ID, args.SourcePath); err != nil {
				fmt.Fprintf(&b, "Source upload FAILED: %v — retry with amo_version_edit {\"version\": \"%d\", \"sourcePath\": …}.\n", err, v.ID)
			} else {
				b.WriteString("Source code attached.\n")
			}
		}
		switch v.Channel {
		case "listed":
			b.WriteString("Listed: it goes live after Mozilla's review (auto-approval is often minutes). Check amo_version.\n")
		case "unlisted":
			b.WriteString("Unlisted: once signed, download the .xpi from the file URL in amo_version.\n")
		}
		if v.EditURL != "" {
			fmt.Fprintf(&b, "Edit page: %s\n", v.EditURL)
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "amo_version_edit",
		Title: "Edit Version",
		Description: "Edit a version: release notes, reviewer approval notes, license, compatibility, and the source code (sourcePath, or clearSource). " +
			"isDisabled:true disables the version (users stop getting it; confirm with the user), false re-enables it.",
		Annotations: writeLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		addonArgs
		Version string `json:"version" jsonschema:"Version id or number (prefix v for a dotless number)."`
		versionFields
		IsDisabled  *bool  `json:"isDisabled,omitempty" jsonschema:"Disable (true) or re-enable (false) the version. Confirm with the user."`
		SourcePath  string `json:"sourcePath,omitempty" jsonschema:"Source code archive (.zip/.tar.gz) or folder to attach (replaces the current one)."`
		ClearSource bool   `json:"clearSource,omitempty" jsonschema:"Remove the attached source code."`
	}) (*mcp.CallToolResult, any, error) {
		vref, err := versionRef(args.Version)
		if err != nil {
			return errResult(err)
		}
		body, err := args.versionFields.body()
		if err != nil {
			return errResult(err)
		}
		if args.IsDisabled != nil {
			body["is_disabled"] = *args.IsDisabled
		}
		if args.ClearSource {
			if args.SourcePath != "" {
				return errResult(errors.New("pass sourcePath or clearSource, not both"))
			}
			body["source"] = nil
		}
		if len(body) == 0 && args.SourcePath == "" {
			return errResult(errors.New("nothing to change: pass at least one field"))
		}
		c, ref, res := withAddon(args.addonArgs)
		if res != nil {
			return res, nil, nil
		}
		var v addonVersion
		if len(body) > 0 {
			if err := c.call(ctx, http.MethodPatch, versionPath(ref, vref), nil, body, &v); err != nil {
				return errResult(err)
			}
		}
		if args.SourcePath != "" {
			name, data, err := loadSource(args.SourcePath)
			if err != nil {
				return errResult(err)
			}
			if err := c.multipartCall(ctx, http.MethodPatch, versionPath(ref, vref), nil,
				[]filePart{{Field: "source", Name: name, Data: data}}, &v); err != nil {
				return errResult(err)
			}
		}
		rememberAddon(ref)
		return textResult("Updated.\n\n" + renderVersion(v))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "amo_version_delete",
		Title:       "Delete Version",
		Description: "Delete a version. Its file is soft-blocked: Firefox stops installing it, existing users may re-enable it. The version number cannot be reused. Confirm with the user first.",
		Annotations: deleteLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		addonArgs
		Version string `json:"version" jsonschema:"Version id or number (prefix v for a dotless number)."`
	}) (*mcp.CallToolResult, any, error) {
		vref, err := versionRef(args.Version)
		if err != nil {
			return errResult(err)
		}
		c, ref, res := withAddon(args.addonArgs)
		if res != nil {
			return res, nil, nil
		}
		if err := c.call(ctx, http.MethodDelete, versionPath(ref, vref), nil, nil, nil); err != nil {
			return errResult(err)
		}
		rememberAddon(ref)
		return textResult(fmt.Sprintf("Deleted version %s of %s.", args.Version, ref))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "amo_version_rollback",
		Title: "Roll Back Version",
		Description: "Re-release an earlier approved version under a new, higher version number (e.g. 1.3 is broken → version 1.2, newVersion 1.3.1). " +
			"Listed channel: only the previous approved version qualifies, and a listed version awaiting review is disabled. Asynchronous: AMO emails the result. Confirm with the user first.",
		Annotations: releaseLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		addonArgs
		Version      string            `json:"version" jsonschema:"The earlier approved version to restore (id or number)."`
		NewVersion   string            `json:"newVersion" jsonschema:"New version number, unique across channels and (listed) higher than every signed version."`
		ReleaseNotes map[string]string `json:"releaseNotes,omitempty" jsonschema:"Release notes by locale."`
	}) (*mcp.CallToolResult, any, error) {
		vref, err := versionRef(args.Version)
		if err != nil {
			return errResult(err)
		}
		if strings.TrimSpace(args.NewVersion) == "" {
			return errResult(errors.New("newVersion is required"))
		}
		body := map[string]any{"new_version_string": strings.TrimSpace(args.NewVersion)}
		if t := translatedArg(args.ReleaseNotes); t != nil {
			body["release_notes"] = t
		}
		c, ref, res := withAddon(args.addonArgs)
		if res != nil {
			return res, nil, nil
		}
		if err := c.call(ctx, http.MethodPost, versionPath(ref, vref)+"rollback/", nil, body, nil); err != nil {
			return errResult(err)
		}
		rememberAddon(ref)
		return textResult(fmt.Sprintf("Rollback of %s to %s as %s accepted. It runs in the background; AMO emails the result. Check amo_versions.", ref, args.Version, args.NewVersion))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "amo_preview_add",
		Title: "Add Screenshot",
		Description: "Add a screenshot (preview image, PNG/JPEG; 1280×800 recommended) to a non-theme add-on, with optional position and caption. " +
			"amo_addon lists existing previews with their ids.",
		Annotations: writeLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		addonArgs
		Path     string            `json:"path" jsonschema:"Local .png or .jpg file."`
		Position *int              `json:"position,omitempty" jsonschema:"Sort position, ascending."`
		Caption  map[string]string `json:"caption,omitempty" jsonschema:"Caption by locale."`
	}) (*mcp.CallToolResult, any, error) {
		name, data, err := readImage(args.Path, 10<<20)
		if err != nil {
			return errResult(err)
		}
		c, ref, res := withAddon(args.addonArgs)
		if res != nil {
			return res, nil, nil
		}
		fields := map[string]string{}
		if args.Position != nil {
			fields["position"] = itoa(*args.Position)
		}
		var p preview
		if err := c.multipartCall(ctx, http.MethodPost, addonPath(ref)+"previews/", fields,
			[]filePart{{Field: "image", Name: name, Data: data}}, &p); err != nil {
			return errResult(err)
		}
		rememberAddon(ref)
		msg := fmt.Sprintf("Added preview %d (position %d) to %s: %s", p.ID, p.Position, ref, p.ImageURL)
		if t := translatedArg(args.Caption); t != nil {
			if err := c.call(ctx, http.MethodPatch, addonPath(ref)+"previews/"+itoa(p.ID)+"/", nil, map[string]any{"caption": t}, nil); err != nil {
				return errResult(fmt.Errorf("%s\nSetting the caption failed: %w", msg, err))
			}
			msg += "\nCaption set."
		}
		return textResult(msg)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "amo_preview_edit",
		Title:       "Edit Screenshot",
		Description: "Change a screenshot's caption or position (preview ids from amo_addon).",
		Annotations: writeLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		addonArgs
		PreviewID int               `json:"previewId" jsonschema:"Preview id."`
		Position  *int              `json:"position,omitempty" jsonschema:"Sort position, ascending."`
		Caption   map[string]string `json:"caption,omitempty" jsonschema:"Caption by locale; an empty string deletes that translation."`
	}) (*mcp.CallToolResult, any, error) {
		body := map[string]any{}
		if args.Position != nil {
			body["position"] = *args.Position
		}
		if t := translatedArg(args.Caption); t != nil {
			body["caption"] = t
		}
		if args.PreviewID <= 0 || len(body) == 0 {
			return errResult(errors.New("previewId and at least one of caption, position are required"))
		}
		c, ref, res := withAddon(args.addonArgs)
		if res != nil {
			return res, nil, nil
		}
		if err := c.call(ctx, http.MethodPatch, addonPath(ref)+"previews/"+itoa(args.PreviewID)+"/", nil, body, nil); err != nil {
			return errResult(err)
		}
		rememberAddon(ref)
		return textResult(fmt.Sprintf("Updated preview %d of %s.", args.PreviewID, ref))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "amo_preview_delete",
		Title:       "Delete Screenshot",
		Description: "Delete a screenshot of a non-theme add-on (preview ids from amo_addon).",
		Annotations: deleteLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		addonArgs
		PreviewID int `json:"previewId" jsonschema:"Preview id."`
	}) (*mcp.CallToolResult, any, error) {
		if args.PreviewID <= 0 {
			return errResult(errors.New("previewId is required"))
		}
		c, ref, res := withAddon(args.addonArgs)
		if res != nil {
			return res, nil, nil
		}
		if err := c.call(ctx, http.MethodDelete, addonPath(ref)+"previews/"+itoa(args.PreviewID)+"/", nil, nil, nil); err != nil {
			return errResult(err)
		}
		rememberAddon(ref)
		return textResult(fmt.Sprintf("Deleted preview %d of %s.", args.PreviewID, ref))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "amo_eula_policy",
		Title:       "EULA and Privacy Policy",
		Description: "Read an add-on's End-User License Agreement and Privacy Policy (all locales).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args addonArgs) (*mcp.CallToolResult, any, error) {
		ref, err := resolveAddon(args.Addon)
		if err != nil {
			return errResult(err)
		}
		c, err := newPublicClient()
		if err != nil {
			return errResult(err)
		}
		var out struct {
			EULA          translated `json:"eula"`
			PrivacyPolicy translated `json:"privacy_policy"`
		}
		if err := c.call(ctx, http.MethodGet, addonPath(ref)+"eula_policy/", nil, nil, &out); err != nil {
			return errResult(err)
		}
		rememberAddon(ref)
		var b strings.Builder
		for _, f := range []struct {
			label string
			t     translated
		}{{"EULA", out.EULA}, {"Privacy policy", out.PrivacyPolicy}} {
			if len(f.t.locales()) == 0 {
				fmt.Fprintf(&b, "## %s\n(none)\n\n", f.label)
				continue
			}
			for _, l := range f.t.locales() {
				fmt.Fprintf(&b, "## %s [%s]\n%s\n\n", f.label, l, *f.t[l])
			}
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "amo_eula_policy_edit",
		Title: "Edit EULA and Privacy Policy",
		Description: "Set an add-on's EULA and/or Privacy Policy by locale (not for themes). Locales not passed stay unchanged; an empty string deletes that translation, " +
			"deleting every locale removes the document.",
		Annotations: writeLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		addonArgs
		EULA          map[string]string `json:"eula,omitempty" jsonschema:"EULA text by locale."`
		PrivacyPolicy map[string]string `json:"privacyPolicy,omitempty" jsonschema:"Privacy policy text by locale."`
	}) (*mcp.CallToolResult, any, error) {
		body := map[string]any{}
		if t := translatedArg(args.EULA); t != nil {
			body["eula"] = t
		}
		if t := translatedArg(args.PrivacyPolicy); t != nil {
			body["privacy_policy"] = t
		}
		if len(body) == 0 {
			return errResult(errors.New("pass eula and/or privacyPolicy"))
		}
		c, ref, res := withAddon(args.addonArgs)
		if res != nil {
			return res, nil, nil
		}
		if err := c.call(ctx, http.MethodPatch, addonPath(ref)+"eula_policy/", nil, body, nil); err != nil {
			return errResult(err)
		}
		rememberAddon(ref)
		return textResult(fmt.Sprintf("Updated the EULA / privacy policy of %s.", ref))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "amo_ratings",
		Title: "Add-on Ratings",
		Description: "User ratings of an add-on, newest first: date, stars, version, author, text and the developer reply. Filter by score, text only, or version id; " +
			"includes the per-star counts. No API key needed. Replying is not available with API keys: use the AMO website.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		addonArgs
		Score     []int `json:"score,omitempty" jsonschema:"Only these star scores, e.g. [1, 2]."`
		WithText  bool  `json:"withText,omitempty" jsonschema:"Only ratings with review text."`
		VersionID int   `json:"versionId,omitempty" jsonschema:"Only ratings of this version id."`
		Page      int   `json:"page,omitempty" jsonschema:"1-based page."`
		PageSize  int   `json:"pageSize,omitempty" jsonschema:"Ratings per page, 1-50 (default 25)."`
	}) (*mcp.CallToolResult, any, error) {
		ref, err := resolveAddon(args.Addon)
		if err != nil {
			return errResult(err)
		}
		q := url.Values{"addon": {ref}, "show_grouped_ratings": {"true"}}
		var scores []string
		for _, s := range args.Score {
			if s < 1 || s > 5 {
				return errResult(fmt.Errorf("score must be 1-5, got %d", s))
			}
			scores = append(scores, itoa(s))
		}
		if len(scores) > 0 {
			q.Set("score", strings.Join(scores, ","))
		}
		if args.WithText {
			q.Set("filter", "without_empty_body")
		}
		if args.VersionID > 0 {
			q.Set("version", itoa(args.VersionID))
		}
		if err := setPage(q, args.Page, args.PageSize, 50); err != nil {
			return errResult(err)
		}
		c, err := newPublicClient()
		if err != nil {
			return errResult(err)
		}
		var out page[rating]
		if err := c.call(ctx, http.MethodGet, "/ratings/rating/", q, nil, &out); err != nil {
			return errResult(err)
		}
		rememberAddon(ref)
		return textResult(renderRatings(out, args.Page))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "amo_search",
		Title: "Search Add-ons",
		Description: "Search public add-ons on AMO (no key needed): by text, type, app, category, author, guid, tag or promoted status; " +
			"sort by relevance, users, downloads, rating, created, updated, hotness. Good for competitor research and finding an add-on's id.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		Query    string `json:"query,omitempty" jsonschema:"Search text (max 100 chars)."`
		Type     string `json:"type,omitempty" jsonschema:"extension, statictheme, dictionary, language, search."`
		App      string `json:"app,omitempty" jsonschema:"firefox or android."`
		Category string `json:"category,omitempty" jsonschema:"Category slug (needs app and type)."`
		Author   string `json:"author,omitempty" jsonschema:"Author username or user id (comma-separated for several)."`
		GUID     string `json:"guid,omitempty" jsonschema:"Exact guid(s), comma-separated."`
		Tag      string `json:"tag,omitempty" jsonschema:"Tag name(s), comma-separated (all must match)."`
		Promoted string `json:"promoted,omitempty" jsonschema:"recommended, line, badged, …"`
		Sort     string `json:"sort,omitempty" jsonschema:"relevance, users, downloads, rating, created, updated, hotness, recommended (comma-separated to combine)."`
		Page     int    `json:"page,omitempty" jsonschema:"1-based page."`
		PageSize int    `json:"pageSize,omitempty" jsonschema:"Results per page, 1-50 (default 25)."`
	}) (*mcp.CallToolResult, any, error) {
		q := url.Values{}
		for k, v := range map[string]string{"q": args.Query, "app": args.App, "category": args.Category, "author": args.Author,
			"guid": args.GUID, "tag": args.Tag, "promoted": args.Promoted} {
			if v = strings.TrimSpace(v); v != "" {
				q.Set(k, v)
			}
		}
		if len(args.Query) > 100 {
			return errResult(errors.New("query is limited to 100 characters"))
		}
		if args.Type != "" {
			for _, t := range strings.Split(args.Type, ",") {
				if !slices.Contains(append(addonTypes, "theme", "persona"), strings.TrimSpace(t)) {
					return errResult(fmt.Errorf("type %q must be one of %s", t, strings.Join(addonTypes, ", ")))
				}
			}
			q.Set("type", args.Type)
		}
		if args.Sort != "" {
			for _, s := range strings.Split(args.Sort, ",") {
				if !slices.Contains(searchSorts, strings.TrimSpace(s)) {
					return errResult(fmt.Errorf("sort %q must be one of %s", s, strings.Join(searchSorts, ", ")))
				}
			}
			q.Set("sort", args.Sort)
		}
		if err := setPage(q, args.Page, args.PageSize, 50); err != nil {
			return errResult(err)
		}
		c, err := newPublicClient()
		if err != nil {
			return errResult(err)
		}
		var out page[addon]
		if err := c.call(ctx, http.MethodGet, "/addons/search/", q, nil, &out); err != nil {
			return errResult(err)
		}
		return textResult(renderAddonList(out, args.Page, false))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "amo_request",
		Title: "Raw AMO API Request",
		Description: "Send any AMO API v5 request (signed with your key) and return the JSON: for endpoints without a tool (accounts, collections, activity/review notes, " +
			"add-on authors, …). path is relative to /api/v5, e.g. /accounts/profile/. Confirm DELETE and other destructive calls with the user first.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive, OpenWorldHint: &openWorld},
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		Method string            `json:"method,omitempty" jsonschema:"GET (default), POST, PATCH, PUT or DELETE."`
		Path   string            `json:"path" jsonschema:"Path under /api/v5, e.g. /addons/addon/my-addon/authors/."`
		Query  map[string]string `json:"query,omitempty" jsonschema:"Query parameters."`
		Body   map[string]any    `json:"body,omitempty" jsonschema:"JSON body for POST/PATCH/PUT."`
	}) (*mcp.CallToolResult, any, error) {
		method := strings.ToUpper(firstNonEmpty(strings.TrimSpace(args.Method), "GET"))
		if !slices.Contains([]string{"GET", "POST", "PATCH", "PUT", "DELETE"}, method) {
			return errResult(fmt.Errorf("method %q must be GET, POST, PATCH, PUT or DELETE", args.Method))
		}
		p := strings.TrimSpace(args.Path)
		p = strings.TrimPrefix(strings.TrimPrefix(p, "/api/v5"), "api/v5")
		if p == "" || strings.Contains(p, "://") || strings.Contains(p, "..") {
			return errResult(errors.New("path must be relative to /api/v5, e.g. /accounts/profile/"))
		}
		q := url.Values{}
		for k, v := range args.Query {
			q.Set(k, v)
		}
		c, err := newAMOClient()
		if err != nil {
			return errResult(err)
		}
		var body any
		if args.Body != nil {
			body = args.Body
		}
		data, status, err := c.Do(ctx, method, c.url(p, q), body)
		if err != nil {
			return errResult(err)
		}
		if len(data) == 0 {
			return textResult(fmt.Sprintf("%s %s → %d (no content)", method, p, status))
		}
		var pretty any
		if json.Unmarshal(data, &pretty) == nil {
			if out, err := json.MarshalIndent(pretty, "", "  "); err == nil {
				data = out
			}
		}
		return textResult(fmt.Sprintf("%s %s → %d\n%s", method, p, status, data))
	})
}

func joinReport(report string, err error) error {
	if report == "" {
		return err
	}
	return fmt.Errorf("%s\n%w", strings.TrimRight(report, "\n"), err)
}

// readImage reads a PNG or JPEG file.
func readImage(path string, max int64) (string, []byte, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil, errors.New("path is required: a .png or .jpg file")
	}
	name, data, err := readFile(path, max)
	if err != nil {
		return "", nil, err
	}
	ct := http.DetectContentType(data)
	if ct != "image/png" && ct != "image/jpeg" {
		return "", nil, fmt.Errorf("%s is %s; AMO accepts PNG or JPEG", path, ct)
	}
	return name, data, nil
}
