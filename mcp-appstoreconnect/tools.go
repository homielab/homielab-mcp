package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/mcpkit"
	"github.com/homielab/homielab-mcp/internal/render"
)

type emptyArgs = mcpkit.EmptyArgs

func textResult(s string) (*mcp.CallToolResult, any, error) { return mcpkit.TextResult(s) }

func errResult(err error) (*mcp.CallToolResult, any, error) { return mcpkit.ErrorResult(err) }

// withApp returns a client and the resolved app.
func withApp(ctx context.Context, app string) (*ascClient, rememberedApp, error) {
	c, err := newASCClient()
	if err != nil {
		return nil, rememberedApp{}, err
	}
	a, err := c.resolveApp(ctx, app)
	return c, a, err
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// shortDate turns RFC 3339 into "2006-01-02 15:04".
func shortDate(s string) string {
	if len(s) >= 16 {
		return strings.Replace(s[:16], "T", " ", 1)
	}
	return render.OrDash(s)
}

func is404(err error) bool { return err != nil && strings.Contains(err.Error(), " 404:") }
func is409(err error) bool { return err != nil && strings.Contains(err.Error(), " 409:") }

const appDesc = "App: Apple ID, bundle id or app name. Omit to reuse the last used app."

// versionArgs select an App Store version; all empty = the editable version.
type versionArgs struct {
	App           string `json:"app,omitempty" jsonschema:"App: Apple ID, bundle id or app name. Omit to reuse the last used app."`
	VersionID     string `json:"versionId,omitempty" jsonschema:"App Store version id (from asc_versions)."`
	VersionString string `json:"versionString,omitempty" jsonschema:"Version string, e.g. 1.4.0. Omit (with versionId) for the editable version."`
	Platform      string `json:"platform,omitempty" jsonschema:"IOS (default), MAC_OS, TV_OS or VISION_OS."`
}

func (a versionArgs) resolve(ctx context.Context, fallbackLatest bool) (*ascClient, rememberedApp, resource[versionAttrs], included, error) {
	var v resource[versionAttrs]
	platform, err := normPlatform(a.Platform)
	if err != nil {
		return nil, rememberedApp{}, v, nil, err
	}
	c, app, err := withApp(ctx, a.App)
	if err != nil {
		return nil, app, v, nil, err
	}
	v, in, err := c.resolveVersion(ctx, app.ID, a.VersionID, a.VersionString, platform, fallbackLatest)
	return c, app, v, in, err
}

// buildArgs select a build.
type buildArgs struct {
	BuildID     string `json:"buildId,omitempty" jsonschema:"Build id (from asc_builds)."`
	BuildNumber string `json:"buildNumber,omitempty" jsonschema:"Build number (CFBundleVersion), e.g. 45, or \"latest\" for the newest upload."`
}

func (b buildArgs) set() bool { return b.BuildID != "" || b.BuildNumber != "" }

func registerTools(s *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	idempotent := &mcp.ToolAnnotations{IdempotentHint: true}
	destructive := true
	dangerous := &mcp.ToolAnnotations{DestructiveHint: &destructive}

	// ---- auth & apps ----

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_auth_status",
		Title:       "Check API Key",
		Description: "Show which App Store Connect API key is configured (key id, issuer, key file, vendor number, remembered app) and test it with one API call. Use when any tool fails with an auth error.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		c, err := newASCClient()
		if err != nil {
			return errResult(err)
		}
		var b strings.Builder
		b.WriteString("App Store Connect API key:\n")
		fmt.Fprintf(&b, "  key id:        %s\n", c.creds.KeyID)
		if c.creds.IssuerID != "" {
			fmt.Fprintf(&b, "  issuer id:     %s (team key)\n", c.creds.IssuerID)
		} else {
			b.WriteString("  issuer id:     — (individual key)\n")
		}
		fmt.Fprintf(&b, "  private key:   %s\n", firstNonEmpty(c.creds.KeyPath, "ASC_PRIVATE_KEY env"))
		fmt.Fprintf(&b, "  vendor number: %s\n", render.OrDash(c.creds.VendorNumber))
		if a, ok := loadRememberedApp(); ok {
			fmt.Fprintf(&b, "  last app:      %s\n", a)
		}
		_, _, err = list[appAttrs](ctx, c, "/v1/apps", nil, 1)
		if err != nil {
			fmt.Fprintf(&b, "\nTest call FAILED: %v\n", err)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: b.String()}}, IsError: true}, nil, nil
		}
		fmt.Fprintf(&b, "\nTest call OK. Ready: call any asc_* tool.\n")
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_apps",
		Title:       "List Apps",
		Description: "List the apps this API key can see: name, bundle id, SKU, Apple ID, primary locale.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BundleID string `json:"bundleId,omitempty" jsonschema:"Only the app with this bundle id."`
	}) (*mcp.CallToolResult, any, error) {
		c, err := newASCClient()
		if err != nil {
			return errResult(err)
		}
		apps, err := c.listApps(ctx, args.BundleID)
		if err != nil {
			return errResult(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d app(s):\n\n| Name | Bundle ID | SKU | Apple ID | Locale |\n| --- | --- | --- | --- | --- |\n", len(apps))
		for _, a := range apps {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", render.MDCell(a.Attributes.Name), a.Attributes.BundleID, render.MDCell(a.Attributes.SKU), a.ID, a.Attributes.PrimaryLocale)
		}
		return textResult(b.String())
	})

	// ---- builds & TestFlight ----

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_builds",
		Title:       "List Builds",
		Description: "Uploaded builds of an app, newest first: marketing version, build number, platform, processing state, TestFlight internal/external state, upload and expiry dates, build id.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		App             string `json:"app,omitempty" jsonschema:"App: Apple ID, bundle id or app name. Omit to reuse the last used app."`
		Version         string `json:"version,omitempty" jsonschema:"Only builds of this marketing version, e.g. 1.4.0."`
		Platform        string `json:"platform,omitempty" jsonschema:"Only this platform: IOS, MAC_OS, TV_OS, VISION_OS."`
		ProcessingState string `json:"processingState,omitempty" jsonschema:"PROCESSING, FAILED, INVALID or VALID."`
		Limit           int    `json:"limit,omitempty" jsonschema:"Max builds (default 20)."`
	}) (*mcp.CallToolResult, any, error) {
		c, app, err := withApp(ctx, args.App)
		if err != nil {
			return errResult(err)
		}
		q := url.Values{"filter[app]": {app.ID}, "sort": {"-uploadedDate"}, "include": {"preReleaseVersion,buildBetaDetail"}}
		if args.Version != "" {
			q.Set("filter[preReleaseVersion.version]", args.Version)
		}
		if args.Platform != "" {
			p, err := normPlatform(args.Platform)
			if err != nil {
				return errResult(err)
			}
			q.Set("filter[preReleaseVersion.platform]", p)
		}
		if args.ProcessingState != "" {
			q.Set("filter[processingState]", strings.ToUpper(args.ProcessingState))
		}
		builds, in, err := list[buildAttrs](ctx, c, "/v1/builds", q, cmpOr(args.Limit, 20))
		if err != nil {
			return errResult(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Builds of %s — %d shown:\n\n", app, len(builds))
		b.WriteString("| Version | Build | Platform | Processing | TestFlight internal | TestFlight external | Uploaded | Expires | Build id |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for _, x := range builds {
			var pre preReleaseAttrs
			in.get("preReleaseVersions", x.rel("preReleaseVersion"), &pre)
			var bd buildBetaDetailAttrs
			in.get("buildBetaDetails", x.rel("buildBetaDetail"), &bd)
			exp := shortDate(x.Attributes.ExpirationDate)
			if x.Attributes.Expired {
				exp = "expired"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", render.OrDash(pre.Version), x.Attributes.Version, render.OrDash(pre.Platform),
				x.Attributes.ProcessingState, render.OrDash(bd.InternalBuildState), render.OrDash(bd.ExternalBuildState), shortDate(x.Attributes.UploadedDate), exp, x.ID)
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_beta_groups",
		Title:       "List TestFlight Groups",
		Description: "TestFlight beta groups of an app: name, internal/external, public link, access to all builds, group id.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		App string `json:"app,omitempty" jsonschema:"App: Apple ID, bundle id or app name. Omit to reuse the last used app."`
	}) (*mcp.CallToolResult, any, error) {
		c, app, err := withApp(ctx, args.App)
		if err != nil {
			return errResult(err)
		}
		groups, _, err := list[betaGroupAttrs](ctx, c, "/v1/apps/"+app.ID+"/betaGroups", nil, 0)
		if err != nil {
			return errResult(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "TestFlight groups of %s — %d:\n\n| Name | Type | Public link | All builds | Feedback | Group id |\n| --- | --- | --- | --- | --- | --- |\n", app, len(groups))
		for _, g := range groups {
			typ := "external"
			if g.Attributes.IsInternalGroup {
				typ = "internal"
			}
			link := "—"
			if g.Attributes.PublicLinkEnabled {
				link = g.Attributes.PublicLink
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %v | %v | %s |\n", render.MDCell(g.Attributes.Name), typ, link, g.Attributes.AllBuilds, g.Attributes.FeedbackEnabled, g.ID)
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_beta_testers",
		Title:       "List TestFlight Testers",
		Description: "TestFlight testers of one beta group, or of the whole app: email, name, invite type, state, tester id.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		App   string `json:"app,omitempty" jsonschema:"App: Apple ID, bundle id or app name. Omit to reuse the last used app."`
		Group string `json:"group,omitempty" jsonschema:"Beta group name or id. Omit for every tester of the app."`
		Email string `json:"email,omitempty" jsonschema:"Only this email."`
		Limit int    `json:"limit,omitempty" jsonschema:"Max testers (default 200)."`
	}) (*mcp.CallToolResult, any, error) {
		c, app, err := withApp(ctx, args.App)
		if err != nil {
			return errResult(err)
		}
		path, q, scope := "/v1/betaTesters", url.Values{"filter[apps]": {app.ID}}, app.String()
		if args.Group != "" {
			groups, err := c.resolveGroups(ctx, app.ID, []string{args.Group})
			if err != nil {
				return errResult(err)
			}
			path, q, scope = "/v1/betaGroups/"+groups[0].ID+"/betaTesters", nil, "group "+groups[0].Attributes.Name
		} else if args.Email != "" {
			q.Set("filter[email]", args.Email)
		}
		testers, _, err := list[betaTesterAttrs](ctx, c, path, q, cmpOr(args.Limit, 200))
		if err != nil {
			return errResult(err)
		}
		if args.Email != "" {
			testers = slices.DeleteFunc(testers, func(t resource[betaTesterAttrs]) bool { return !strings.EqualFold(t.Attributes.Email, args.Email) })
		}
		var b strings.Builder
		fmt.Fprintf(&b, "TestFlight testers of %s — %d:\n\n| Email | Name | Invite | State | Tester id |\n| --- | --- | --- | --- | --- |\n", scope, len(testers))
		for _, t := range testers {
			name := strings.TrimSpace(t.Attributes.FirstName + " " + t.Attributes.LastName)
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", render.OrDash(t.Attributes.Email), render.MDCell(render.OrDash(name)), t.Attributes.InviteType, t.Attributes.State, t.ID)
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_beta_tester_add",
		Title:       "Invite TestFlight Tester",
		Description: "Invite a tester by email into one or more beta groups of an app. An existing tester is added to the groups.",
		Annotations: idempotent,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		App       string   `json:"app,omitempty" jsonschema:"App: Apple ID, bundle id or app name. Omit to reuse the last used app."`
		Email     string   `json:"email" jsonschema:"Tester email."`
		FirstName string   `json:"firstName,omitempty"`
		LastName  string   `json:"lastName,omitempty"`
		Groups    []string `json:"groups" jsonschema:"Beta group names or ids (asc_beta_groups)."`
	}) (*mcp.CallToolResult, any, error) {
		if !strings.Contains(args.Email, "@") {
			return errResult(errors.New("email is required"))
		}
		if len(args.Groups) == 0 {
			return errResult(errors.New("groups is required: a tester must join at least one beta group"))
		}
		c, app, err := withApp(ctx, args.App)
		if err != nil {
			return errResult(err)
		}
		groups, err := c.resolveGroups(ctx, app.ID, args.Groups)
		if err != nil {
			return errResult(err)
		}
		var ids, names []string
		for _, g := range groups {
			ids = append(ids, g.ID)
			names = append(names, g.Attributes.Name)
		}
		attrs := map[string]any{"email": args.Email}
		if args.FirstName != "" {
			attrs["firstName"] = args.FirstName
		}
		if args.LastName != "" {
			attrs["lastName"] = args.LastName
		}
		body := payload("betaTesters", "", attrs, nil)
		body["data"].(map[string]any)["relationships"] = map[string]any{"betaGroups": linkage("betaGroups", ids)}
		t, err := send[betaTesterAttrs](ctx, c, http.MethodPost, "/v1/betaTesters", body)
		if is409(err) {
			// Already a tester somewhere: link the existing one to the groups.
			found, _, ferr := list[betaTesterAttrs](ctx, c, "/v1/betaTesters", url.Values{"filter[email]": {args.Email}}, 1)
			if ferr != nil || len(found) == 0 {
				return errResult(err)
			}
			t = found[0]
			for _, id := range ids {
				if _, _, err := c.Do(ctx, http.MethodPost, c.url("/v1/betaGroups/"+id+"/relationships/betaTesters", nil), linkage("betaTesters", []string{t.ID})); err != nil {
					return errResult(err)
				}
			}
			err = nil
		}
		if err != nil {
			return errResult(err)
		}
		return textResult(fmt.Sprintf("Added %s (tester id %s) to %s of %s. TestFlight emails the invitation.", args.Email, t.ID, strings.Join(names, ", "), app))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_beta_tester_remove",
		Title:       "Remove TestFlight Tester",
		Description: "Remove a tester from beta groups, or with deleteTester:true delete the tester entirely (removes them from every app and group of the team).",
		Annotations: dangerous,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		App          string   `json:"app,omitempty" jsonschema:"App: Apple ID, bundle id or app name. Omit to reuse the last used app."`
		Email        string   `json:"email,omitempty" jsonschema:"Tester email (or testerId)."`
		TesterID     string   `json:"testerId,omitempty"`
		Groups       []string `json:"groups,omitempty" jsonschema:"Beta group names or ids to remove the tester from."`
		DeleteTester bool     `json:"deleteTester,omitempty" jsonschema:"Delete the tester from the whole team instead."`
	}) (*mcp.CallToolResult, any, error) {
		if len(args.Groups) == 0 && !args.DeleteTester {
			return errResult(errors.New("pass groups, or deleteTester:true to delete the tester entirely"))
		}
		c, app, err := withApp(ctx, args.App)
		if err != nil {
			return errResult(err)
		}
		id, who := args.TesterID, args.TesterID
		if id == "" {
			if args.Email == "" {
				return errResult(errors.New("email or testerId is required"))
			}
			found, _, err := list[betaTesterAttrs](ctx, c, "/v1/betaTesters", url.Values{"filter[email]": {args.Email}}, 1)
			if err != nil {
				return errResult(err)
			}
			if len(found) == 0 {
				return errResult(fmt.Errorf("no tester with email %s", args.Email))
			}
			id, who = found[0].ID, args.Email
		}
		if args.DeleteTester {
			if _, _, err := c.Do(ctx, http.MethodDelete, c.url("/v1/betaTesters/"+id, nil), nil); err != nil {
				return errResult(err)
			}
			return textResult(fmt.Sprintf("Deleted tester %s from the team (all apps and groups).", who))
		}
		groups, err := c.resolveGroups(ctx, app.ID, args.Groups)
		if err != nil {
			return errResult(err)
		}
		var names []string
		for _, g := range groups {
			if _, _, err := c.Do(ctx, http.MethodDelete, c.url("/v1/betaGroups/"+g.ID+"/relationships/betaTesters", nil), linkage("betaTesters", []string{id})); err != nil {
				return errResult(err)
			}
			names = append(names, g.Attributes.Name)
		}
		return textResult(fmt.Sprintf("Removed %s from %s.", who, strings.Join(names, ", ")))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_build_distribute",
		Title:       "Distribute Build on TestFlight",
		Description: "Distribute a build on TestFlight: optionally set export compliance and \"What to Test\", add it to beta groups, and submit it for beta app review (needed once per version for external groups; default on when an external group is given).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		App string `json:"app,omitempty" jsonschema:"App: Apple ID, bundle id or app name. Omit to reuse the last used app."`
		buildArgs
		Version             string   `json:"version,omitempty" jsonschema:"Marketing version, to disambiguate buildNumber."`
		Platform            string   `json:"platform,omitempty" jsonschema:"Platform, to disambiguate buildNumber."`
		Groups              []string `json:"groups,omitempty" jsonschema:"Beta group names or ids to add the build to."`
		WhatToTest          string   `json:"whatToTest,omitempty" jsonschema:"TestFlight \"What to Test\" text (≤4000 chars)."`
		Locale              string   `json:"locale,omitempty" jsonschema:"Locale of whatToTest (default en-US)."`
		UsesEncryption      *bool    `json:"usesNonExemptEncryption,omitempty" jsonschema:"Export compliance answer, required once per build unless set in Info.plist (ITSAppUsesNonExemptEncryption)."`
		SubmitForBetaReview *bool    `json:"submitForBetaReview,omitempty" jsonschema:"Submit for beta app review. Default: true when any group is external."`
	}) (*mcp.CallToolResult, any, error) {
		if n := utf8.RuneCountInString(args.WhatToTest); n > 4000 {
			return errResult(fmt.Errorf("whatToTest is %d chars; the limit is 4000", n))
		}
		var platform string
		if args.Platform != "" {
			var err error
			if platform, err = normPlatform(args.Platform); err != nil {
				return errResult(err)
			}
		}
		c, app, err := withApp(ctx, args.App)
		if err != nil {
			return errResult(err)
		}
		build, in, err := c.resolveBuild(ctx, app.ID, args.BuildID, args.BuildNumber, args.Version, platform)
		if err != nil {
			return errResult(err)
		}
		label := buildLabel(build, in)
		if st := build.Attributes.ProcessingState; st != "VALID" {
			return errResult(fmt.Errorf("build %s is %s, not VALID: wait for processing to finish", label, st))
		}
		groups, err := c.resolveGroups(ctx, app.ID, args.Groups)
		if err != nil {
			return errResult(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "TestFlight distribution of %s %s (build id %s):\n", app.Name, label, build.ID)
		if args.UsesEncryption != nil {
			if _, err := send[buildAttrs](ctx, c, http.MethodPatch, "/v1/builds/"+build.ID, payload("builds", build.ID, map[string]any{"usesNonExemptEncryption": *args.UsesEncryption}, nil)); err != nil {
				return errResult(fmt.Errorf("setting export compliance: %w", err))
			}
			fmt.Fprintf(&b, "- export compliance: usesNonExemptEncryption = %v\n", *args.UsesEncryption)
		} else if build.Attributes.UsesEncryption == nil {
			b.WriteString("- ⚠ export compliance not answered: pass usesNonExemptEncryption (false for most apps) or testers cannot install it\n")
		}
		if args.WhatToTest != "" {
			locale := cmpOr(args.Locale, "en-US")
			if err := c.setWhatToTest(ctx, build.ID, locale, args.WhatToTest); err != nil {
				return errResult(fmt.Errorf("setting What to Test: %w", err))
			}
			fmt.Fprintf(&b, "- What to Test (%s) set\n", locale)
		}
		external := false
		for _, g := range groups {
			if _, _, err := c.Do(ctx, http.MethodPost, c.url("/v1/betaGroups/"+g.ID+"/relationships/builds", nil), linkage("builds", []string{build.ID})); err != nil {
				return errResult(fmt.Errorf("adding to group %s: %w", g.Attributes.Name, err))
			}
			kind := "internal"
			if !g.Attributes.IsInternalGroup {
				kind, external = "external", true
			}
			fmt.Fprintf(&b, "- added to %s (%s)\n", g.Attributes.Name, kind)
		}
		submit := external
		if args.SubmitForBetaReview != nil {
			submit = *args.SubmitForBetaReview
		}
		if submit {
			_, err := send[json.RawMessage](ctx, c, http.MethodPost, "/v1/betaAppReviewSubmissions", payload("betaAppReviewSubmissions", "", nil, map[string]ref{"build": {Type: "builds", ID: build.ID}}))
			switch {
			case is409(err):
				fmt.Fprintf(&b, "- beta review: not submitted (%v)\n", err)
			case err != nil:
				return errResult(fmt.Errorf("submitting for beta review: %w", err))
			default:
				b.WriteString("- submitted for beta app review; external testers get it once approved\n")
			}
		}
		return textResult(b.String())
	})

	// ---- App Store versions ----

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_versions",
		Title:       "List App Store Versions",
		Description: "App Store versions of an app: platform, version, state (PREPARE_FOR_SUBMISSION, WAITING_FOR_REVIEW, PENDING_DEVELOPER_RELEASE, READY_FOR_DISTRIBUTION, ...), release type, attached build, version id.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		App      string `json:"app,omitempty" jsonschema:"App: Apple ID, bundle id or app name. Omit to reuse the last used app."`
		Platform string `json:"platform,omitempty" jsonschema:"Only this platform: IOS, MAC_OS, TV_OS, VISION_OS."`
		Limit    int    `json:"limit,omitempty" jsonschema:"Max versions (default 10)."`
	}) (*mcp.CallToolResult, any, error) {
		c, app, err := withApp(ctx, args.App)
		if err != nil {
			return errResult(err)
		}
		q := url.Values{"include": {"build"}}
		if args.Platform != "" {
			p, err := normPlatform(args.Platform)
			if err != nil {
				return errResult(err)
			}
			q.Set("filter[platform]", p)
		}
		versions, in, err := list[versionAttrs](ctx, c, "/v1/apps/"+app.ID+"/appStoreVersions", q, cmpOr(args.Limit, 10))
		if err != nil {
			return errResult(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "App Store versions of %s — %d:\n\n| Platform | Version | State | Release | Build | Created | Version id |\n| --- | --- | --- | --- | --- | --- | --- |\n", app, len(versions))
		for _, v := range versions {
			var build buildAttrs
			buildNo := "—"
			if in.get("builds", v.rel("build"), &build) {
				buildNo = build.Version
			}
			release := render.OrDash(v.Attributes.ReleaseType)
			if v.Attributes.ReleaseType == "SCHEDULED" {
				release += " " + shortDate(v.Attributes.EarliestReleaseDate)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s |\n", v.Attributes.Platform, v.Attributes.VersionString, v.Attributes.state(), release, buildNo, shortDate(v.Attributes.CreatedDate), v.ID)
		}
		return textResult(b.String())
	})

	type versionFields struct {
		ReleaseType         string `json:"releaseType,omitempty" jsonschema:"MANUAL (release with asc_version_release), AFTER_APPROVAL, or SCHEDULED (needs earliestReleaseDate)."`
		EarliestReleaseDate string `json:"earliestReleaseDate,omitempty" jsonschema:"RFC 3339 time for SCHEDULED, e.g. 2026-11-05T09:00:00-08:00."`
		Copyright           string `json:"copyright,omitempty" jsonschema:"e.g. 2026 Example Inc."`
	}
	fieldAttrs := func(f versionFields) (map[string]any, error) {
		attrs := map[string]any{}
		if f.ReleaseType != "" {
			rt := strings.ToUpper(f.ReleaseType)
			if !slices.Contains([]string{"MANUAL", "AFTER_APPROVAL", "SCHEDULED"}, rt) {
				return nil, fmt.Errorf("releaseType %q: want MANUAL, AFTER_APPROVAL or SCHEDULED", f.ReleaseType)
			}
			if rt == "SCHEDULED" && f.EarliestReleaseDate == "" {
				return nil, errors.New("releaseType SCHEDULED needs earliestReleaseDate")
			}
			attrs["releaseType"] = rt
		}
		if f.EarliestReleaseDate != "" {
			t, err := time.Parse(time.RFC3339, f.EarliestReleaseDate)
			if err != nil {
				return nil, fmt.Errorf("earliestReleaseDate: want RFC 3339 (2026-11-05T09:00:00-08:00): %w", err)
			}
			// The API takes whole hours.
			attrs["earliestReleaseDate"] = t.Truncate(time.Hour).Format(time.RFC3339)
		}
		if f.Copyright != "" {
			attrs["copyright"] = f.Copyright
		}
		return attrs, nil
	}
	attachBuild := func(ctx context.Context, c *ascClient, appID, platform, versionID, versionString string, ba buildArgs) (string, error) {
		build, in, err := c.resolveBuild(ctx, appID, ba.BuildID, ba.BuildNumber, versionString, platform)
		if err != nil {
			return "", err
		}
		_, _, err = c.Do(ctx, http.MethodPatch, c.url("/v1/appStoreVersions/"+versionID+"/relationships/build", nil), map[string]any{"data": ref{Type: "builds", ID: build.ID}})
		if err != nil {
			return "", fmt.Errorf("attaching build: %w", err)
		}
		return buildLabel(build, in), nil
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_version_create",
		Title:       "Create App Store Version",
		Description: "Create a new App Store version (e.g. 1.5.0) for a platform, optionally with release type, copyright and a build to attach. Only one version per platform can be in preparation.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		App           string `json:"app,omitempty" jsonschema:"App: Apple ID, bundle id or app name. Omit to reuse the last used app."`
		VersionString string `json:"versionString" jsonschema:"New version, e.g. 1.5.0."`
		Platform      string `json:"platform,omitempty" jsonschema:"IOS (default), MAC_OS, TV_OS or VISION_OS."`
		versionFields
		buildArgs
	}) (*mcp.CallToolResult, any, error) {
		if args.VersionString == "" {
			return errResult(errors.New("versionString is required"))
		}
		platform, err := normPlatform(args.Platform)
		if err != nil {
			return errResult(err)
		}
		attrs, err := fieldAttrs(args.versionFields)
		if err != nil {
			return errResult(err)
		}
		c, app, err := withApp(ctx, args.App)
		if err != nil {
			return errResult(err)
		}
		attrs["platform"] = platform
		attrs["versionString"] = args.VersionString
		v, err := send[versionAttrs](ctx, c, http.MethodPost, "/v1/appStoreVersions", payload("appStoreVersions", "", attrs, map[string]ref{"app": {Type: "apps", ID: app.ID}}))
		if err != nil {
			return errResult(err)
		}
		msg := fmt.Sprintf("Created %s for %s.", versionLabel(v), app)
		if args.buildArgs.set() {
			label, err := attachBuild(ctx, c, app.ID, platform, v.ID, args.VersionString, args.buildArgs)
			if err != nil {
				return errResult(fmt.Errorf("%s But: %w", msg, err))
			}
			msg += " Attached build " + label + "."
		}
		return textResult(msg + "\nNext: asc_localization_update (whatsNew), then asc_review_submit.")
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_version_update",
		Title:       "Update App Store Version",
		Description: "Change an App Store version: version string, release type / scheduled date, copyright, and/or the attached build. Only given fields change.",
		Annotations: idempotent,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		versionArgs
		NewVersionString string `json:"newVersionString,omitempty" jsonschema:"Rename the version, e.g. 1.5.1."`
		versionFields
		buildArgs
	}) (*mcp.CallToolResult, any, error) {
		attrs, err := fieldAttrs(args.versionFields)
		if err != nil {
			return errResult(err)
		}
		if args.NewVersionString != "" {
			attrs["versionString"] = args.NewVersionString
		}
		if len(attrs) == 0 && !args.buildArgs.set() {
			return errResult(errors.New("nothing to update: pass newVersionString, releaseType, earliestReleaseDate, copyright, buildId or buildNumber"))
		}
		c, app, v, _, err := args.versionArgs.resolve(ctx, false)
		if err != nil {
			return errResult(err)
		}
		var done []string
		if len(attrs) > 0 {
			if v, err = send[versionAttrs](ctx, c, http.MethodPatch, "/v1/appStoreVersions/"+v.ID, payload("appStoreVersions", v.ID, attrs, nil)); err != nil {
				return errResult(err)
			}
			for k := range attrs {
				done = append(done, k)
			}
			slices.Sort(done)
		}
		if args.buildArgs.set() {
			label, err := attachBuild(ctx, c, app.ID, v.Attributes.Platform, v.ID, v.Attributes.VersionString, args.buildArgs)
			if err != nil {
				return errResult(err)
			}
			done = append(done, "build → "+label)
		}
		return textResult(fmt.Sprintf("Updated %s of %s: %s.", versionLabel(v), app.Name, strings.Join(done, ", ")))
	})

	// ---- metadata ----

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_localizations",
		Title:       "Get Store Metadata",
		Description: "Localized App Store metadata per locale: name and subtitle (app info), and for the version: promotional text, what's new, keywords, description, support/marketing URLs. Defaults to the editable version.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		versionArgs
		Locale string `json:"locale,omitempty" jsonschema:"Only this locale (full text shown), e.g. en-US, vi."`
	}) (*mcp.CallToolResult, any, error) {
		c, app, v, _, err := args.versionArgs.resolve(ctx, true)
		if err != nil {
			return errResult(err)
		}
		vlocs, _, err := list[versionLocAttrs](ctx, c, "/v1/appStoreVersions/"+v.ID+"/appStoreVersionLocalizations", nil, 0)
		if err != nil {
			return errResult(err)
		}
		info, err := c.editableAppInfo(ctx, app.ID)
		if err != nil {
			return errResult(err)
		}
		ilocs, _, err := list[appInfoLocAttrs](ctx, c, "/v1/appInfos/"+info.ID+"/appInfoLocalizations", nil, 0)
		if err != nil {
			return errResult(err)
		}
		infoBy := map[string]appInfoLocAttrs{}
		for _, l := range ilocs {
			infoBy[l.Attributes.Locale] = l.Attributes
		}
		limit := 300
		if args.Locale != "" {
			limit = 100000
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Store metadata of %s, %s:\n", app.Name, versionLabel(v))
		shown := 0
		for _, l := range vlocs {
			a := l.Attributes
			if args.Locale != "" && !strings.EqualFold(a.Locale, args.Locale) {
				continue
			}
			shown++
			i := infoBy[a.Locale]
			fmt.Fprintf(&b, "\n## %s\n", a.Locale)
			fmt.Fprintf(&b, "- name: %s\n- subtitle: %s\n", render.OrDash(i.Name), render.OrDash(i.Subtitle))
			fmt.Fprintf(&b, "- promotional text: %s\n- keywords: %s\n", render.OrDash(a.PromotionalText), render.OrDash(a.Keywords))
			fmt.Fprintf(&b, "- support URL: %s\n- marketing URL: %s\n", render.OrDash(a.SupportURL), render.OrDash(a.MarketingURL))
			fmt.Fprintf(&b, "- what's new:\n%s\n- description:\n%s\n", render.OrDash(truncate(a.WhatsNew, limit)), render.OrDash(truncate(a.Description, limit)))
		}
		if shown == 0 {
			b.WriteString("\nNo version localization found. asc_localization_update creates one.\n")
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_localization_update",
		Title:       "Update Store Metadata",
		Description: "Set localized metadata for one locale; creates the locale when missing. App info: name (≤30), subtitle (≤30), privacyPolicyUrl. Version (editable by default): whatsNew (≤4000), description (≤4000), keywords (≤100, comma-separated), promotionalText (≤170, editable even on a live version), supportUrl, marketingUrl. Only given fields change.",
		Annotations: idempotent,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		versionArgs
		Locale           string `json:"locale" jsonschema:"Locale, e.g. en-US, vi, ja."`
		Name             string `json:"name,omitempty"`
		Subtitle         string `json:"subtitle,omitempty"`
		PrivacyPolicyURL string `json:"privacyPolicyUrl,omitempty"`
		WhatsNew         string `json:"whatsNew,omitempty"`
		Description      string `json:"description,omitempty"`
		Keywords         string `json:"keywords,omitempty"`
		PromotionalText  string `json:"promotionalText,omitempty"`
		SupportURL       string `json:"supportUrl,omitempty"`
		MarketingURL     string `json:"marketingUrl,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		if args.Locale == "" {
			return errResult(errors.New("locale is required, e.g. en-US"))
		}
		limits := []struct {
			name, val string
			max       int
		}{{"name", args.Name, 30}, {"subtitle", args.Subtitle, 30}, {"whatsNew", args.WhatsNew, 4000}, {"description", args.Description, 4000}, {"keywords", args.Keywords, 100}, {"promotionalText", args.PromotionalText, 170}}
		for _, l := range limits {
			if n := utf8.RuneCountInString(l.val); n > l.max {
				return errResult(fmt.Errorf("%s is %d chars; the limit is %d", l.name, n, l.max))
			}
		}
		infoAttrs := nonEmpty(map[string]string{"name": args.Name, "subtitle": args.Subtitle, "privacyPolicyUrl": args.PrivacyPolicyURL})
		verAttrs := nonEmpty(map[string]string{"whatsNew": args.WhatsNew, "description": args.Description, "keywords": args.Keywords,
			"promotionalText": args.PromotionalText, "supportUrl": args.SupportURL, "marketingUrl": args.MarketingURL})
		if len(infoAttrs)+len(verAttrs) == 0 {
			return errResult(errors.New("nothing to update: pass name, subtitle, privacyPolicyUrl, whatsNew, description, keywords, promotionalText, supportUrl or marketingUrl"))
		}
		var b strings.Builder
		var c *ascClient
		var app rememberedApp
		// Promotional text can change on a live version too.
		_, hasPromo := verAttrs["promotionalText"]
		onlyPromo := hasPromo && len(verAttrs) == 1
		if len(verAttrs) > 0 {
			var v resource[versionAttrs]
			var err error
			c, app, v, _, err = args.versionArgs.resolve(ctx, onlyPromo)
			if err != nil {
				return errResult(err)
			}
			locs, _, err := list[versionLocAttrs](ctx, c, "/v1/appStoreVersions/"+v.ID+"/appStoreVersionLocalizations", nil, 0)
			if err != nil {
				return errResult(err)
			}
			if i := slices.IndexFunc(locs, func(l resource[versionLocAttrs]) bool { return strings.EqualFold(l.Attributes.Locale, args.Locale) }); i >= 0 {
				_, err = send[versionLocAttrs](ctx, c, http.MethodPatch, "/v1/appStoreVersionLocalizations/"+locs[i].ID, payload("appStoreVersionLocalizations", locs[i].ID, verAttrs, nil))
			} else {
				verAttrs["locale"] = args.Locale
				_, err = send[versionLocAttrs](ctx, c, http.MethodPost, "/v1/appStoreVersionLocalizations", payload("appStoreVersionLocalizations", "", verAttrs, map[string]ref{"appStoreVersion": {Type: "appStoreVersions", ID: v.ID}}))
				delete(verAttrs, "locale")
			}
			if err != nil {
				return errResult(err)
			}
			fmt.Fprintf(&b, "Version %s (%s): set %s.\n", v.Attributes.VersionString, args.Locale, strings.Join(sortedKeys(verAttrs), ", "))
		}
		if len(infoAttrs) > 0 {
			if c == nil {
				var err error
				if c, app, err = withApp(ctx, args.App); err != nil {
					return errResult(err)
				}
			}
			info, err := c.editableAppInfo(ctx, app.ID)
			if err != nil {
				return errResult(err)
			}
			locs, _, err := list[appInfoLocAttrs](ctx, c, "/v1/appInfos/"+info.ID+"/appInfoLocalizations", nil, 0)
			if err != nil {
				return errResult(err)
			}
			if i := slices.IndexFunc(locs, func(l resource[appInfoLocAttrs]) bool { return strings.EqualFold(l.Attributes.Locale, args.Locale) }); i >= 0 {
				_, err = send[appInfoLocAttrs](ctx, c, http.MethodPatch, "/v1/appInfoLocalizations/"+locs[i].ID, payload("appInfoLocalizations", locs[i].ID, infoAttrs, nil))
			} else {
				infoAttrs["locale"] = args.Locale
				_, err = send[appInfoLocAttrs](ctx, c, http.MethodPost, "/v1/appInfoLocalizations", payload("appInfoLocalizations", "", infoAttrs, map[string]ref{"appInfo": {Type: "appInfos", ID: info.ID}}))
				delete(infoAttrs, "locale")
			}
			if err != nil {
				return errResult(err)
			}
			fmt.Fprintf(&b, "App info (%s): set %s.\n", args.Locale, strings.Join(sortedKeys(infoAttrs), ", "))
		}
		return textResult(fmt.Sprintf("Updated metadata of %s.\n%s", app.Name, b.String()))
	})

	// ---- review & release ----

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_review_submissions",
		Title:       "List Review Submissions",
		Description: "App Review submissions of an app: state (READY_FOR_REVIEW, WAITING_FOR_REVIEW, IN_REVIEW, UNRESOLVED_ISSUES, COMPLETE, ...), platform, submitted date, version, submission id.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		App      string `json:"app,omitempty" jsonschema:"App: Apple ID, bundle id or app name. Omit to reuse the last used app."`
		Platform string `json:"platform,omitempty" jsonschema:"Only this platform."`
		State    string `json:"state,omitempty" jsonschema:"Only these states, comma-separated, e.g. WAITING_FOR_REVIEW,IN_REVIEW."`
		Limit    int    `json:"limit,omitempty" jsonschema:"Max submissions (default 10)."`
	}) (*mcp.CallToolResult, any, error) {
		c, app, err := withApp(ctx, args.App)
		if err != nil {
			return errResult(err)
		}
		q := url.Values{"filter[app]": {app.ID}, "include": {"appStoreVersionForReview"}}
		if args.Platform != "" {
			p, err := normPlatform(args.Platform)
			if err != nil {
				return errResult(err)
			}
			q.Set("filter[platform]", p)
		}
		if args.State != "" {
			q.Set("filter[state]", strings.ToUpper(strings.ReplaceAll(args.State, " ", "")))
		}
		subs, in, err := list[reviewSubmissionAttrs](ctx, c, "/v1/reviewSubmissions", q, cmpOr(args.Limit, 10))
		if err != nil {
			return errResult(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Review submissions of %s — %d:\n\n| Platform | State | Submitted | Version | Submission id |\n| --- | --- | --- | --- | --- |\n", app, len(subs))
		for _, x := range subs {
			var v versionAttrs
			ver := "—"
			if in.get("appStoreVersions", x.rel("appStoreVersionForReview"), &v) {
				ver = v.VersionString
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", x.Attributes.Platform, x.Attributes.State, shortDate(x.Attributes.SubmittedDate), ver, x.ID)
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_review_submit",
		Title:       "Submit for App Review",
		Description: "Submit an App Store version (default: the editable one) to App Review. The version needs a build and complete metadata; App Store Connect reports what is missing. Confirm with the user first.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args versionArgs) (*mcp.CallToolResult, any, error) {
		c, app, v, in, err := args.resolve(ctx, false)
		if err != nil {
			return errResult(err)
		}
		var build buildAttrs
		if !in.get("builds", v.rel("build"), &build) {
			return errResult(fmt.Errorf("%s has no build: attach one with asc_version_update {buildNumber}", versionLabel(v)))
		}
		sub, err := c.submitForReview(ctx, app.ID, v)
		if err != nil {
			return errResult(err)
		}
		return textResult(fmt.Sprintf("Submitted %s %s (build %s) for App Review: submission %s is %s.\nTrack it with asc_review_submissions; cancel with asc_review_cancel.",
			app.Name, v.Attributes.VersionString, build.Version, sub.ID, render.OrDash(sub.Attributes.State)))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_review_cancel",
		Title:       "Cancel App Review Submission",
		Description: "Withdraw a review submission (default: the app's submission waiting for or in review) so the version can be edited again.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		App          string `json:"app,omitempty" jsonschema:"App: Apple ID, bundle id or app name. Omit to reuse the last used app."`
		SubmissionID string `json:"submissionId,omitempty" jsonschema:"Review submission id (asc_review_submissions)."`
		Platform     string `json:"platform,omitempty" jsonschema:"IOS (default), MAC_OS, TV_OS or VISION_OS."`
	}) (*mcp.CallToolResult, any, error) {
		platform, err := normPlatform(args.Platform)
		if err != nil {
			return errResult(err)
		}
		c, app, err := withApp(ctx, args.App)
		if err != nil {
			return errResult(err)
		}
		id := args.SubmissionID
		if id == "" {
			subs, _, err := list[reviewSubmissionAttrs](ctx, c, "/v1/reviewSubmissions", url.Values{
				"filter[app]": {app.ID}, "filter[platform]": {platform},
				"filter[state]": {"WAITING_FOR_REVIEW,IN_REVIEW,UNRESOLVED_ISSUES,READY_FOR_REVIEW"},
			}, 1)
			if err != nil {
				return errResult(err)
			}
			if len(subs) == 0 {
				return errResult(fmt.Errorf("no open %s review submission for %s", platform, app))
			}
			id = subs[0].ID
		}
		sub, err := send[reviewSubmissionAttrs](ctx, c, http.MethodPatch, "/v1/reviewSubmissions/"+id, payload("reviewSubmissions", id, map[string]any{"canceled": true}, nil))
		if err != nil {
			return errResult(err)
		}
		return textResult(fmt.Sprintf("Canceled review submission %s of %s (now %s).", id, app.Name, render.OrDash(sub.Attributes.State)))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_version_release",
		Title:       "Release Approved Version",
		Description: "Release an approved version that waits for manual release (PENDING_DEVELOPER_RELEASE) to the App Store. With a phased release it starts day 1. Confirm with the user first.",
		Annotations: dangerous,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args versionArgs) (*mcp.CallToolResult, any, error) {
		platform, err := normPlatform(args.Platform)
		if err != nil {
			return errResult(err)
		}
		c, app, err := withApp(ctx, args.App)
		if err != nil {
			return errResult(err)
		}
		var v resource[versionAttrs]
		if args.VersionID != "" || args.VersionString != "" {
			if v, _, err = c.resolveVersion(ctx, app.ID, args.VersionID, args.VersionString, platform, false); err != nil {
				return errResult(err)
			}
		} else {
			vs, _, err := list[versionAttrs](ctx, c, "/v1/apps/"+app.ID+"/appStoreVersions", url.Values{"filter[platform]": {platform}, "filter[appVersionState]": {"PENDING_DEVELOPER_RELEASE"}}, 1)
			if err != nil {
				return errResult(err)
			}
			if len(vs) == 0 {
				return errResult(fmt.Errorf("no %s version of %s is PENDING_DEVELOPER_RELEASE (asc_versions shows states)", platform, app))
			}
			v = vs[0]
		}
		if st := v.Attributes.state(); st != "PENDING_DEVELOPER_RELEASE" {
			return errResult(fmt.Errorf("%s is %s; only PENDING_DEVELOPER_RELEASE versions can be released", versionLabel(v), st))
		}
		if _, err := send[json.RawMessage](ctx, c, http.MethodPost, "/v1/appStoreVersionReleaseRequests", payload("appStoreVersionReleaseRequests", "", nil, map[string]ref{"appStoreVersion": {Type: "appStoreVersions", ID: v.ID}})); err != nil {
			return errResult(err)
		}
		return textResult(fmt.Sprintf("Released %s %s %s. It appears on the App Store within hours; asc_phased_release shows rollout progress if phased.", app.Name, v.Attributes.Platform, v.Attributes.VersionString))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_phased_release",
		Title:       "Phased Release",
		Description: "Phased (7-day) release of a version. action: status (default), enable (before release), disable (before release), pause, resume, complete (release to everyone now). Without versionId/versionString: enable/disable use the editable version, the others the newest version.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		versionArgs
		Action string `json:"action,omitempty" jsonschema:"status (default), enable, disable, pause, resume or complete."`
	}) (*mcp.CallToolResult, any, error) {
		action := strings.ToLower(cmpOr(args.Action, "status"))
		if !slices.Contains([]string{"status", "enable", "disable", "pause", "resume", "complete"}, action) {
			return errResult(fmt.Errorf("action %q: want status, enable, disable, pause, resume or complete", args.Action))
		}
		platform, err := normPlatform(args.Platform)
		if err != nil {
			return errResult(err)
		}
		c, app, err := withApp(ctx, args.App)
		if err != nil {
			return errResult(err)
		}
		var v resource[versionAttrs]
		if args.VersionID != "" || args.VersionString != "" || action == "enable" || action == "disable" {
			v, _, err = c.resolveVersion(ctx, app.ID, args.VersionID, args.VersionString, platform, false)
		} else {
			var vs []resource[versionAttrs]
			vs, _, err = list[versionAttrs](ctx, c, "/v1/apps/"+app.ID+"/appStoreVersions", url.Values{"filter[platform]": {platform}}, 10)
			if err == nil && len(vs) == 0 {
				err = fmt.Errorf("%s has no %s versions", app, platform)
			}
			if err == nil {
				v = newestVersion(vs)
			}
		}
		if err != nil {
			return errResult(err)
		}
		pr, _, err := get[phasedReleaseAttrs](ctx, c, "/v1/appStoreVersions/"+v.ID+"/appStoreVersionPhasedRelease", nil)
		if err != nil && !is404(err) {
			return errResult(err)
		}
		exists := err == nil && pr.ID != ""
		describe := func(p resource[phasedReleaseAttrs]) string {
			a := p.Attributes
			s := fmt.Sprintf("phased release of %s %s: %s", app.Name, versionLabel(v), a.State)
			if a.State == "ACTIVE" || a.State == "PAUSED" {
				s += fmt.Sprintf(", day %d of 7, started %s, paused %d day(s) total", a.CurrentDayNumber, shortDate(a.StartDate), a.TotalPauseDays)
			}
			return s
		}
		state := map[string]string{"pause": "PAUSED", "resume": "ACTIVE", "complete": "COMPLETE"}[action]
		switch {
		case action == "status":
			if !exists {
				return textResult(fmt.Sprintf("No phased release for %s: it releases to everyone at once.", versionLabel(v)))
			}
			return textResult("Status: " + describe(pr))
		case action == "enable":
			if exists {
				return textResult("Already on: " + describe(pr))
			}
			pr, err = send[phasedReleaseAttrs](ctx, c, http.MethodPost, "/v1/appStoreVersionPhasedReleases", payload("appStoreVersionPhasedReleases", "",
				map[string]any{"phasedReleaseState": "INACTIVE"}, map[string]ref{"appStoreVersion": {Type: "appStoreVersions", ID: v.ID}}))
			if err != nil {
				return errResult(err)
			}
			return textResult("Enabled " + describe(pr) + " — it starts when the version is released.")
		case !exists:
			return errResult(fmt.Errorf("%s has no phased release", versionLabel(v)))
		case action == "disable":
			if _, _, err := c.Do(ctx, http.MethodDelete, c.url("/v1/appStoreVersionPhasedReleases/"+pr.ID, nil), nil); err != nil {
				return errResult(err)
			}
			return textResult(fmt.Sprintf("Disabled the phased release of %s: it will release to everyone at once.", versionLabel(v)))
		default:
			pr, err = send[phasedReleaseAttrs](ctx, c, http.MethodPatch, "/v1/appStoreVersionPhasedReleases/"+pr.ID, payload("appStoreVersionPhasedReleases", pr.ID, map[string]any{"phasedReleaseState": state}, nil))
			if err != nil {
				return errResult(err)
			}
			return textResult("Updated " + describe(pr))
		}
	})

	// ---- customer reviews ----

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_reviews",
		Title:       "List Customer Reviews",
		Description: "App Store customer reviews: rating, title, text, nickname, territory, date, your published response, review id. Filter by rating, territory, unanswered.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		App        string `json:"app,omitempty" jsonschema:"App: Apple ID, bundle id or app name. Omit to reuse the last used app."`
		Rating     []int  `json:"rating,omitempty" jsonschema:"Only these star ratings, e.g. [1,2]."`
		Territory  string `json:"territory,omitempty" jsonschema:"Only this territory, ISO 3166-1 alpha-3, e.g. USA, VNM."`
		Unanswered bool   `json:"unanswered,omitempty" jsonschema:"Only reviews without a published response."`
		Sort       string `json:"sort,omitempty" jsonschema:"newest (default), oldest, highest, lowest."`
		Limit      int    `json:"limit,omitempty" jsonschema:"Max reviews (default 20, max 500)."`
	}) (*mcp.CallToolResult, any, error) {
		sortParam, ok := map[string]string{"": "-createdDate", "newest": "-createdDate", "oldest": "createdDate", "highest": "-rating", "lowest": "rating"}[strings.ToLower(args.Sort)]
		if !ok {
			return errResult(fmt.Errorf("sort %q: want newest, oldest, highest or lowest", args.Sort))
		}
		c, app, err := withApp(ctx, args.App)
		if err != nil {
			return errResult(err)
		}
		q := url.Values{"sort": {sortParam}, "include": {"response"}}
		if len(args.Rating) > 0 {
			var rs []string
			for _, r := range args.Rating {
				if r < 1 || r > 5 {
					return errResult(fmt.Errorf("rating %d: want 1–5", r))
				}
				rs = append(rs, fmt.Sprint(r))
			}
			q.Set("filter[rating]", strings.Join(rs, ","))
		}
		if args.Territory != "" {
			q.Set("filter[territory]", strings.ToUpper(args.Territory))
		}
		if args.Unanswered {
			q.Set("exists[publishedResponse]", "false")
		}
		reviews, in, err := list[customerReviewAttrs](ctx, c, "/v1/apps/"+app.ID+"/customerReviews", q, min(cmpOr(args.Limit, 20), 500))
		if err != nil {
			return errResult(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Customer reviews of %s — %d shown", app, len(reviews))
		if len(reviews) > 0 {
			sum := 0
			for _, r := range reviews {
				sum += r.Attributes.Rating
			}
			fmt.Fprintf(&b, ", average %.2f★", float64(sum)/float64(len(reviews)))
		}
		b.WriteString(":\n")
		for _, r := range reviews {
			a := r.Attributes
			fmt.Fprintf(&b, "\n%s %s · %s · %s · id %s\n**%s**\n%s\n", strings.Repeat("★", a.Rating)+strings.Repeat("☆", 5-a.Rating),
				shortDate(a.CreatedDate), a.Territory, a.Nickname, r.ID, a.Title, truncate(a.Body, 1000))
			var resp reviewResponseAttrs
			if in.get("customerReviewResponses", r.rel("response"), &resp) {
				fmt.Fprintf(&b, "↳ response (%s, %s): %s\n", resp.State, shortDate(resp.LastModifiedDate), truncate(resp.ResponseBody, 500))
			}
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_review_reply",
		Title:       "Reply to Customer Review",
		Description: "Publish a developer response to a customer review (≤5970 chars). A review has one response: replace:true swaps an existing one. Responses are public after Apple's check.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		ReviewID string `json:"reviewId" jsonschema:"Customer review id (asc_reviews)."`
		Response string `json:"response" jsonschema:"Response text."`
		Replace  bool   `json:"replace,omitempty" jsonschema:"Replace an existing response."`
	}) (*mcp.CallToolResult, any, error) {
		if args.ReviewID == "" || strings.TrimSpace(args.Response) == "" {
			return errResult(errors.New("reviewId and response are required"))
		}
		if n := utf8.RuneCountInString(args.Response); n > 5970 {
			return errResult(fmt.Errorf("response is %d chars; the limit is 5970", n))
		}
		c, err := newASCClient()
		if err != nil {
			return errResult(err)
		}
		old, _, err := get[reviewResponseAttrs](ctx, c, "/v1/customerReviews/"+args.ReviewID+"/response", nil)
		if err != nil && !is404(err) {
			return errResult(err)
		}
		if err == nil && old.ID != "" {
			if !args.Replace {
				return errResult(fmt.Errorf("review %s already has a response (%q): pass replace:true to swap it", args.ReviewID, truncate(old.Attributes.ResponseBody, 120)))
			}
			if _, _, err := c.Do(ctx, http.MethodDelete, c.url("/v1/customerReviewResponses/"+old.ID, nil), nil); err != nil {
				return errResult(fmt.Errorf("deleting the old response: %w", err))
			}
		}
		resp, err := send[reviewResponseAttrs](ctx, c, http.MethodPost, "/v1/customerReviewResponses", payload("customerReviewResponses", "",
			map[string]any{"responseBody": args.Response}, map[string]ref{"review": {Type: "customerReviews", ID: args.ReviewID}}))
		if err != nil {
			return errResult(err)
		}
		return textResult(fmt.Sprintf("Response %s to review %s saved (state %s). It shows on the App Store once Apple publishes it.", resp.ID, args.ReviewID, render.OrDash(resp.Attributes.State)))
	})

	// ---- monetization ----

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_iaps",
		Title:       "List In-App Purchases",
		Description: "In-app purchases of an app (consumable, non-consumable, non-renewing): name, product id, type, state, family sharing, id. Subscriptions: asc_subscriptions.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		App   string `json:"app,omitempty" jsonschema:"App: Apple ID, bundle id or app name. Omit to reuse the last used app."`
		Limit int    `json:"limit,omitempty" jsonschema:"Max items (default 200)."`
	}) (*mcp.CallToolResult, any, error) {
		c, app, err := withApp(ctx, args.App)
		if err != nil {
			return errResult(err)
		}
		iaps, _, err := list[iapAttrs](ctx, c, "/v1/apps/"+app.ID+"/inAppPurchasesV2", url.Values{"sort": {"name"}}, cmpOr(args.Limit, 200))
		if err != nil {
			return errResult(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "In-app purchases of %s — %d:\n\n| Name | Product ID | Type | State | Family sharing | id |\n| --- | --- | --- | --- | --- | --- |\n", app, len(iaps))
		for _, x := range iaps {
			a := x.Attributes
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %v | %s |\n", render.MDCell(a.Name), a.ProductID, a.Type, a.State, a.FamilySharable, x.ID)
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_subscriptions",
		Title:       "List Subscriptions",
		Description: "Auto-renewable subscription groups of an app and their subscriptions: name, product id, period, level, state, id.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		App string `json:"app,omitempty" jsonschema:"App: Apple ID, bundle id or app name. Omit to reuse the last used app."`
	}) (*mcp.CallToolResult, any, error) {
		c, app, err := withApp(ctx, args.App)
		if err != nil {
			return errResult(err)
		}
		groups, in, err := list[subscriptionGroupAttrs](ctx, c, "/v1/apps/"+app.ID+"/subscriptionGroups", url.Values{"include": {"subscriptions"}, "limit[subscriptions]": {"50"}}, 0)
		if err != nil {
			return errResult(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Subscription groups of %s — %d:\n", app, len(groups))
		for _, g := range groups {
			fmt.Fprintf(&b, "\n## %s (group id %s)\n| Name | Product ID | Period | Level | State | id |\n| --- | --- | --- | --- | --- | --- |\n", g.Attributes.ReferenceName, g.ID)
			for _, r := range g.Relationships["subscriptions"].refs() {
				var a subscriptionAttrs
				if in.get("subscriptions", r.ID, &a) {
					fmt.Fprintf(&b, "| %s | %s | %s | %d | %s | %s |\n", render.MDCell(a.Name), a.ProductID, a.Period, a.GroupLevel, a.State, r.ID)
				}
			}
		}
		return textResult(b.String())
	})

	// ---- reports ----

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_sales_report",
		Title:       "Sales and Trends Report",
		Description: "Download a Sales and Trends report (needs the vendor number and a key with the Sales or Finance role). SALES/SUMMARY shows totals of units by type (downloads, updates, IAP), app and country, and proceeds by currency, plus a preview; saveToPath writes the full report as CSV. Daily data is ready the next day (Pacific time).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		VendorNumber  string `json:"vendorNumber,omitempty" jsonschema:"Vendor number (Payments and Financial Reports). Default: from setup / ASC_VENDOR_NUMBER."`
		ReportType    string `json:"reportType,omitempty" jsonschema:"SALES (default), SUBSCRIPTION, SUBSCRIPTION_EVENT, SUBSCRIBER, SUBSCRIPTION_OFFER_CODE_REDEMPTION, PRE_ORDER, NEWSSTAND, INSTALLS, FIRST_ANNUAL, WIN_BACK_ELIGIBILITY."`
		ReportSubType string `json:"reportSubType,omitempty" jsonschema:"SUMMARY (default), DETAILED, SUMMARY_INSTALL_TYPE, SUMMARY_TERRITORY, SUMMARY_CHANNEL."`
		Frequency     string `json:"frequency,omitempty" jsonschema:"DAILY (default), WEEKLY, MONTHLY, YEARLY."`
		ReportDate    string `json:"reportDate,omitempty" jsonschema:"DAILY/WEEKLY YYYY-MM-DD (weekly = the Sunday ending the week), MONTHLY YYYY-MM, YEARLY YYYY. Default: the latest daily report."`
		Version       string `json:"version,omitempty" jsonschema:"Report format version, e.g. 1_1 (SALES) or 1_4 (subscriptions). Omit for the API default; the API error lists valid ones."`
		SaveToPath    string `json:"saveToPath,omitempty" jsonschema:"Write the full report as CSV to this path."`
		PreviewRows   int    `json:"previewRows,omitempty" jsonschema:"Rows in the preview table (default 10)."`
	}) (*mcp.CallToolResult, any, error) {
		c, err := newASCClient()
		if err != nil {
			return errResult(err)
		}
		vendor := firstNonEmpty(args.VendorNumber, c.creds.VendorNumber)
		if vendor == "" {
			return errResult(errors.New("vendorNumber is required: App Store Connect → Payments and Financial Reports shows it (or run setup --vendor-number)"))
		}
		rtype, sub, freq := strings.ToUpper(cmpOr(args.ReportType, "SALES")), strings.ToUpper(cmpOr(args.ReportSubType, "SUMMARY")), strings.ToUpper(cmpOr(args.Frequency, "DAILY"))
		q := url.Values{
			"filter[vendorNumber]":  {vendor},
			"filter[reportType]":    {rtype},
			"filter[reportSubType]": {sub},
			"filter[frequency]":     {freq},
		}
		if args.Version != "" {
			q.Set("filter[version]", args.Version)
		}
		date := args.ReportDate
		var tsv string
		if date == "" && freq == "DAILY" {
			// Yesterday (Pacific) may not be ready yet: fall back one day.
			today := time.Now().In(pacific())
			for back := 1; back <= 2; back++ {
				date = today.AddDate(0, 0, -back).Format(time.DateOnly)
				q.Set("filter[reportDate]", date)
				if tsv, err = c.fetchReport(ctx, "/v1/salesReports", q); !is404(err) {
					break
				}
			}
		} else {
			if date != "" {
				q.Set("filter[reportDate]", date)
			}
			tsv, err = c.fetchReport(ctx, "/v1/salesReports", q)
		}
		if err != nil {
			return errResult(err)
		}
		t := parseTSV(tsv)
		var b strings.Builder
		fmt.Fprintf(&b, "%s %s %s report, vendor %s, %s — %d rows.\n\n", freq, rtype, sub, vendor, render.OrDash(date), len(t.Rows))
		if rtype == "SALES" {
			b.WriteString(renderSalesSummary(t, 10))
			b.WriteString("\n")
		}
		if args.SaveToPath != "" {
			msg, err := render.SaveCSV(args.SaveToPath, t.csv())
			if err != nil {
				return errResult(err)
			}
			b.WriteString(msg)
		} else {
			b.WriteString(renderTablePreview(t, cmpOr(args.PreviewRows, 10)))
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_finance_report",
		Title:       "Financial Report",
		Description: "Download a monthly financial report (Apple fiscal month; needs the vendor number and a Finance-role key): FINANCIAL for one region (ZZ = consolidated) or FINANCE_DETAIL (region Z1). Shows a preview; saveToPath writes the full report as CSV.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		VendorNumber string `json:"vendorNumber,omitempty" jsonschema:"Vendor number. Default: from setup / ASC_VENDOR_NUMBER."`
		ReportType   string `json:"reportType,omitempty" jsonschema:"FINANCIAL (default) or FINANCE_DETAIL."`
		RegionCode   string `json:"regionCode,omitempty" jsonschema:"Region code, e.g. US, EU, JP; ZZ = all regions consolidated (default for FINANCIAL); Z1 for FINANCE_DETAIL."`
		ReportDate   string `json:"reportDate,omitempty" jsonschema:"Fiscal month YYYY-MM. Default: the previous month."`
		SaveToPath   string `json:"saveToPath,omitempty" jsonschema:"Write the full report as CSV to this path."`
		PreviewRows  int    `json:"previewRows,omitempty" jsonschema:"Rows in the preview table (default 20)."`
	}) (*mcp.CallToolResult, any, error) {
		c, err := newASCClient()
		if err != nil {
			return errResult(err)
		}
		vendor := firstNonEmpty(args.VendorNumber, c.creds.VendorNumber)
		if vendor == "" {
			return errResult(errors.New("vendorNumber is required: App Store Connect → Payments and Financial Reports shows it (or run setup --vendor-number)"))
		}
		rtype := strings.ToUpper(cmpOr(args.ReportType, "FINANCIAL"))
		region := strings.ToUpper(args.RegionCode)
		switch {
		case region != "":
		case rtype == "FINANCE_DETAIL":
			region = "Z1"
		default:
			region = "ZZ"
		}
		date := args.ReportDate
		if date == "" {
			date = time.Now().AddDate(0, -1, 0).Format("2006-01")
		}
		tsv, err := c.fetchReport(ctx, "/v1/financeReports", url.Values{
			"filter[vendorNumber]": {vendor}, "filter[reportType]": {rtype},
			"filter[regionCode]": {region}, "filter[reportDate]": {date},
		})
		if err != nil {
			return errResult(err)
		}
		t := parseTSV(tsv)
		var b strings.Builder
		fmt.Fprintf(&b, "%s report, vendor %s, region %s, fiscal month %s — %d rows.\n\n", rtype, vendor, region, date, len(t.Rows))
		if args.SaveToPath != "" {
			msg, err := render.SaveCSV(args.SaveToPath, t.csv())
			if err != nil {
				return errResult(err)
			}
			b.WriteString(msg)
		} else {
			b.WriteString(renderTablePreview(t, cmpOr(args.PreviewRows, 20)))
		}
		return textResult(b.String())
	})

	// ---- raw API ----

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_get",
		Title:       "API GET",
		Description: "GET any App Store Connect API path for things without a dedicated tool, e.g. /v1/apps/{id}/appClips, /v1/users, /v1/bundleIds, /v1/certificates, /v1/profiles, /v1/devices, /v1/apps/{id}/perfPowerMetrics. Returns JSON (data arrays merged across pages up to maxItems).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		Path     string            `json:"path" jsonschema:"API path starting with /v1/ (or /v2/, /v3/), query string allowed."`
		Query    map[string]string `json:"query,omitempty" jsonschema:"Query parameters, e.g. {\"filter[platform]\": \"IOS\", \"include\": \"build\"}."`
		MaxItems int               `json:"maxItems,omitempty" jsonschema:"Follow pagination up to this many items (default: first page only)."`
	}) (*mcp.CallToolResult, any, error) {
		u, err := apiURL(args.Path, args.Query)
		if err != nil {
			return errResult(err)
		}
		c, err := newASCClient()
		if err != nil {
			return errResult(err)
		}
		out, err := c.getRaw(ctx, u, args.MaxItems)
		if err != nil {
			return errResult(err)
		}
		return textResult(out)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "asc_request",
		Title:       "API Write",
		Description: "Send POST, PATCH or DELETE to any App Store Connect API path with a JSON:API body ({\"data\": {\"type\", \"id\", \"attributes\", \"relationships\"}}), for writes without a dedicated tool. Confirm DELETEs with the user.",
		Annotations: dangerous,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		Method string         `json:"method" jsonschema:"POST, PATCH or DELETE."`
		Path   string         `json:"path" jsonschema:"API path starting with /v1/ (or /v2/, /v3/)."`
		Body   map[string]any `json:"body,omitempty" jsonschema:"JSON body."`
	}) (*mcp.CallToolResult, any, error) {
		method := strings.ToUpper(args.Method)
		if !slices.Contains([]string{http.MethodPost, http.MethodPatch, http.MethodDelete}, method) {
			return errResult(fmt.Errorf("method %q: want POST, PATCH or DELETE (reads: asc_get)", args.Method))
		}
		u, err := apiURL(args.Path, nil)
		if err != nil {
			return errResult(err)
		}
		c, err := newASCClient()
		if err != nil {
			return errResult(err)
		}
		var body any
		if args.Body != nil {
			body = args.Body
		}
		data, status, err := c.Do(ctx, method, u, body)
		if err != nil {
			return errResult(err)
		}
		if len(data) == 0 {
			return textResult(fmt.Sprintf("%s %s → %d (no content).", method, args.Path, status))
		}
		return textResult(fmt.Sprintf("%s %s → %d\n%s", method, args.Path, status, prettyJSON(data)))
	})
}

func cmpOr[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

func nonEmpty(m map[string]string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func pacific() *time.Location {
	if loc, err := time.LoadLocation("America/Los_Angeles"); err == nil {
		return loc
	}
	return time.FixedZone("PST", -8*3600)
}
