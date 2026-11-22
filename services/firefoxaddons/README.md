# Firefox Add-ons

MCP server (Go) for the **addons.mozilla.org (AMO) API v5**: the developer side of Firefox add-ons.
It reads and **writes** your add-ons: listing metadata, icon, screenshots, EULA / privacy policy,
package upload and validation, version submission (with source code), version edit / disable /
delete / rollback, add-on creation and deletion, plus public search and ratings.

## Tools

| Tool | What it does |
|---|---|
| `amo_auth_status` | Server, signed-in account, site status (read-only maintenance, developer notices) |
| `amo_my_addons` | Add-ons you author: id, slug, guid, type, status, channels, version, users, downloads, rating |
| `amo_addon` | Details: names and locales, status, categories, tags, links, current / unlisted version, users, ratings per star, screenshots; `raw` for the full JSON |
| `amo_addon_edit` | **Write**: name, summary, description, developer comments, homepage, support email/URL, categories, tags, slug, default locale, contributions URL, flags, `isDisabled`; `extra` for other fields |
| `amo_addon_icon` | **Write**: upload a PNG/JPEG icon, or `clear` |
| `amo_addon_create` | **Write**: new add-on from a package (`path` or upload UUID) with listing and version fields |
| `amo_addon_delete` | **Write, permanent**: deletes all versions; the guid can never be reused. `confirm` must be the slug or guid |
| `amo_upload` | **Write**: upload an `.xpi`/`.zip` or extension folder, wait for the validator, show errors/warnings |
| `amo_upload_status` | One upload's validation (with optional wait), or your recent uploads |
| `amo_versions` | Versions with channel, file status, review date, compatibility, size |
| `amo_version` | One version: file, hash, download URL, permissions, data collection, license, release notes, reviewer notes, source |
| `amo_version_create` | **Write, goes to review**: `path` (upload + validate) or upload UUID, release notes, reviewer notes, license, compatibility, `sourcePath` |
| `amo_version_edit` | **Write**: release notes, reviewer notes, license, compatibility, source (`sourcePath` / `clearSource`), `isDisabled` |
| `amo_version_delete` | **Write**: delete a version (soft-blocked in Firefox) |
| `amo_version_rollback` | **Write, releases to users**: re-release an earlier approved version under a new number |
| `amo_preview_add` / `amo_preview_edit` / `amo_preview_delete` | **Write**: screenshots (image, position, caption) |
| `amo_eula_policy` / `amo_eula_policy_edit` | Read / set the EULA and privacy policy by locale |
| `amo_ratings` | User ratings with developer replies; filter by score, text, version id; per-star counts |
| `amo_search` | Public search: text, type, app, category, author, guid, tag, promoted; sort by users, downloads, rating, … |
| `amo_request` | Any other v5 endpoint (accounts, collections, authors, activity), signed with your key |

Input conveniences:

- `addon`: numeric id, slug, guid (`{…}` or `name@domain`) or AMO / Developer Hub URL. The last one
  used is remembered (`last_addon.json` in the config dir).
- `version`: version id or number (`1.2.0`). AMO reads a dotless value as an id: write `v2` for
  version "2".
- Translated fields take `{locale: text}`; locales not passed stay unchanged, `""` deletes one.
- Packages: a folder is zipped in memory, dot files (`.git`, …) left out; `manifest.json` must be at
  the root. Source code: `.zip` / `.tar.gz` / `.tgz` / `.tar.bz2`, or a folder.
- `amo_addon`, `amo_versions` (`filter: public`), `amo_version`, `amo_eula_policy`, `amo_ratings`
  and `amo_search` work without a key for public add-ons.

### Prompts

- `amo_addon_review` — read-only review of an add-on: release state, listing gaps,
  ratings and recurring complaints, permissions, competitors, and an action list.

## Setup

Needs the `mcp-homielab` binary ([install](../../README.md#install)).

1. Sign in at <https://addons.mozilla.org/developers/addon/api/key/> and generate credentials:
   a **JWT issuer** (`user:12345:67`) and a **JWT secret** (shown once).
2. Store and test them (the secret is read from stdin, so it stays out of shell history):
   ```bash
   mcp-homielab firefoxaddons setup --issuer user:12345:67
   ```
   Saved to `<user config dir>/mcp-homielab/firefoxaddons/credentials.json` (0600; macOS:
   `~/Library/Application Support/mcp-homielab/firefoxaddons/`, Linux: `~/.config/mcp-homielab/firefoxaddons/`).
3. Register: `mcp-homielab add firefoxaddons` (Claude Code, user scope), or
   `mcp-homielab add --client claude-desktop firefoxaddons` (also `cursor`; `json` prints the config).
   Manual config entry:
   `"firefoxaddons": { "command": "/path/to/mcp-homielab", "args": ["firefoxaddons"] }`.

Env vars instead of the file: `AMO_JWT_ISSUER` + `AMO_JWT_SECRET`, or web-ext's
`WEB_EXT_API_KEY` + `WEB_EXT_API_SECRET`. `AMO_API_BASE` (or `setup --api-base`) targets stage:
`https://addons.allizom.org/api/v5`.

## Example asks

- *"Ship version 1.4 of my add-on"* → `amo_version_create { "addon": "my-addon", "path": "./dist", "releaseNotes": {"en-US": "…"}, "sourcePath": "." }`,
  then `amo_version` until the file is `public`
- *"Sign a self-hosted build"* → `amo_version_create { "path": "./dist", "channel": "unlisted" }`, then download the file URL from `amo_version`
- *"Validate without submitting"* → `amo_upload { "path": "./dist" }`
- *"Update the description in Vietnamese"* → `amo_addon_edit { "description": {"vi": "…"} }`
- *"1.4 is broken, go back to 1.3"* → `amo_version_rollback { "version": "1.3", "newVersion": "1.4.1" }`
- *"What do 1-star reviews say?"* → `amo_ratings { "score": [1], "withText": true }`

## Notes

- The API has no usage statistics endpoint and replying to ratings needs a website session: use the
  AMO Developer Hub for both.
- Listed versions need the add-on's name, summary, categories (max 3) and a license; the first
  listed version also needs them on the add-on itself.
- Minified, bundled or generated code needs `sourcePath`, with build steps in `approvalNotes`.
- Rollback runs in the background; AMO emails the result. A listed version awaiting review is
  disabled by a listed rollback.
- The JWT is signed per request (HS256, 60 s lifetime): a wrong system clock gives 401.
- Revoke or regenerate keys at <https://addons.mozilla.org/developers/addon/api/key/>.

## Development

```bash
go test ./services/firefoxaddons   # credentials, JWT, add-on/version refs, error decoding, package zipping, upload → version flow against a fake server
```
