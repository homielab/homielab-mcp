# mcp-androidpublisher

MCP server (Go) for the **Google Play Developer API** (Android Publisher v3) —
it exposes Play Console operations as MCP tools: release tracks & staged
rollouts, app bundle / APK uploads, store listings & images, in-app products,
reviews, and purchase verification.

Unlike the other servers in this collection (read-only), this one **writes**
to your Play Console: it can publish releases, change store text and delete
products. Destructive tools are marked below.

Built with the official Go SDK `github.com/modelcontextprotocol/go-sdk`.

## Tools

| Tool | What it does |
|---|---|
| `play_login` | One-time OAuth consent in your browser; **non-blocking** — call `play_login_status` after approving. Scope `androidpublisher` (read **and** write). |
| `play_login_status` | Is the flow running / finished / failed? Is a token stored? |
| `play_logout` | Delete the stored token |
| `play_app_details` | App contact details: default language, email, phone, website |
| `play_listings` | Localized store listings per language (title, short/full description, video) |
| `play_listing_update` | Update one language's listing text and commit (`validateOnly` for a dry run) |
| `play_tracks` | All release tracks + releases: status, version codes, rollout %, release notes |
| `play_track_update` | Set what a track serves: versionCodes, status (draft/inProgress/halted/completed), staged-rollout `userFraction`, release notes, in-app update priority. `validateOnly:true` = dry run |
| `play_bundles` | Uploaded .aab bundles (version codes + SHA) |
| `play_bundle_upload` | Upload a local .aab (resumable upload, handles multi-GB files) and commit — **not** live until assigned via `play_track_update` |
| `play_apks` | Uploaded APKs |
| `play_apk_upload` | Upload a local .apk and commit |
| `play_images_list` | Store images of one type + language (icon, featureGraphic, phoneScreenshots, …) |
| `play_images_upload` | Upload PNG/JPEG/WebP images (≤15MB each) for one type + language |
| `play_images_delete` | Delete one image by id — or **ALL images** of the type when `imageId` is omitted (destructive) |
| `play_iap_list` | One-time products: id, title, purchase options with state, US price, priced regions |
| `play_iap_insert` | Create a one-time product: title/description + one price (e.g. `"2.99"` USD), converted to every region like the Play Console; activated unless `active: false` |
| `play_iap_update` | Change listings, the price of one purchase option, and/or `active` (only given parts change) |
| `play_iap_delete` | Permanently delete a one-time product (destructive — `play_iap_update {active: false}` stops sales instead) |
| `play_subscriptions` | Subscription catalog: base plans (ACTIVE/DRAFT), auto-renewing vs prepaid |
| `play_reviews` | User reviews with ratings, text, device/app info, your replies |
| `play_review_reply` | Public reply to one review (≤350 chars; one reply per review) |
| `play_purchase_product` | Verify a one-time purchase token (state, acknowledgement, order id) |
| `play_purchase_subscription` | Verify a subscription purchase token (state, expiry, auto-renew, linkedPurchaseToken warnings) |
| `play_voided_purchases` | Refunds/revocations in a time window (for revoking entitlements) |

### Prompts

- `play_release_status` — release-status report: what each track
  serves, bundles uploaded but not yet on a track, halted rollouts, recent
  reviews and refunds, with next-step tool calls.
- `play_review_triage` — review triage: classify by topic, draft
  replies (≤350 chars) for the worst unanswered reviews, device/version
  patterns, and a short action list.

## Setup (one time, ~5 minutes)

1. **Google Cloud project + OAuth client**:
   - <https://console.cloud.google.com> → create (or pick) a project.
   - *APIs & Services → Library* → search **"Google Play Android Developer
     API"** → **Enable**.
   - *OAuth consent screen* → External → add your Google account as **test
     user** (required while the app is in Testing).
   - *Credentials → Create OAuth client ID* → **Desktop app** → download JSON.

2. **Give the credentials to the server** — either:
   - copy the JSON as `client_secret*.json` into
     `<user config dir>/mcp-androidpublisher/`
     (macOS: `~/Library/Application Support/mcp-androidpublisher/`), or
   - reuse the file you made for another server in this collection — their
     config dirs are probed automatically, or
   - set env vars on the MCP registration:
     ```
     PLAY_MCP_CLIENT_ID=xxxxx.apps.googleusercontent.com
     PLAY_MCP_CLIENT_SECRET=GOCSPX-...
     ```

3. **Authorize** once (token stored at
   `<user config dir>/mcp-androidpublisher/token.json`, mode 0600):
   ```
   ~/go/bin/mcp-androidpublisher login
   ```
   Sign in with a Google account that is a member of the Play Console
   developer account (**Play Console → Users & permissions** — it needs at
   least View app information; release tools need the corresponding
   permissions). Or call the `play_login` tool from your client, then
   `play_login_status`.

4. **Register the MCP server** in your client:

   ```json
   {
     "mcpServers": {
       "androidpublisher": {
         "command": "/path/to/go/bin/mcp-androidpublisher"
       }
     }
   }
   ```

> Service accounts are not supported by this server on purpose: the OAuth
> desktop flow reuses the collection's shared login setup. If you need
> headless/CI usage, run a service-account flow separately.

## Example asks

- *"Where is the app's release at?"* → `play_tracks` — per-track releases with
  rollout fractions; the `play_release_status` prompt does the full picture.
- *"Upload the new build and push it to internal"* →
  `play_bundle_upload {"filePath": "app-release.aab"}` then
  `play_track_update {"track": "internal", "status": "completed", "versionCodes": [42]}`
- *"Launch to production for 10% of users"* →
  `play_track_update {"track": "production", "status": "inProgress", "userFraction": 10, "versionCodes": [41, 42]}`
  (`userFraction` accepts 0–1 or percent; include retained version codes!)
- *"Dry-run: is this release config valid?"* → add `"validateOnly": true`
- *"Halt the rollout"* → `play_track_update {"track": "production", "status": "halted", "versionCodes": [41, 42]}`
- *"Edit the Vietnamese short description"* →
  `play_listing_update {"language": "vi", "shortDescription": "..."}`
- *"Create a 2.99 USD in-app product"* →
  `play_iap_insert {"productId": "coins_100", "title": "100 Coins", "description": "...", "price": "2.99"}`
- *"What do the latest 1-star reviews say?"* → `play_reviews {"maxResults": 20}`
- *"Check this subscription purchase token"* →
  `play_purchase_subscription {"token": "<purchaseToken>"}`
- *"Any refunded orders this week?"* →
  `play_voided_purchases {"startTime": "2026-10-21"}`
- *"No need to retype the package name"* — omitted `packageName` reuses the
  last successful one (stored in the server's config dir); the first call
  needs it explicitly.

### Edit lifecycle (how writes work)

The API changes everything through a short-lived "edit". This server opens an
edit, applies your change, and closes it for you on every call:

- read tools open an edit and delete it (nothing changes);
- write tools open an edit, apply, then **commit** (or only `:validate` with
  `validateOnly:true`), deleting the edit if the commit failed;
- a developer account has **one open edit at a time** — run publishing
  operations sequentially; a 409 from a lingering edit explains how to clear it.

### Input validation (client-side, before spending quota)

- Package names format-checked; `versionCodes` accepts numbers or strings.
- Release statuses and `userFraction` (0–1 or percent) normalized and checked;
  `userFraction` rejected for non-`inProgress` releases.
- Image types normalized (`PHONE_SCREENSHOTS` → `phoneScreenshots`) against
  the API's enum; file extensions, size (≤15MB/image) and existence checked
  locally.
- IAP product ids, prices, currency codes and listing lengths (55/200 chars), store listing
  lengths (80/4000 chars) and reply length (≤350) validated before the call.
- In-app products use the monetization `oneTimeProducts` API; the legacy `inappproducts`
  API answers 403 "Please migrate to the new publishing API" for migrated accounts.
- `startTime`/`endTime` accept ISO dates, RFC 3339 timestamps or epoch ms.

## Robustness

- Retry (3 attempts, backoff, honors `Retry-After`) on 429/5xx and network
  errors for JSON calls; 4xx never retried; 90s HTTP timeout.
- Artifact uploads use the Google **resumable upload** protocol (session
  initiation + streamed PUT, no 90s timeout) so multi-GB .aab files work; the
  PUT itself is never retried after a server response to avoid duplicating
  artifacts (regression-tested).
- Edit commits that fail clean up the edit so the account-wide edit lock is
  released.
- Markdown tables escape `|` in review/listing/product text.

## Development

```
go build ./mcp-androidpublisher     # from the repo root
go test ./mcp-androidpublisher      # edit lifecycle (commit/validate/cleanup/409), resumable
                                    # upload, media upload no-retry, arg validation, renders,
                                    # package memory, decoders
```

## Notes

- The server requests the full `androidpublisher` scope — it can publish
  releases and change store content. Destructive tools (`play_iap_delete`,
  `play_images_delete` without an id) should be confirmed with the user by
  the calling agent.
- Tokens live at `<user config dir>/mcp-androidpublisher/token.json`
  (macOS: `~/Library/Application Support/mcp-androidpublisher/token.json`).
- The last used package name is remembered at
  `<user config dir>/mcp-androidpublisher/last_package.txt`.
- Revoke access any time at <https://myaccount.google.com/permissions>.
- API reference: <https://developers.google.com/android-publisher>
