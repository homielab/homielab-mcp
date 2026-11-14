package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/homielab/homielab-mcp/internal/googleapi"
)

var apiBase = "https://androidpublisher.googleapis.com/androidpublisher/v3"
var uploadBase = "https://androidpublisher.googleapis.com/upload/androidpublisher/v3"
var resumableBase = "https://androidpublisher.googleapis.com/resumable/upload/androidpublisher/v3"

const maxImageUploadSize = 15 << 20 // 15 MiB, matches the API's images.upload maxSize

type playClient struct {
	*googleapi.Client
}

func newPlayClient(ctx context.Context) (*playClient, error) {
	hc, err := auth.HTTPClient(ctx)
	if err != nil {
		return nil, err
	}
	return &playClient{googleapi.New(hc, "Play Developer API")}, nil
}

// uploadBytes performs a media upload whose bytes must not be blindly retried:
// once a server has received the payload a retry could duplicate the artifact.
// Retries happen only on transport-level failures (no HTTP response at all).
// body must be a fresh reader on every call. hc carries the OAuth transport;
// callers may pass a timeout-free client for large uploads.
func (c *playClient) uploadBytes(ctx context.Context, hc *http.Client, method, fullURL, contentType string, length int64, body io.Reader) ([]byte, error) {
	var err error
	for attempt := 1; attempt <= googleapi.MaxRetries; attempt++ {
		if attempt > 1 {
			// A consumed reader would resend nothing (bytes.Reader) or the
			// wrong byte range (os.File mid-upload) — rewind it or give up.
			s, ok := body.(io.Seeker)
			if !ok {
				return nil, fmt.Errorf("cannot retry upload after %v: body is not seekable", err)
			}
			if _, serr := s.Seek(0, io.SeekStart); serr != nil {
				return nil, serr
			}
			if werr := googleapi.WaitBackoff(ctx, attempt, 0); werr != nil {
				return nil, werr
			}
			log.Printf("retrying upload %s %s after transport error: %v", method, fullURL, err)
		}
		req, rerr := http.NewRequestWithContext(ctx, method, fullURL, body)
		if rerr != nil {
			return nil, rerr
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if length > 0 {
			req.ContentLength = length
		}
		resp, derr := hc.Do(req)
		if derr != nil {
			err = derr
			continue
		}
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, googleapi.MaxResponseBody))
		resp.Body.Close()
		if rerr != nil {
			err = rerr
			continue
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			return nil, c.Error(resp.StatusCode, data)
		}
		return data, nil
	}
	return nil, err
}

// ---- package name resolution ----
//
// The Android Publisher API has no "list my apps" endpoint, so every tool
// needs a packageName. To keep sessions smooth the server remembers the last
// successfully validated package name and uses it when packageName is omitted.

func lastPackagePath() (string, error) {
	dir, err := auth.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "last_package.txt"), nil
}

func rememberedPackage() string {
	p, err := lastPackagePath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func rememberPackage(pkg string) {
	p, err := lastPackagePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(p, []byte(pkg+"\n"), 0o600)
}

var packageRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)

// validatePackage sanity-checks the format client-side before spending a
// network round-trip.
func validatePackage(pkg string) error {
	if !packageRE.MatchString(pkg) {
		return fmt.Errorf("%q is not a valid Android package name (expected dotted segments like com.example.app)", pkg)
	}
	return nil
}

// resolvePackage validates an explicitly given package name (remembering it
// for later) or falls back to the last remembered one when empty. The Android
// Publisher API has no "list apps" endpoint, so there is nothing to resolve
// against server-side.
func resolvePackage(pkg string) (string, error) {
	pkg = strings.TrimSpace(pkg)
	if pkg == "" {
		if last := rememberedPackage(); last != "" {
			return last, nil
		}
		return "", errors.New("packageName is required (e.g. com.example.app). " +
			"You can find it in the Play Console URL (.../app/<package>) or the Play Store listing URL (...?id=<package>). " +
			"After the first successful call the server remembers the last used package name.")
	}
	if err := validatePackage(pkg); err != nil {
		return "", err
	}
	rememberPackage(pkg)
	return pkg, nil
}

// ---- edit lifecycle ----

type appEdit struct {
	ID                string `json:"id"`
	ExpiryTimeSeconds string `json:"expiryTimeSeconds"`
}

// editOptions controls how withEdit finishes an edit.
type editOptions struct {
	Commit                  bool // POST :commit when the work succeeded
	ValidateOnly            bool // POST :validate instead of :commit (implies commit-side behavior)
	ChangesNotSentForReview bool // pass changesNotSentForReview=true to commit
}

// withEdit opens a short-lived edit, runs fn against it and cleans up. Reads
// use Commit=false (edit is deleted; nothing changes server-side). Writes use
// Commit=true; the edit is committed (or only validated) after fn succeeds,
// then the edit is deleted in case the commit failed. A developer account can
// only have one open edit at a time, so conflicts surface as 409 with a hint.
func (c *playClient) withEdit(ctx context.Context, pkg string, opts editOptions, fn func(editID string) error) error {
	// edits:insert is not idempotent: if a first attempt landed server-side
	// but its response was lost, a retry would hit the one-open-edit limit.
	// No retry — a genuine transient failure just fails the tool call.
	body, _, err := c.DoOnce(ctx, http.MethodPost, googleapi.JoinURL(apiBase, "/applications/"+pkg+"/edits"), map[string]any{})
	if err != nil {
		return editConflictHint(err)
	}
	var ed appEdit
	if err := json.Unmarshal(body, &ed); err != nil || ed.ID == "" {
		if err == nil {
			err = fmt.Errorf("edits.insert returned no edit id")
		}
		return err
	}

	fnErr := fn(ed.ID)

	finalErr := fnErr
	if fnErr == nil && opts.Commit {
		action := "commit"
		if opts.ValidateOnly {
			action = "validate"
		}
		u := googleapi.JoinURL(apiBase, fmt.Sprintf("/applications/%s/edits/%s:%s", pkg, ed.ID, action))
		if opts.ChangesNotSentForReview {
			u += "?changesNotSentForReview=true"
		}
		if _, _, cerr := c.Do(ctx, http.MethodPost, u, nil); cerr != nil {
			finalErr = fmt.Errorf("%s: %w", action, cerr)
		}
	}
	// Clean the edit up whenever it may still exist; after a successful commit
	// Google deletes it and the 404 is not an error.
	if _, status, derr := c.Do(ctx, http.MethodDelete, googleapi.JoinURL(apiBase, "/applications/"+pkg+"/edits/"+ed.ID), nil); derr != nil && finalErr == nil && status != http.StatusNotFound {
		finalErr = fmt.Errorf("cleaning up edit: %w", derr)
	}
	return finalErr
}

func editConflictHint(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "409") || strings.Contains(strings.ToUpper(msg), "CONFLICT") {
		return fmt.Errorf("%w\n\nHint: only one edit can be open per developer account. Uncommitted edits expire automatically after a while; retry in a few minutes, or make any change in Play Console to release the stale edit.", err)
	}
	return err
}

// ---- media uploads ----

// initiateUpload starts a Google resumable-upload session and returns the
// session URL. The init request carries no artifact bytes, so 429/5xx retries
// are safe here.
func (c *playClient) initiateUpload(ctx context.Context, hc *http.Client, fullURL string, size int64, q url.Values) (string, error) {
	u := fullURL
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var sessionURL string
	var err error
	for attempt := 1; attempt <= googleapi.MaxRetries; attempt++ {
		if attempt > 1 {
			if werr := googleapi.WaitBackoff(ctx, attempt, 0); werr != nil {
				return "", werr
			}
			log.Printf("retrying upload initiation %s after error: %v", u, err)
		}
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
		if rerr != nil {
			return "", rerr
		}
		req.Header.Set("X-Upload-Content-Type", "application/octet-stream")
		req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
		req.Header.Set("Content-Type", "application/json; charset=UTF-8")
		resp, derr := hc.Do(req)
		if derr != nil {
			err = derr
			continue
		}
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, googleapi.MaxResponseBody))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err = c.Error(resp.StatusCode, data)
			if !googleapi.Retryable(resp.StatusCode, err) {
				return "", err
			}
			continue
		}
		sessionURL = resp.Header.Get("Location")
		if sessionURL == "" {
			return "", errors.New("upload initiation response has no Location header (session URL)")
		}
		return sessionURL, nil
	}
	return "", err
}

// uploadFileResumable streams a local file (APK/AAB, up to tens of GB) through
// a Google resumable-upload session into the given edit resource.
// resourcePath is relative to the v3 root, e.g.
// "/applications/{pkg}/edits/{editId}/bundles".
func (c *playClient) uploadFileResumable(ctx context.Context, resourcePath, filePath string, q url.Values) (json.RawMessage, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() == 0 {
		return nil, fmt.Errorf("%s is empty", filePath)
	}
	if q == nil {
		q = url.Values{}
	}
	q.Set("uploadType", "resumable")

	// Uploads must not inherit the 90s client timeout: an AAB can take
	// minutes. Reuse the authenticated transport, drop the timeout.
	upClient := &http.Client{Transport: c.HTTP.Transport}

	session, err := c.initiateUpload(ctx, upClient, googleapi.JoinURL(resumableBase, resourcePath), fi.Size(), q)
	if err != nil {
		return nil, err
	}
	data, err := c.uploadBytes(ctx, upClient, http.MethodPut, session, "application/octet-stream", fi.Size(), f)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

// uploadImageBytes posts raw image bytes via a simple media upload
// (uploadType=media). Images are ≤15 MiB per the API's limit.
func (c *playClient) uploadImageBytes(ctx context.Context, fullURL, contentType string, data []byte) (json.RawMessage, error) {
	return c.uploadBytes(ctx, c.HTTP, http.MethodPost, fullURL, contentType, int64(len(data)), bytes.NewReader(data))
}

// ---- response types (only the fields the tools render) ----

type appDetails struct {
	ContactWebsite  string `json:"contactWebsite"`
	DefaultLanguage string `json:"defaultLanguage"`
	ContactPhone    string `json:"contactPhone"`
	ContactEmail    string `json:"contactEmail"`
}

type localizedText struct {
	Language string `json:"language"`
	Text     string `json:"text"`
}

type listing struct {
	Title            string `json:"title"`
	ShortDescription string `json:"shortDescription"`
	Video            string `json:"video"`
	Language         string `json:"language"`
	FullDescription  string `json:"fullDescription"`
}

type trackRelease struct {
	Name                string          `json:"name"`
	Status              string          `json:"status"`
	UserFraction        float64         `json:"userFraction"`
	VersionCodes        []string        `json:"versionCodes"`
	ReleaseNotes        []localizedText `json:"releaseNotes"`
	InAppUpdatePriority int             `json:"inAppUpdatePriority"`
}

type track struct {
	Track    string         `json:"track"`
	Releases []trackRelease `json:"releases"`
}

type bundle struct {
	VersionCode int64  `json:"versionCode"`
	SHA1        string `json:"sha1"`
	SHA256      string `json:"sha256"`
}

type apkBinary struct {
	SHA1   string `json:"sha1"`
	SHA256 string `json:"sha256"`
}

type apk struct {
	VersionCode int64     `json:"versionCode"`
	Binary      apkBinary `json:"binary"`
}

type image struct {
	ID               string `json:"id"`
	URL              string `json:"url"`
	SHA1             string `json:"sha1"`
	SHA256           string `json:"sha256"`
	AiGeneratedState string `json:"aiGeneratedState"`
}

type subscriptionListing struct {
	LanguageCode string `json:"languageCode"`
	Title        string `json:"title"`
	Description  string `json:"description"`
}

type basePlan struct {
	BasePlanID string `json:"basePlanId"`
	State      string `json:"state"`
	// Exactly one of these is set per plan type.
	AutoRenewingBasePlanType map[string]any `json:"autoRenewingBasePlanType"`
	PrepaidBasePlanType      map[string]any `json:"prepaidBasePlanType"`
	InstallmentsBasePlanType map[string]any `json:"installmentsBasePlanType"`
}

type subscription struct {
	ProductID        string                `json:"productId"`
	Listings         []subscriptionListing `json:"listings"`
	BasePlans        []basePlan            `json:"basePlans"`
	TaxAndCompliance map[string]any        `json:"taxAndComplianceSettings"`
}

type offerDetails struct {
	BasePlanID string   `json:"basePlanId"`
	OfferID    string   `json:"offerId"`
	OfferTags  []string `json:"offerTags"`
}

type autoRenewingPlan struct {
	AutoRenewEnabled bool  `json:"autoRenewEnabled"`
	RecurringPrice   money `json:"recurringPrice"`
}

type subscriptionLineItem struct {
	ProductID        string            `json:"productId"`
	ExpiryTime       string            `json:"expiryTime"`
	OfferDetails     *offerDetails     `json:"offerDetails"`
	AutoRenewingPlan *autoRenewingPlan `json:"autoRenewingPlan"`
	PrepaidPlan      map[string]any    `json:"prepaidPlan"`
}

type subscriptionPurchaseV2 struct {
	Kind                 string                 `json:"kind"`
	SubscriptionState    string                 `json:"subscriptionState"`
	AcknowledgementState string                 `json:"acknowledgementState"`
	StartTime            string                 `json:"startTime"`
	RegionCode           string                 `json:"regionCode"`
	LinkedPurchaseToken  string                 `json:"linkedPurchaseToken"`
	LineItems            []subscriptionLineItem `json:"lineItems"`
	ExternalAccountID    string                 `json:"externalAccountIdentifiers"`
	CanceledStateContext map[string]any         `json:"canceledStateContext"`
}

type productOfferDetails struct {
	BasePlanID string   `json:"basePlanId"`
	OfferTags  []string `json:"offerTags"`
	OfferToken string   `json:"offerToken"`
}

type productLineItem struct {
	ProductID           string               `json:"productId"`
	ProductOfferDetails *productOfferDetails `json:"productOfferDetails"`
}

type productPurchaseV2 struct {
	OrderID              string `json:"orderId"`
	AcknowledgementState string `json:"acknowledgementState"`
	PurchaseStateContext struct {
		PurchaseState string `json:"purchaseState"`
	} `json:"purchaseStateContext"`
	PurchaseCompletionTime      string            `json:"purchaseCompletionTime"`
	RegionCode                  string            `json:"regionCode"`
	ProductLineItem             []productLineItem `json:"productLineItem"`
	ObfuscatedExternalAccountID string            `json:"obfuscatedExternalAccountId"`
	ObfuscatedExternalProfileID string            `json:"obfuscatedExternalProfileId"`
}

type timestamp struct {
	Seconds string `json:"seconds"`
	Nanos   int    `json:"nanos"`
}

type reviewComment struct {
	UserComment *struct {
		Text             string    `json:"text"`
		OriginalText     string    `json:"originalText"`
		StarRating       int       `json:"starRating"`
		Device           string    `json:"device"`
		AppVersionName   string    `json:"appVersionName"`
		AppVersionCode   int       `json:"appVersionCode"`
		AndroidOsVersion int       `json:"androidOsVersion"`
		ReviewerLanguage string    `json:"reviewerLanguage"`
		ThumbsUpCount    int       `json:"thumbsUpCount"`
		ThumbsDownCount  int       `json:"thumbsDownCount"`
		LastModified     timestamp `json:"lastModified"`
	} `json:"userComment"`
	DeveloperComment *struct {
		Text         string    `json:"text"`
		LastModified timestamp `json:"lastModified"`
	} `json:"developerComment"`
}

type review struct {
	ReviewID   string          `json:"reviewId"`
	AuthorName string          `json:"authorName"`
	Comments   []reviewComment `json:"comments"`
}

type tokenPagination struct {
	NextPageToken     string `json:"nextPageToken"`
	PreviousPageToken string `json:"previousPageToken"`
}

type voidedPurchase struct {
	OrderID            string `json:"orderId"`
	PurchaseToken      string `json:"purchaseToken"`
	PurchaseTimeMillis string `json:"purchaseTimeMillis"`
	VoidedTimeMillis   string `json:"voidedTimeMillis"`
	VoidedSource       int    `json:"voidedSource"`
	VoidedReason       int    `json:"voidedReason"`
	VoidedQuantity     int    `json:"voidedQuantity"`
}

// ---- API calls ----

func (c *playClient) getAppDetails(ctx context.Context, pkg, editID string) (appDetails, error) {
	var out appDetails
	body, _, err := c.Do(ctx, http.MethodGet, googleapi.JoinURL(apiBase, fmt.Sprintf("/applications/%s/edits/%s/details", pkg, editID)), nil)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(body, &out)
	return out, err
}

func (c *playClient) listListings(ctx context.Context, pkg, editID string) ([]listing, error) {
	var out struct {
		Listings []listing `json:"listings"`
	}
	body, _, err := c.Do(ctx, http.MethodGet, googleapi.JoinURL(apiBase, fmt.Sprintf("/applications/%s/edits/%s/listings", pkg, editID)), nil)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(body, &out)
	return out.Listings, err
}

func (c *playClient) patchListing(ctx context.Context, pkg, editID, language string, body any) (listing, error) {
	var out listing
	data, _, err := c.Do(ctx, http.MethodPatch, googleapi.JoinURL(apiBase, fmt.Sprintf("/applications/%s/edits/%s/listings/%s", pkg, editID, url.PathEscape(language))), body)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(data, &out)
	return out, err
}

func (c *playClient) listTracks(ctx context.Context, pkg, editID string) ([]track, error) {
	var out struct {
		Tracks []track `json:"tracks"`
	}
	body, _, err := c.Do(ctx, http.MethodGet, googleapi.JoinURL(apiBase, fmt.Sprintf("/applications/%s/edits/%s/tracks", pkg, editID)), nil)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(body, &out)
	return out.Tracks, err
}

func (c *playClient) updateTrack(ctx context.Context, pkg, editID, trackName string, body any) (track, error) {
	var out track
	data, _, err := c.Do(ctx, http.MethodPut, googleapi.JoinURL(apiBase, fmt.Sprintf("/applications/%s/edits/%s/tracks/%s", pkg, editID, url.PathEscape(trackName))), body)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(data, &out)
	return out, err
}

func (c *playClient) listBundles(ctx context.Context, pkg, editID string) ([]bundle, error) {
	var out struct {
		Bundles []bundle `json:"bundles"`
	}
	body, _, err := c.Do(ctx, http.MethodGet, googleapi.JoinURL(apiBase, fmt.Sprintf("/applications/%s/edits/%s/bundles", pkg, editID)), nil)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(body, &out)
	return out.Bundles, err
}

func (c *playClient) uploadBundle(ctx context.Context, pkg, editID, filePath string) (bundle, error) {
	var out bundle
	raw, err := c.uploadFileResumable(ctx, fmt.Sprintf("/applications/%s/edits/%s/bundles", pkg, editID), filePath, nil)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

func (c *playClient) listApks(ctx context.Context, pkg, editID string) ([]apk, error) {
	var out struct {
		Apks []apk `json:"apks"`
	}
	body, _, err := c.Do(ctx, http.MethodGet, googleapi.JoinURL(apiBase, fmt.Sprintf("/applications/%s/edits/%s/apks", pkg, editID)), nil)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(body, &out)
	return out.Apks, err
}

func (c *playClient) uploadApk(ctx context.Context, pkg, editID, filePath string) (apk, error) {
	var out apk
	raw, err := c.uploadFileResumable(ctx, fmt.Sprintf("/applications/%s/edits/%s/apks", pkg, editID), filePath, nil)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

func (c *playClient) listImages(ctx context.Context, pkg, editID, language, imageType string) ([]image, error) {
	var out struct {
		Images []image `json:"images"`
	}
	body, _, err := c.Do(ctx, http.MethodGet, googleapi.JoinURL(apiBase, fmt.Sprintf("/applications/%s/edits/%s/listings/%s/%s", pkg, editID, url.PathEscape(language), imageType)), nil)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(body, &out)
	return out.Images, err
}

func (c *playClient) deleteImages(ctx context.Context, pkg, editID, language, imageType, imageID string) error {
	path := fmt.Sprintf("/applications/%s/edits/%s/listings/%s/%s", pkg, editID, url.PathEscape(language), imageType)
	if imageID != "" {
		path += "/" + imageID
	}
	_, _, err := c.Do(ctx, http.MethodDelete, googleapi.JoinURL(apiBase, path), nil)
	return err
}

func (c *playClient) uploadImage(ctx context.Context, pkg, editID, language, imageType, contentType string, data []byte) (image, error) {
	var out image
	u := googleapi.JoinURL(uploadBase, fmt.Sprintf("/applications/%s/edits/%s/listings/%s/%s?uploadType=media", pkg, editID, url.PathEscape(language), imageType))
	raw, err := c.uploadImageBytes(ctx, u, contentType, data)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

func (c *playClient) listSubscriptions(ctx context.Context, pkg string, pageSize int, pageToken string) ([]subscription, string, error) {
	var out struct {
		Subscriptions []subscription `json:"subscriptions"`
		NextPageToken string         `json:"nextPageToken"`
	}
	u := googleapi.JoinURL(apiBase, "/applications/"+pkg+"/subscriptions")
	q := url.Values{}
	if pageSize > 0 {
		q.Set("pageSize", strconv.Itoa(pageSize))
	}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	body, _, err := c.Do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	err = json.Unmarshal(body, &out)
	return out.Subscriptions, out.NextPageToken, err
}

func (c *playClient) listReviews(ctx context.Context, pkg string, args reviewsArgs) ([]review, string, error) {
	var out struct {
		Reviews         []review        `json:"reviews"`
		TokenPagination tokenPagination `json:"tokenPagination"`
	}
	u := googleapi.JoinURL(apiBase, "/applications/"+pkg+"/reviews")
	q := url.Values{}
	if args.MaxResults > 0 {
		q.Set("maxResults", strconv.Itoa(args.MaxResults))
	}
	if args.StartIndex > 0 {
		q.Set("startIndex", strconv.Itoa(args.StartIndex))
	}
	if args.Token != "" {
		q.Set("token", args.Token)
	}
	if args.TranslationLanguage != "" {
		q.Set("translationLanguage", args.TranslationLanguage)
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	body, _, err := c.Do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	err = json.Unmarshal(body, &out)
	return out.Reviews, out.TokenPagination.NextPageToken, err
}

func (c *playClient) replyReview(ctx context.Context, pkg, reviewID, replyText string) (review, error) {
	var out struct {
		ReplyResult struct {
			ReplyText  string    `json:"replyText"`
			LastEdited timestamp `json:"lastEdited"`
		} `json:"replyResult"`
	}
	body, _, err := c.Do(ctx, http.MethodPost, googleapi.JoinURL(apiBase, fmt.Sprintf("/applications/%s/reviews/%s:reply", pkg, reviewID)), map[string]string{"replyText": replyText})
	if err != nil {
		return review{}, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return review{}, err
	}
	// Echo the reply as a developer comment so the renderer can show it.
	r := review{ReviewID: reviewID}
	r.Comments = append(r.Comments, reviewComment{DeveloperComment: &struct {
		Text         string    `json:"text"`
		LastModified timestamp `json:"lastModified"`
	}{Text: out.ReplyResult.ReplyText, LastModified: out.ReplyResult.LastEdited}})
	return r, nil
}

func (c *playClient) getPurchaseV2(ctx context.Context, kind, pkg, token string) (json.RawMessage, error) {
	var path string
	switch kind {
	case "productsv2":
		path = fmt.Sprintf("/applications/%s/purchases/productsv2/tokens/%s", pkg, url.PathEscape(token))
	case "subscriptionsv2":
		path = fmt.Sprintf("/applications/%s/purchases/subscriptionsv2/tokens/%s", pkg, url.PathEscape(token))
	default:
		return nil, fmt.Errorf("unknown purchase kind %q", kind)
	}
	body, _, err := c.Do(ctx, http.MethodGet, googleapi.JoinURL(apiBase, path), nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

func (c *playClient) listVoidedPurchases(ctx context.Context, pkg string, args voidedArgs) ([]voidedPurchase, tokenPagination, error) {
	var out struct {
		VoidedPurchases []voidedPurchase `json:"voidedPurchases"`
		TokenPagination tokenPagination  `json:"tokenPagination"`
	}
	u := googleapi.JoinURL(apiBase, "/applications/"+pkg+"/purchases/voidedpurchases")
	q := url.Values{}
	if args.StartTimeMS != "" {
		q.Set("startTime", args.StartTimeMS)
	}
	if args.EndTimeMS != "" {
		q.Set("endTime", args.EndTimeMS)
	}
	if args.MaxResults > 0 {
		q.Set("maxResults", strconv.Itoa(args.MaxResults))
	}
	if args.Token != "" {
		q.Set("token", args.Token)
	}
	if args.StartIndex > 0 {
		q.Set("startIndex", strconv.Itoa(args.StartIndex))
	}
	if args.IncludePartialRefunds {
		q.Set("includeQuantityBasedPartialRefund", "true")
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	body, _, err := c.Do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, tokenPagination{}, err
	}
	err = json.Unmarshal(body, &out)
	return out.VoidedPurchases, out.TokenPagination, err
}
