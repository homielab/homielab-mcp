# mcp-appstoreconnect

MCP server (Go) for the **App Store Connect API**: apps, TestFlight builds /
groups / testers, App Store versions, localized metadata, App Review
submission, manual and phased release, customer reviews, in-app purchases and
subscriptions, sales and finance reports, plus raw access to every endpoint.

It **writes** to App Store Connect (submits for review, releases versions,
changes metadata, invites testers). Tools that release or delete are marked
below.

Auth is an App Store Connect **API key** (.p8) signed into a short-lived ES256
JWT per request — no browser login, no Google OAuth.

## Tools

| Tool | What it does |
|---|---|
| `asc_auth_status` | Show the configured key (key id, issuer, file, vendor number, last app) and test it |
| `asc_apps` | Apps the key can see: name, bundle id, SKU, Apple ID, locale |
| `asc_builds` | Builds, newest first: version, build number, processing state, TestFlight internal/external state, expiry |
| `asc_beta_groups` | TestFlight groups: internal/external, public link |
| `asc_beta_testers` | Testers of a group or the whole app |
| `asc_beta_tester_add` | Invite a tester by email into groups (existing testers are linked) |
| `asc_beta_tester_remove` | Remove a tester from groups; `deleteTester:true` deletes them from the team (**destructive**) |
| `asc_build_distribute` | TestFlight: export compliance, "What to Test", add to groups, submit for beta review |
| `asc_versions` | App Store versions: state, release type, attached build |
| `asc_version_create` | New version (+ release type, copyright, build) |
| `asc_version_update` | Rename, change release type / scheduled date / copyright, attach a build |
| `asc_localizations` | Metadata per locale: name, subtitle, promo text, what's new, keywords, description, URLs |
| `asc_localization_update` | Set metadata for one locale (creates it if missing); lengths checked locally |
| `asc_review_submissions` | App Review submissions and their state |
| `asc_review_submit` | Submit a version to App Review (**confirm first**) |
| `asc_review_cancel` | Withdraw a submission |
| `asc_version_release` | Release a `PENDING_DEVELOPER_RELEASE` version (**confirm first**) |
| `asc_phased_release` | 7-day phased release: status, enable, disable, pause, resume, complete |
| `asc_reviews` | Customer reviews with your responses; filter by rating, territory, unanswered |
| `asc_review_reply` | Respond to a review (`replace:true` swaps an existing response) |
| `asc_iaps` | In-app purchases: product id, type, state |
| `asc_subscriptions` | Subscription groups and subscriptions: period, level, state |
| `asc_sales_report` | Sales and Trends report (gzip TSV): units by type/app/country, proceeds by currency, preview, CSV export |
| `asc_finance_report` | Monthly financial report: preview, CSV export |
| `asc_get` | GET any API path (users, bundle ids, certificates, profiles, devices, app clips, metrics, ...) |
| `asc_request` | POST / PATCH / DELETE any API path with a JSON:API body (**destructive** for DELETE) |

Most tools take `app`: Apple ID, bundle id or app name. The last resolved app
is remembered, so later calls may omit it. Version tools default to the
editable version on `IOS`.

### Prompts

- `asc_release_status` — release-status report: live / in review /
  in preparation per platform, builds not yet attached, pending releases,
  phased-release state, recent reviews, next tool calls.
- `asc_review_triage` — review triage: topics, territories, draft
  replies for the worst unanswered reviews (sent only after approval).

## Setup

1. **Create a key**: App Store Connect → Users and Access → Integrations →
   App Store Connect API → Team Keys → generate. Role: **Admin** or **App
   Manager** for writes; **Sales** or **Finance** for reports. Download
   `AuthKey_<KEYID>.p8` (only possible once) and copy the **Issuer ID** above
   the key list. For reports, note the **vendor number** (Payments and
   Financial Reports).
2. **Store it** (copies the key into the config dir with mode 0600, writes
   `config.json`, and tests the key):
   ```bash
   ~/go/bin/mcp-appstoreconnect setup --key-file ~/Downloads/AuthKey_ABC123.p8 --issuer-id <ISSUER_ID> --vendor-number <N>
   ```
   Or set env vars on the MCP registration: `ASC_KEY_ID`, `ASC_ISSUER_ID`,
   `ASC_PRIVATE_KEY_PATH` (or `ASC_PRIVATE_KEY` with the PEM text),
   `ASC_VENDOR_NUMBER`. Individual keys work too: omit the issuer id.
3. **Register**:
   ```bash
   claude mcp add mcp-appstoreconnect "$(go env GOPATH)/bin/mcp-appstoreconnect"
   ```

Lookup order (`mcp-appstoreconnect --help`): env vars → `config.json` in
`<user config dir>/mcp-appstoreconnect/` → `AuthKey_<KEYID>.p8` in the config
dir, `./private_keys`, `~/private_keys`, `~/.private_keys`,
`~/.appstoreconnect/private_keys` (the folders `altool` and fastlane use).

## Example asks

- *"Where is the iOS release at?"* → `asc_versions`, `asc_review_submissions`;
  the `asc_release_status` prompt does the full picture.
- *"Send build 45 to the Public Beta group"* →
  `asc_build_distribute {"buildNumber": "45", "groups": ["Public Beta"], "whatToTest": "...", "usesNonExemptEncryption": false}`
- *"Prepare 1.5.0 with the latest build"* →
  `asc_version_create {"versionString": "1.5.0", "buildNumber": "latest", "releaseType": "MANUAL"}`
  then `asc_localization_update {"locale": "en-US", "whatsNew": "..."}`
- *"Submit it"* → `asc_review_submit {}`; after approval
  `asc_version_release {}`
- *"Pause the rollout"* → `asc_phased_release {"action": "pause"}`
- *"Unanswered 1–2 star reviews"* →
  `asc_reviews {"rating": [1, 2], "unanswered": true}`
- *"Yesterday's downloads"* → `asc_sales_report {}`
- *"List devices"* → `asc_get {"path": "/v1/devices"}`

## Robustness

- JWT cached and re-signed a minute before its 19-minute lifetime ends.
- Retries on 429/5xx and network errors (3 attempts, honors `Retry-After`);
  JSON:API errors decoded into code, title, detail and source pointer, with
  hints for 401 (key) and 403 (role).
- Pagination follows `links.next`; list tools cap results.
- Lengths checked before the call: name/subtitle 30, keywords 100, promo text
  170, description / what's new / What to Test 4000, review response 5970.
- Releasing checks the version is `PENDING_DEVELOPER_RELEASE`; submitting
  checks a build is attached and reuses an open review submission.

## Development

```bash
go test ./mcp-appstoreconnect   # JWT signing and verification, credential lookup, pagination,
                                # app resolution, TestFlight / review / metadata / reply flows
                                # against a fake API, gzip sales report, tool schemas
```

API reference: <https://developer.apple.com/documentation/appstoreconnectapi>
