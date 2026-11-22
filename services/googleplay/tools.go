package googleplay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/mcpkit"
)

type emptyArgs = mcpkit.EmptyArgs

func textResult(s string) (*mcp.CallToolResult, any, error) { return mcpkit.TextResult(s) }

func errResult(err error) (*mcp.CallToolResult, any, error) { return mcpkit.ErrResult(auth, err) }

// mustClient returns an API client or an error result.
func mustClient(ctx context.Context) (*playClient, *mcp.CallToolResult) {
	c, err := newPlayClient(ctx)
	if err != nil {
		res, _, _ := errResult(err)
		return nil, res
	}
	return c, nil
}

// resolvePackageArg resolves packageName (possibly empty → last remembered)
// for tools, returning an error result on failure.
func resolvePackageArg(pkg string) (string, *mcp.CallToolResult) {
	resolved, err := resolvePackage(pkg)
	if err != nil {
		res, _, _ := errResult(err)
		return "", res
	}
	return resolved, nil
}

// truncate shortens s to at most n runes (not bytes, so multi-byte text such
// as Vietnamese or Japanese reviews is never split mid-rune).
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// int64List accepts JSON array items that are either numbers or strings —
// models frequently emit numeric version codes while the API requires strings.
type int64List []string

func (l *int64List) UnmarshalJSON(data []byte) error {
	var raw []any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		switch t := v.(type) {
		case string:
			out = append(out, t)
		case float64:
			out = append(out, strconv.FormatInt(int64(t), 10))
		case json.Number:
			out = append(out, t.String())
		default:
			return fmt.Errorf("version code items must be numbers or strings, got %T", v)
		}
	}
	*l = out
	return nil
}

func registerTools(s *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	// ---- auth ----

	mcpkit.AddLoginTools(s, auth, mcpkit.LoginTools{
		Description: "Authorize access to the Google Play Developer API (Android Publisher): opens the browser at Google's OAuth consent page and stores the refresh token locally. Non-blocking: returns immediately while approval is pending — call play_login_status afterwards. Required once before any other tool. The Google account must be added to the Play Console developer account (Users & permissions). Scope: androidpublisher (read AND write).",
		ReadyNote:   " (needs a packageName)",
		Authorized: func(ctx context.Context) (string, bool) {
			return "A token is already stored. If API calls still fail with authentication errors, call play_logout and play_login again to re-authorize.", true
		},
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_app_details",
		Title:       "Get App Details",
		Description: "Store listing contact details of an app: default language, contact email/phone, website. For localized titles/descriptions use play_listings.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		PackageName string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		var d appDetails
		err := c.withEdit(ctx, pkg, editOptions{}, func(editID string) error {
			var err error
			d, err = c.getAppDetails(ctx, pkg, editID)
			return err
		})
		if err != nil {
			return errResult(err)
		}
		return textResult(renderDetails(pkg, d))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_listings",
		Title:       "List Store Listings",
		Description: "List localized store listings of an app: title, short description and video URL per language. Full descriptions are not included (too long for one response).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		PackageName string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		var listings []listing
		err := c.withEdit(ctx, pkg, editOptions{}, func(editID string) error {
			var err error
			listings, err = c.listListings(ctx, pkg, editID)
			return err
		})
		if err != nil {
			return errResult(err)
		}
		return textResult(renderListings(pkg, listings))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_listing_update",
		Title:       "Update Store Listing",
		Description: "Update the store listing text of one language (title, short description, full description, video URL) and commit. Only provided fields change. Short description ≤ 80 chars, full description ≤ 4000 chars.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args listingUpdateArgs) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		if args.Language == "" {
			return errResult(errors.New("language is required (BCP-47 tag, e.g. en-US, vi)"))
		}
		if args.Title == "" && args.ShortDescription == "" && args.FullDescription == "" && args.Video == "" {
			return errResult(errors.New("nothing to update: provide title, shortDescription, fullDescription or video"))
		}
		if n := len(args.ShortDescription); n > 80 {
			return errResult(fmt.Errorf("shortDescription is %d chars; the limit is 80", n))
		}
		if n := len(args.FullDescription); n > 4000 {
			return errResult(fmt.Errorf("fullDescription is %d chars; the limit is 4000", n))
		}
		body := map[string]string{}
		if args.Title != "" {
			body["title"] = args.Title
		}
		if args.ShortDescription != "" {
			body["shortDescription"] = args.ShortDescription
		}
		if args.FullDescription != "" {
			body["fullDescription"] = args.FullDescription
		}
		if args.Video != "" {
			body["video"] = args.Video
		}
		var out listing
		opts := editOptions{Commit: true, ValidateOnly: args.ValidateOnly}
		err := c.withEdit(ctx, pkg, opts, func(editID string) error {
			var err error
			out, err = c.patchListing(ctx, pkg, editID, args.Language, body)
			return err
		})
		if err != nil {
			return errResult(err)
		}
		return textResult(renderListingUpdate(pkg, args.Language, out, args.ValidateOnly))
	})

	// ---- tracks & releases (edit-based) ----

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_tracks",
		Title:       "List Tracks & Releases",
		Description: "List all release tracks (production, internal, beta, alpha, custom, form-factor like wear:production) with their releases: status, version codes, staged-rollout fraction, release notes. This is what users currently get or will get.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		PackageName string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		var tracks []track
		err := c.withEdit(ctx, pkg, editOptions{}, func(editID string) error {
			var err error
			tracks, err = c.listTracks(ctx, pkg, editID)
			return err
		})
		if err != nil {
			return errResult(err)
		}
		return textResult(renderTracks(pkg, tracks))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_track_update",
		Title:       "Update Track Releases",
		Description: "Set the releases of a track and commit: which version codes are served, release status (draft/inProgress/halted/completed), staged rollout fraction and release notes. versionCodes must list ALL builds to keep in the release (retained ones included) — a bundle/apk uploaded but not listed here stays off-track. Use validateOnly:true to dry-run without committing. userFraction only applies to inProgress releases.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args trackUpdateArgs) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		if args.Track == "" {
			return errResult(errors.New("track is required (production, internal, beta, alpha, a custom track name, or a form-factor track like wear:production)"))
		}
		body, err := buildTrackBody(args)
		if err != nil {
			return errResult(err)
		}
		var out track
		opts := editOptions{Commit: true, ValidateOnly: args.ValidateOnly, ChangesNotSentForReview: args.ChangesNotSentForReview}
		err = c.withEdit(ctx, pkg, opts, func(editID string) error {
			var err error
			out, err = c.updateTrack(ctx, pkg, editID, args.Track, body)
			return err
		})
		if err != nil {
			return errResult(err)
		}
		return textResult(renderTrackUpdate(args.Track, out, args.ValidateOnly))
	})

	// ---- bundles & apks (edit-based) ----

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_bundles",
		Title:       "List Uploaded App Bundles",
		Description: "List uploaded .aab app bundles (version codes + SHA1/SHA256). Newest bundles are at the end; the highest version code is what a track update should reference.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		PackageName string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		var bundles []bundle
		err := c.withEdit(ctx, pkg, editOptions{}, func(editID string) error {
			var err error
			bundles, err = c.listBundles(ctx, pkg, editID)
			return err
		})
		if err != nil {
			return errResult(err)
		}
		return textResult(renderBundles(pkg, bundles))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_bundle_upload",
		Title:       "Upload App Bundle (.aab)",
		Description: "Upload a local .aab file as a new app bundle version and commit the edit. This does NOT assign the bundle to a release track — follow up with play_track_update (versionCodes=[<returned versionCode>]). Large files stream via a resumable upload session. Note: Play requires each new version code to be strictly greater than the previous one.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		PackageName string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
		FilePath    string `json:"filePath" jsonschema:"Absolute or workspace-relative path to the .aab file to upload."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		if err := validateArtifactFile(args.FilePath, ".aab"); err != nil {
			return errResult(err)
		}
		var out bundle
		err := c.withEdit(ctx, pkg, editOptions{Commit: true}, func(editID string) error {
			var err error
			out, err = c.uploadBundle(ctx, pkg, editID, args.FilePath)
			return err
		})
		if err != nil {
			return errResult(err)
		}
		return textResult(renderBundleUpload(args.FilePath, out))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_apks",
		Title:       "List Uploaded APKs",
		Description: "List uploaded APKs (version codes + SHA1/SHA256).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		PackageName string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		var apks []apk
		err := c.withEdit(ctx, pkg, editOptions{}, func(editID string) error {
			var err error
			apks, err = c.listApks(ctx, pkg, editID)
			return err
		})
		if err != nil {
			return errResult(err)
		}
		return textResult(renderApks(pkg, apks))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_apk_upload",
		Title:       "Upload APK",
		Description: "Upload a local .apk file as a new APK version and commit the edit. This does NOT assign the APK to a release track — follow up with play_track_update. Prefer .aab (play_bundle_upload) when possible.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		PackageName string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
		FilePath    string `json:"filePath" jsonschema:"Absolute or workspace-relative path to the .apk file to upload."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		if err := validateArtifactFile(args.FilePath, ".apk"); err != nil {
			return errResult(err)
		}
		var out apk
		err := c.withEdit(ctx, pkg, editOptions{Commit: true}, func(editID string) error {
			var err error
			out, err = c.uploadApk(ctx, pkg, editID, args.FilePath)
			return err
		})
		if err != nil {
			return errResult(err)
		}
		return textResult(renderApkUpload(args.FilePath, out))
	})

	// ---- store images (edit-based) ----

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_images_list",
		Title:       "List Store Images",
		Description: "List uploaded store images of one type for one language: ids, SHA1 and URLs. imageType: phoneScreenshots, sevenInchScreenshots, tenInchScreenshots, tvScreenshots, wearScreenshots, icon, featureGraphic, tvBanner.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args imagesArgs) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		language, it, res := validateImageArgs(args.Language, args.ImageType)
		if res != nil {
			return res, nil, nil
		}
		var images []image
		err := c.withEdit(ctx, pkg, editOptions{}, func(editID string) error {
			var err error
			images, err = c.listImages(ctx, pkg, editID, language, it)
			return err
		})
		if err != nil {
			return errResult(err)
		}
		return textResult(renderImages(pkg, language, it, images))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_images_upload",
		Title:       "Upload Store Images",
		Description: "Upload one or more image files (PNG/JPEG, ≤15MB each) for one imageType + language and commit. Icon: exactly one 512×512 PNG. featureGraphic: 1024×500. phoneScreenshots: 2–8 images. Re-uploading a type replaces nothing — delete old images first (play_images_delete).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args imagesUploadArgs) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		language, it, res := validateImageArgs(args.Language, args.ImageType)
		if res != nil {
			return res, nil, nil
		}
		if len(args.FilePaths) == 0 {
			return errResult(errors.New("filePaths is required (one or more image files)"))
		}
		var uploads []imageUpload
		for _, p := range args.FilePaths {
			ct, data, err := readImageFile(p)
			if err != nil {
				return errResult(err)
			}
			uploads = append(uploads, imageUpload{Path: p, ContentType: ct, Data: data})
		}
		var uploaded []image
		err := c.withEdit(ctx, pkg, editOptions{Commit: true}, func(editID string) error {
			uploaded = nil
			for _, up := range uploads {
				img, err := c.uploadImage(ctx, pkg, editID, language, it, up.ContentType, up.Data)
				if err != nil {
					return err
				}
				uploaded = append(uploaded, img)
			}
			return nil
		})
		if err != nil {
			return errResult(err)
		}
		return textResult(renderImagesUpload(language, it, uploads, uploaded))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_images_delete",
		Title:       "Delete Store Images",
		Description: "Delete one uploaded store image by id, or ALL images of the given imageType + language when imageId is omitted (destructive — list first with play_images_list).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args imagesDeleteArgs) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		language, it, res := validateImageArgs(args.Language, args.ImageType)
		if res != nil {
			return res, nil, nil
		}
		all := args.ImageID == ""
		err := c.withEdit(ctx, pkg, editOptions{Commit: true}, func(editID string) error {
			return c.deleteImages(ctx, pkg, editID, language, it, args.ImageID)
		})
		if err != nil {
			return errResult(err)
		}
		scope := fmt.Sprintf("image %s", args.ImageID)
		if all {
			scope = "ALL images"
		}
		return textResult(fmt.Sprintf("Deleted %s of type %s for language %s in %s (edit committed).\n", scope, it, language, pkg))
	})

	// ---- in-app products (monetization API, no edit needed) ----

	registerIAPTools(s, readOnly)

	// ---- subscriptions catalog (no edit needed) ----

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_subscriptions",
		Title:       "List Subscription Products",
		Description: "List subscription products with localized titles and base plans (id, state ACTIVE/DRAFT/INACTIVE, auto-renewing vs prepaid). Purchase verification is play_purchase_subscription; creating/archiving subscriptions is not exposed.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		PackageName string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
		PageSize    int    `json:"pageSize,omitempty" jsonschema:"Max subscriptions per page (default 50)."`
		PageToken   string `json:"pageToken,omitempty" jsonschema:"Page token from a previous call."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		pageSize := args.PageSize
		if pageSize <= 0 {
			pageSize = 50
		}
		subs, next, err := c.listSubscriptions(ctx, pkg, pageSize, args.PageToken)
		if err != nil {
			return errResult(err)
		}
		return textResult(renderSubscriptions(pkg, subs, next))
	})

	// ---- reviews (no edit needed) ----

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_reviews",
		Title:       "List App Reviews",
		Description: "List recent user reviews with star ratings, text, device/app info and your replies. Reviews appear after the app has enough installs. Reply with play_review_reply (≤350 chars, public).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args reviewsArgs) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		maxResults := args.MaxResults
		if maxResults <= 0 {
			maxResults = 10
		}
		if maxResults > 100 {
			maxResults = 100
		}
		args.MaxResults = maxResults
		reviews, next, err := c.listReviews(ctx, pkg, args)
		if err != nil {
			return errResult(err)
		}
		return textResult(renderReviews(pkg, reviews, next))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_review_reply",
		Title:       "Reply to a Review",
		Description: "Post a public developer reply to one review (max 350 characters; the same user sees it in the Play Store and may get a notification). Get reviewId from play_reviews. One reply per review — a new reply replaces the old one.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		PackageName string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
		ReviewID    string `json:"reviewId" jsonschema:"Review id from play_reviews."`
		ReplyText   string `json:"replyText" jsonschema:"Public reply text, max 350 characters."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		if args.ReviewID == "" {
			return errResult(errors.New("reviewId is required (from play_reviews)"))
		}
		text := strings.TrimSpace(args.ReplyText)
		if text == "" {
			return errResult(errors.New("replyText is required"))
		}
		if n := len(text); n > 350 {
			return errResult(fmt.Errorf("replyText is %d characters; the limit is 350", n))
		}
		r, err := c.replyReview(ctx, pkg, args.ReviewID, text)
		if err != nil {
			return errResult(err)
		}
		out := fmt.Sprintf("Replied to review %s in %s:\n\n%s", args.ReviewID, pkg, renderReviews(pkg, []review{r}, ""))
		return textResult(out)
	})

	// ---- purchase verification (no edit needed) ----

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_purchase_product",
		Title:       "Verify One-Time Product Purchase",
		Description: "Verify the purchase state of a one-time in-app product purchase: state (PURCHASED/PENDING/CANCELLED), acknowledgement state, order id, completion time. token is the purchaseToken from your app's backend / RTDN (not the orderId).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		PackageName string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
		Token       string `json:"token" jsonschema:"purchaseToken of the purchase to verify."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		if args.Token == "" {
			return errResult(errors.New("token is required (the purchaseToken, not the orderId)"))
		}
		raw, err := c.getPurchaseV2(ctx, "productsv2", pkg, args.Token)
		if err != nil {
			return errResult(err)
		}
		var p productPurchaseV2
		if err := json.Unmarshal(raw, &p); err != nil {
			return errResult(err)
		}
		return textResult(renderProductPurchase(p))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_purchase_subscription",
		Title:       "Verify Subscription Purchase",
		Description: "Verify the state of a subscription purchase (v2): state (ACTIVE/CANCELED/IN_GRACE_PERIOD/ON_HOLD/PAUSED/EXPIRED/…), acknowledgement, expiry time per line item, auto-renew on/off, base plan/offer, region, and linkedPurchaseToken when this token was replaced by an upgrade/downgrade (verify the newest token instead).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		PackageName string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
		Token       string `json:"token" jsonschema:"purchaseToken of the subscription to verify."`
	}) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		if args.Token == "" {
			return errResult(errors.New("token is required (the purchaseToken, not the orderId)"))
		}
		raw, err := c.getPurchaseV2(ctx, "subscriptionsv2", pkg, args.Token)
		if err != nil {
			return errResult(err)
		}
		var p subscriptionPurchaseV2
		if err := json.Unmarshal(raw, &p); err != nil {
			return errResult(err)
		}
		return textResult(renderSubscriptionPurchase(p))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_voided_purchases",
		Title:       "List Voided Purchases",
		Description: "List refunded/voided purchases (refunds, revocations) in a time window, so your backend can revoke entitlements. startTime/endTime accept ISO dates (2026-01-31), RFC 3339 timestamps, or epoch milliseconds. type: 0=all, 1=voided purchases only, 2=canceled subscriptions only.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args voidedArgs) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		if args.StartTimeMS, args.EndTimeMS, res = resolveVoidedTimes(args); res != nil {
			return res, nil, nil
		}
		if args.MaxResults <= 0 {
			args.MaxResults = 100
		}
		purchases, pagination, err := c.listVoidedPurchases(ctx, pkg, args)
		if err != nil {
			return errResult(err)
		}
		return textResult(renderVoided(pkg, purchases, pagination))
	})
}

// ---- args types & validation ----

type listingUpdateArgs struct {
	PackageName      string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
	Language         string `json:"language" jsonschema:"BCP-47 language tag of the listing to update, e.g. en-US, vi."`
	Title            string `json:"title,omitempty" jsonschema:"App title for this language (max 30 chars)."`
	ShortDescription string `json:"shortDescription,omitempty" jsonschema:"Short description (max 80 chars)."`
	FullDescription  string `json:"fullDescription,omitempty" jsonschema:"Full description (max 4000 chars)."`
	Video            string `json:"video,omitempty" jsonschema:"YouTube video URL."`
	ValidateOnly     bool   `json:"validateOnly,omitempty" jsonschema:"Validate the edit instead of committing (dry run)."`
}

type trackUpdateArgs struct {
	PackageName             string          `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
	Track                   string          `json:"track" jsonschema:"Track name: production, internal, beta, alpha, a custom track name, or a form-factor track like wear:production."`
	Status                  string          `json:"status" jsonschema:"Release status: draft, inProgress (serving to userFraction of users), halted, or completed."`
	VersionCodes            int64List       `json:"versionCodes" jsonschema:"ALL version codes to keep in this release (numbers or strings) — builds not listed here leave the track."`
	UserFraction            float64         `json:"userFraction,omitempty" jsonschema:"Staged rollout fraction for inProgress releases: 0.0–1.0; values >1 are read as percent (10 → 0.1)."`
	InAppUpdatePriority     int             `json:"inAppUpdatePriority,omitempty" jsonschema:"In-app update priority 0–5 (higher = more urgent; cannot change after rollout starts)."`
	Name                    string          `json:"name,omitempty" jsonschema:"Release name; auto-generated when omitted."`
	ReleaseNotes            []localizedText `json:"releaseNotes,omitempty" jsonschema:"What's new per language: [{language: \"en-US\", text: \"...\"}]."`
	ValidateOnly            bool            `json:"validateOnly,omitempty" jsonschema:"Validate the edit instead of committing (dry run)."`
	ChangesNotSentForReview bool            `json:"changesNotSentForReview,omitempty" jsonschema:"Commit without sending the changes for review (keeps them as draft pending other changes)."`
}

var releaseStatuses = []string{"draft", "inProgress", "halted", "completed"}

func normalizeReleaseStatus(s string) (string, error) {
	n := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "_", ""))
	switch n {
	case "draft":
		return "draft", nil
	case "inprogress", "in progress", "rolling":
		return "inProgress", nil
	case "halted":
		return "halted", nil
	case "completed", "full", "fully rolled out":
		return "completed", nil
	case "", "statusunspecified":
		return "", fmt.Errorf("status is required: one of %s", strings.Join(releaseStatuses, ", "))
	default:
		return "", fmt.Errorf("invalid status %q: use one of %s", s, strings.Join(releaseStatuses, ", "))
	}
}

// normalizeUserFraction maps 0–1 fractions as-is and 1–100 percents to 0–1;
// 0 means "no staged rollout".
func normalizeUserFraction(f float64) (float64, error) {
	switch {
	case f < 0:
		return 0, fmt.Errorf("userFraction must be ≥ 0, got %v", f)
	case f == 0:
		return 0, nil
	case f <= 1:
		return f, nil
	case f <= 100:
		return f / 100, nil
	default:
		return 0, fmt.Errorf("userFraction must be 0.0–1.0 (or a percent 1–100), got %v", f)
	}
}

func buildTrackBody(args trackUpdateArgs) (map[string]any, error) {
	status, err := normalizeReleaseStatus(args.Status)
	if err != nil {
		return nil, err
	}
	if len(args.VersionCodes) == 0 {
		return nil, errors.New("versionCodes is required: list every version code to keep in the release, including previous ones you want to retain")
	}
	fraction, err := normalizeUserFraction(args.UserFraction)
	if err != nil {
		return nil, err
	}
	if fraction > 0 && status != "inProgress" {
		return nil, fmt.Errorf("userFraction only applies to status=inProgress releases (got status=%s)", status)
	}
	priority := args.InAppUpdatePriority
	if priority < 0 || priority > 5 {
		return nil, fmt.Errorf("inAppUpdatePriority must be 0–5, got %d", priority)
	}
	release := map[string]any{
		"status":       status,
		"versionCodes": []string(args.VersionCodes),
	}
	if args.Name != "" {
		release["name"] = args.Name
	}
	if fraction > 0 {
		release["userFraction"] = fraction
	}
	if priority != 0 {
		release["inAppUpdatePriority"] = priority
	}
	if len(args.ReleaseNotes) > 0 {
		notes := make([]map[string]string, 0, len(args.ReleaseNotes))
		for i, n := range args.ReleaseNotes {
			if n.Language == "" || n.Text == "" {
				return nil, fmt.Errorf("releaseNotes[%d] needs both language and text", i)
			}
			notes = append(notes, map[string]string{"language": n.Language, "text": n.Text})
		}
		release["releaseNotes"] = notes
	}
	return map[string]any{
		"track":    args.Track,
		"releases": []any{release},
	}, nil
}

var imageTypes = []string{"phoneScreenshots", "sevenInchScreenshots", "tenInchScreenshots", "tvScreenshots", "wearScreenshots", "icon", "featureGraphic", "tvBanner"}

// normalizeImageType maps lenient input (PHONE_SCREENSHOTS, phone screenshots)
// onto the API's camelCase enum values.
func normalizeImageType(s string) (string, error) {
	norm := strings.ToLower(strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '_' {
			return -1
		}
		return r
	}, strings.TrimSpace(s)))
	for _, t := range imageTypes {
		if strings.ToLower(t) == norm {
			return t, nil
		}
	}
	return "", fmt.Errorf("unknown imageType %q — allowed: %s", s, strings.Join(imageTypes, ", "))
}

func validateImageArgs(language, imageType string) (string, string, *mcp.CallToolResult) {
	if strings.TrimSpace(language) == "" {
		res, _, _ := errResult(errors.New("language is required (BCP-47 tag, e.g. en-US — the default store language usually matches the app's defaultLanguage)"))
		return "", "", res
	}
	it, err := normalizeImageType(imageType)
	if err != nil {
		res, _, _ := errResult(err)
		return "", "", res
	}
	return strings.TrimSpace(language), it, nil
}

type imagesArgs struct {
	PackageName string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
	Language    string `json:"language" jsonschema:"BCP-47 language tag, e.g. en-US."`
	ImageType   string `json:"imageType" jsonschema:"phoneScreenshots, sevenInchScreenshots, tenInchScreenshots, tvScreenshots, wearScreenshots, icon, featureGraphic, tvBanner."`
}

type imagesUploadArgs struct {
	PackageName string   `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
	Language    string   `json:"language" jsonschema:"BCP-47 language tag, e.g. en-US."`
	ImageType   string   `json:"imageType" jsonschema:"phoneScreenshots, sevenInchScreenshots, tenInchScreenshots, tvScreenshots, wearScreenshots, icon, featureGraphic, tvBanner."`
	FilePaths   []string `json:"filePaths" jsonschema:"Paths of the image files (PNG/JPEG/WebP, ≤15MB each)."`
}

type imagesDeleteArgs struct {
	PackageName string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
	Language    string `json:"language" jsonschema:"BCP-47 language tag, e.g. en-US."`
	ImageType   string `json:"imageType" jsonschema:"phoneScreenshots, sevenInchScreenshots, tenInchScreenshots, tvScreenshots, wearScreenshots, icon, featureGraphic, tvBanner."`
	ImageID     string `json:"imageId,omitempty" jsonschema:"Id of one image to delete; omit to delete ALL images of this type + language."`
}

var imageExtContentTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
}

type imageUpload struct {
	Path        string
	ContentType string
	Data        []byte
}

func readImageFile(path string) (string, []byte, error) {
	ct, ok := imageExtContentTypes[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return "", nil, fmt.Errorf("%s: unsupported image type (use .png, .jpg, .jpeg or .webp)", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	if len(data) > maxImageUploadSize {
		return "", nil, fmt.Errorf("%s is %d bytes; the API accepts at most %d bytes per image", path, len(data), maxImageUploadSize)
	}
	if len(data) == 0 {
		return "", nil, fmt.Errorf("%s is empty", path)
	}
	return ct, data, nil
}

func validateArtifactFile(path, wantExt string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("filePath is required (a local %s file)", wantExt)
	}
	if ext := strings.ToLower(filepath.Ext(path)); ext != wantExt {
		return fmt.Errorf("%s does not look like a %s file (expected extension %s)", path, wantExt, wantExt)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("%s is a directory, not a file", path)
	}
	if fi.Size() == 0 {
		return fmt.Errorf("%s is empty", path)
	}
	return nil
}

type reviewsArgs struct {
	PackageName         string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
	MaxResults          int    `json:"maxResults,omitempty" jsonschema:"How many reviews to return (default 10, max 100)."`
	StartIndex          int    `json:"startIndex,omitempty" jsonschema:"Index of the first review to return."`
	Token               string `json:"token,omitempty" jsonschema:"Pagination token from a previous call."`
	TranslationLanguage string `json:"translationLanguage,omitempty" jsonschema:"Return the review text also translated into this language (BCP-47)."`
}

type voidedArgs struct {
	PackageName           string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
	StartTime             string `json:"startTime,omitempty" jsonschema:"Oldest voided purchase to include: ISO date (2026-01-31), RFC 3339 timestamp, or epoch milliseconds."`
	EndTime               string `json:"endTime,omitempty" jsonschema:"Newest voided purchase to include: same formats as startTime."`
	MaxResults            int    `json:"maxResults,omitempty" jsonschema:"Max results per page (default 100)."`
	StartIndex            int    `json:"startIndex,omitempty" jsonschema:"Index of the first result to return (indexed paging)."`
	Token                 string `json:"token,omitempty" jsonschema:"Pagination token from a previous call."`
	Type                  int    `json:"type,omitempty" jsonschema:"0 = all (default), 1 = voided purchases only, 2 = canceled subscriptions only."`
	IncludePartialRefunds bool   `json:"includePartialRefunds,omitempty" jsonschema:"Include quantity-based partial refunds."`
	// filled in by resolveVoidedTimes before the API call
	StartTimeMS string `json:"-"`
	EndTimeMS   string `json:"-"`
}

// resolveVoidedTimes converts the user-facing time inputs into the
// epoch-milliseconds strings the API expects.
func resolveVoidedTimes(args voidedArgs) (string, string, *mcp.CallToolResult) {
	start, err := toEpochMS(args.StartTime)
	if err != nil {
		res, _, _ := errResult(fmt.Errorf("startTime: %w", err))
		return "", "", res
	}
	end, err := toEpochMS(args.EndTime)
	if err != nil {
		res, _, _ := errResult(fmt.Errorf("endTime: %w", err))
		return "", "", res
	}
	return start, end, nil
}

func toEpochMS(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return strconv.FormatInt(t.UnixMilli(), 10), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return strconv.FormatInt(t.UnixMilli(), 10), nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return strconv.FormatInt(n, 10), nil
	}
	return "", fmt.Errorf("cannot parse %q as RFC 3339, YYYY-MM-DD or epoch milliseconds", s)
}
