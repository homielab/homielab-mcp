package googleplay

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/homielab/homielab-mcp/internal/render"
)

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// renderVersionCodes lists version codes compactly, tail-truncated.
func renderVersionCodes(codes []string) string {
	const max = 10
	if len(codes) <= max {
		return "[" + strings.Join(codes, ", ") + "]"
	}
	return fmt.Sprintf("[%s … +%d more]", strings.Join(codes[:max], ", "), len(codes)-max)
}

func renderDetails(pkg string, d appDetails) string {
	var b strings.Builder
	fmt.Fprintf(&b, "App details for %s:\n", pkg)
	fmt.Fprintf(&b, "  default language: %s\n", render.OrDash(d.DefaultLanguage))
	fmt.Fprintf(&b, "  contact email:    %s\n", render.OrDash(d.ContactEmail))
	fmt.Fprintf(&b, "  contact phone:    %s\n", render.OrDash(d.ContactPhone))
	fmt.Fprintf(&b, "  website:          %s\n", render.OrDash(d.ContactWebsite))
	return b.String()
}

func renderListings(pkg string, listings []listing) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Store listings for %s — %d language(s):\n\n", pkg, len(listings))
	if len(listings) == 0 {
		b.WriteString("No listings found.")
		return b.String()
	}
	b.WriteString("| Language | Title | Short description | Video |\n| --- | --- | --- | --- |\n")
	for _, l := range listings {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n",
			l.Language, render.MDCell(l.Title), render.MDCell(truncate(l.ShortDescription, 80)), render.MDCell(l.Video))
	}
	return b.String()
}

func renderListingUpdate(pkg, language string, l listing, validateOnly bool) string {
	var b strings.Builder
	if validateOnly {
		fmt.Fprintf(&b, "VALIDATED ONLY — the listing change for %s (%s) passed validation but was NOT committed.\n\n", pkg, language)
	} else {
		fmt.Fprintf(&b, "Committed the listing update for %s (%s). It goes through Play review before going live.\n\n", pkg, language)
	}
	fmt.Fprintf(&b, "- title: %s\n- short description: %s\n- video: %s\n", l.Title, l.ShortDescription, l.Video)
	return b.String()
}

func releaseStatusEmoji(status string) string {
	switch status {
	case "completed":
		return "✅"
	case "inProgress":
		return "🚀"
	case "halted":
		return "⏸"
	case "draft":
		return "📝"
	default:
		return "·"
	}
}

func renderRelease(r trackRelease, prefix string) string {
	var b strings.Builder
	fraction := ""
	if r.UserFraction > 0 {
		fraction = fmt.Sprintf(" rollout=%.1f%%", r.UserFraction*100)
	}
	prio := ""
	if r.InAppUpdatePriority != 0 {
		prio = fmt.Sprintf(" priority=%d", r.InAppUpdatePriority)
	}
	name := ""
	if r.Name != "" {
		name = fmt.Sprintf(" %q", r.Name)
	}
	fmt.Fprintf(&b, "%s%s%s status=%s versions=%s%s%s\n",
		prefix, releaseStatusEmoji(r.Status), name, r.Status, renderVersionCodes(r.VersionCodes), fraction, prio)
	for _, n := range r.ReleaseNotes {
		fmt.Fprintf(&b, "%s    note %s: %s\n", prefix, n.Language, truncate(n.Text, 120))
	}
	return b.String()
}

func renderTracks(pkg string, tracks []track) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Release tracks for %s — %d track(s):\n\n", pkg, len(tracks))
	if len(tracks) == 0 {
		b.WriteString("No tracks returned.")
		return b.String()
	}
	for _, t := range tracks {
		fmt.Fprintf(&b, "▶ %s (%d %s)\n", t.Track, len(t.Releases), plural(len(t.Releases), "release", "releases"))
		for _, r := range t.Releases {
			b.WriteString(renderRelease(r, "  · "))
		}
	}
	return b.String()
}

func renderTrackUpdate(trackName string, t track, validateOnly bool) string {
	var b strings.Builder
	if validateOnly {
		fmt.Fprintf(&b, "VALIDATED ONLY — the change to track %q passed validation but was NOT committed.\n\n", trackName)
	} else {
		fmt.Fprintf(&b, "Committed the update to track %q. New/changed releases go through Play review before reaching users.\n\n", trackName)
	}
	if len(t.Releases) == 0 {
		b.WriteString("(track now has no active releases)")
		return b.String()
	}
	for _, r := range t.Releases {
		b.WriteString(renderRelease(r, "· "))
	}
	return b.String()
}

func renderBundles(pkg string, bundles []bundle) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d uploaded %s for %s:\n", len(bundles), plural(len(bundles), "bundle", "bundles"), pkg)
	for _, bu := range bundles {
		fmt.Fprintf(&b, "- versionCode %d  sha1=%s sha256=%s\n", bu.VersionCode, shortSHA(bu.SHA1), shortSHA(bu.SHA256))
	}
	if len(bundles) > 0 {
		fmt.Fprintf(&b, "\nNewest version code: %d.\n", bundles[len(bundles)-1].VersionCode)
	}
	return b.String()
}

func renderBundleUpload(path string, bu bundle) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Uploaded %s as app bundle versionCode %d (sha1=%s).\n\n", path, bu.VersionCode, shortSHA(bu.SHA1))
	b.WriteString("The bundle is NOT live yet — assign it to a track, e.g.:\n" +
		"  play_track_update {\"track\": \"production\", \"status\": \"inProgress\", \"userFraction\": 0.1, \"versionCodes\": [" +
		strconv.FormatInt(bu.VersionCode, 10) + "]}\n" +
		"(include previously retained version codes in versionCodes).")
	return b.String()
}

func renderApks(pkg string, apks []apk) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d uploaded %s for %s:\n", len(apks), plural(len(apks), "APK", "APKs"), pkg)
	for _, a := range apks {
		fmt.Fprintf(&b, "- versionCode %d  sha1=%s sha256=%s\n", a.VersionCode, shortSHA(a.Binary.SHA1), shortSHA(a.Binary.SHA256))
	}
	if len(apks) > 0 {
		fmt.Fprintf(&b, "\nNewest version code: %d.\n", apks[len(apks)-1].VersionCode)
	}
	return b.String()
}

func renderApkUpload(path string, a apk) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Uploaded %s as APK versionCode %d (sha1=%s).\n\n", path, a.VersionCode, shortSHA(a.Binary.SHA1))
	b.WriteString("The APK is NOT live yet — assign it to a track with play_track_update (include this version code in versionCodes).")
	return b.String()
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}

func renderImages(pkg, language, imageType string, images []image) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d image(s) of type %s for %s (%s):\n", len(images), imageType, pkg, language)
	for _, im := range images {
		fmt.Fprintf(&b, "- id=%s sha1=%s url=%s\n", im.ID, shortSHA(im.SHA1), im.URL)
	}
	return b.String()
}

func renderImagesUpload(language, imageType string, uploads []imageUpload, uploaded []image) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Uploaded %d image(s) as %s for language %s and committed:\n", len(uploaded), imageType, language)
	for i, im := range uploaded {
		fmt.Fprintf(&b, "- %s → id=%s sha1=%s\n", uploads[i].Path, im.ID, shortSHA(im.SHA1))
	}
	return b.String()
}

func renderSubscriptions(pkg string, subs []subscription, nextPageToken string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d subscription %s for %s:\n\n", len(subs), plural(len(subs), "product", "products"), pkg)
	if len(subs) == 0 {
		b.WriteString("No subscription products.")
		return b.String()
	}
	for _, s := range subs {
		title := ""
		for _, l := range s.Listings {
			if title == "" {
				title = l.Title
			}
		}
		fmt.Fprintf(&b, "▶ %s — %s\n", s.ProductID, title)
		for _, bp := range s.BasePlans {
			kind := "auto-renewing"
			if bp.PrepaidBasePlanType != nil {
				kind = "prepaid"
			} else if bp.InstallmentsBasePlanType != nil {
				kind = "installments"
			}
			fmt.Fprintf(&b, "  · basePlan %s: %s (%s)\n", bp.BasePlanID, strings.ToLower(bp.State), kind)
		}
	}
	if nextPageToken != "" {
		fmt.Fprintf(&b, "\nMore pages available — pass pageToken=%q.\n", nextPageToken)
	}
	return b.String()
}

func tsString(t timestamp) string {
	sec, err := strconv.ParseInt(t.Seconds, 10, 64)
	if err != nil {
		return ""
	}
	return time.Unix(sec, int64(t.Nanos)).UTC().Format("2006-01-02 15:04 MST")
}

func renderReviews(pkg string, reviews []review, nextPageToken string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d review(s) for %s:\n\n", len(reviews), pkg)
	if len(reviews) == 0 {
		b.WriteString("No reviews returned.")
		return b.String()
	}
	for _, r := range reviews {
		fmt.Fprintf(&b, "### %s (id=%s)\n", r.AuthorName, r.ReviewID)
		for _, c := range r.Comments {
			if uc := c.UserComment; uc != nil {
				meta := []string{}
				if uc.StarRating > 0 {
					meta = append(meta, fmt.Sprintf("%d★", uc.StarRating))
				}
				if uc.Device != "" {
					meta = append(meta, uc.Device)
				}
				if uc.AppVersionName != "" {
					meta = append(meta, "app "+uc.AppVersionName)
				}
				if uc.ThumbsUpCount > 0 {
					meta = append(meta, fmt.Sprintf("👍%d", uc.ThumbsUpCount))
				}
				if uc.ThumbsDownCount > 0 {
					meta = append(meta, fmt.Sprintf("👎%d", uc.ThumbsDownCount))
				}
				fmt.Fprintf(&b, "- %s — %s\n", strings.Join(meta, ", "), tsString(uc.LastModified))
				fmt.Fprintf(&b, "  %s\n", render.MDCell(truncate(uc.Text, 500)))
			}
			if dc := c.DeveloperComment; dc != nil {
				fmt.Fprintf(&b, "- your reply (%s): %s\n", tsString(dc.LastModified), render.MDCell(truncate(dc.Text, 350)))
			}
		}
		b.WriteString("\n")
	}
	if nextPageToken != "" {
		fmt.Fprintf(&b, "More pages — pass token=%q to play_reviews.\n", nextPageToken)
	}
	return b.String()
}

func renderProductPurchase(p productPurchaseV2) string {
	var b strings.Builder
	b.WriteString("One-time product purchase (v2):\n")
	fmt.Fprintf(&b, "  order id:     %s\n", render.OrDash(p.OrderID))
	fmt.Fprintf(&b, "  state:        %s\n", stateTail(p.PurchaseStateContext.PurchaseState))
	fmt.Fprintf(&b, "  acknowledged: %s\n", ackLabel(p.AcknowledgementState))
	if p.PurchaseCompletionTime != "" {
		fmt.Fprintf(&b, "  completed:    %s\n", p.PurchaseCompletionTime)
	}
	if p.RegionCode != "" {
		fmt.Fprintf(&b, "  region:       %s\n", p.RegionCode)
	}
	for _, li := range p.ProductLineItem {
		prod := li.ProductID
		if li.ProductOfferDetails != nil && li.ProductOfferDetails.BasePlanID != "" {
			prod += " (" + li.ProductOfferDetails.BasePlanID
			if li.ProductOfferDetails.OfferToken != "" {
				prod += ", offer token present"
			}
			prod += ")"
		}
		fmt.Fprintf(&b, "  product:      %s\n", prod)
	}
	if p.ObfuscatedExternalAccountID != "" {
		fmt.Fprintf(&b, "  obfuscated account id: %s\n", p.ObfuscatedExternalAccountID)
	}
	if p.ObfuscatedExternalProfileID != "" {
		fmt.Fprintf(&b, "  obfuscated profile id: %s\n", p.ObfuscatedExternalProfileID)
	}
	return b.String()
}

func renderSubscriptionPurchase(p subscriptionPurchaseV2) string {
	var b strings.Builder
	b.WriteString("Subscription purchase (v2):\n")
	fmt.Fprintf(&b, "  state:        %s\n", stateTail(p.SubscriptionState))
	fmt.Fprintf(&b, "  acknowledged: %s\n", ackLabel(p.AcknowledgementState))
	if p.StartTime != "" {
		fmt.Fprintf(&b, "  start:        %s\n", p.StartTime)
	}
	if p.RegionCode != "" {
		fmt.Fprintf(&b, "  region:       %s\n", p.RegionCode)
	}
	if p.LinkedPurchaseToken != "" {
		b.WriteString("  ⚠ this token was replaced by an upgrade/downgrade — treat it as superseded; verify the token that produced the linked purchase.\n")
	}
	for _, li := range p.LineItems {
		fmt.Fprintf(&b, "  line item:    %s (expires %s)\n", li.ProductID, render.OrDash(li.ExpiryTime))
		if od := li.OfferDetails; od != nil {
			detail := od.BasePlanID
			if od.OfferID != "" {
				detail += " + offer " + od.OfferID
			}
			fmt.Fprintf(&b, "                %s\n", detail)
		}
		if ar := li.AutoRenewingPlan; ar != nil {
			renew := "OFF"
			if ar.AutoRenewEnabled {
				renew = "on"
			}
			price := ""
			if ar.RecurringPrice.CurrencyCode != "" {
				price = " @ " + ar.RecurringPrice.String()
			}
			fmt.Fprintf(&b, "                auto-renew: %s%s\n", renew, price)
		}
		if li.PrepaidPlan != nil {
			b.WriteString("                prepaid plan\n")
		}
	}
	if len(p.CanceledStateContext) > 0 {
		b.WriteString("  cancellation: present (see canceledStateContext for who canceled)\n")
	}
	return b.String()
}

func ackLabel(s string) string {
	switch s {
	case "ACKNOWLEDGEMENT_STATE_ACKNOWLEDGED":
		return "acknowledged"
	case "ACKNOWLEDGEMENT_STATE_PENDING":
		return "PENDING (your app/backend must acknowledge within 3 days or it is auto-refunded)"
	default:
		if s == "" {
			return "?"
		}
		return s
	}
}

func stateTail(s string) string {
	// "PURCHASED", "SUBSCRIPTION_STATE_ACTIVE" → readable tail
	if i := strings.LastIndex(s, "_STATE_"); i >= 0 {
		return strings.ToLower(strings.ReplaceAll(s[i+len("_STATE_"):], "_", " "))
	}
	return strings.ToLower(strings.ReplaceAll(s, "_", " "))
}

var voidedReasons = map[int]string{
	0: "other", 1: "order/stock issue", 2: "user regret", 3: "payment declined",
	4: "incomplete purchase", 5: "affordable pending", 6: "not delivered",
	7: "undeliverable", 8: "duplicate", 9: "paid after cancel", 10: "bought by mistake",
}

var voidedSources = map[int]string{0: "unspecified", 1: "developer", 2: "user", 3: "google"}

func renderVoided(pkg string, purchases []voidedPurchase, pagination tokenPagination) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d voided %s for %s:\n\n", len(purchases), plural(len(purchases), "purchase", "purchases"), pkg)
	if len(purchases) == 0 {
		b.WriteString("No voided purchases in this window.")
		return b.String()
	}
	b.WriteString("| Order id | Purchased | Voided | Reason | Source | Qty | Token |\n| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, vp := range purchases {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %d | %s |\n",
			render.MDCell(vp.OrderID), epochMSRender(vp.PurchaseTimeMillis), epochMSRender(vp.VoidedTimeMillis),
			voidedReasons[vp.VoidedReason], voidedSources[vp.VoidedSource], vp.VoidedQuantity,
			render.MDCell(truncate(vp.PurchaseToken, 24)))
	}
	if pagination.NextPageToken != "" {
		fmt.Fprintf(&b, "\nMore pages — pass token=%q.\n", pagination.NextPageToken)
	}
	return b.String()
}

func epochMSRender(ms string) string {
	n, err := strconv.ParseInt(ms, 10, 64)
	if err != nil || n == 0 {
		return render.OrDash(ms)
	}
	return time.UnixMilli(n).UTC().Format("2006-01-02 15:04")
}
