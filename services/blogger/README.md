# Blogger

MCP server (Go) for the **Blogger API v3**. It reads and **writes** your blogs:
posts and pages (create, edit, publish, schedule, revert to draft, delete),
comment moderation, and page view counts.

## Tools

| Tool | What it does |
|---|---|
| `blogger_login` | One-time OAuth consent in your browser; **non-blocking** — call `blogger_login_status` after approving. Scope `blogger` (read-write). |
| `blogger_login_status` | Is the flow running / finished / failed? Is a token stored? |
| `blogger_logout` | Delete the stored token |
| `blogger_list_blogs` | Your blogs: id, name, URL, post/page counts, your role |
| `blogger_get_blog` | One blog: description, locale, dates, counts |
| `blogger_pageviews` | Page views for the last 7 days, 30 days and all time |
| `blogger_list_posts` | Posts by `status` (LIVE, DRAFT, SCHEDULED, SOFT_TRASHED), `labels`, date range, order; paginated |
| `blogger_search_posts` | Full-text search of published posts |
| `blogger_get_post` | One post with its HTML body |
| `blogger_create_post` | **Write**: new post, draft by default; `isDraft:false` publishes, `publishDate` schedules |
| `blogger_update_post` | **Write**: change title, body or labels (only the fields passed) |
| `blogger_post_status` | **Write**: `publish` (now, or scheduled with `publishDate`) or `draft` (revert) |
| `blogger_delete_post` | **Write, destructive**: to trash by default; `useTrash:false` deletes permanently |
| `blogger_list_pages` / `blogger_get_page` | Static pages (About, Contact, …) |
| `blogger_create_page` / `blogger_update_page` / `blogger_page_status` / `blogger_delete_page` | Same writes as for posts (pages cannot be scheduled) |
| `blogger_list_comments` | Comments of one post or the whole blog, by status (LIVE, PENDING, SPAM, EMPTIED) |
| `blogger_moderate_comment` | **Write**: `approve`, `spam`, `remove_content` or `delete` (last two destructive) |

Input conveniences:

- `blogId`: omit with one blog, or pass the blog URL or host (`myblog.blogspot.com`, custom domains too).
- `postId` / `pageId`: the numeric id or the public URL. Drafts have no URL: use the id.
- Body: `content` (HTML) or `contentFile` (path of a local .html file). Blogger stores HTML, so convert Markdown first.
- Dates: `YYYY-MM-DD` (an end date covers the whole day, UTC) or RFC 3339. `publishDate` needs a time and zone: `2026-11-05T08:00:00+07:00`.

### Prompts

- `blogger_blog_review` — read-only review of a blog: page views, posting rhythm, labels,
  stale drafts, missing static pages, pending/spam comments, and an action list.

## Setup

Needs the `mcp-homielab` binary ([install](../../README.md#install)).

1. Google Cloud Console, in the project of your Google OAuth client: *APIs & Services → Library*
   → **Blogger API v3** → **Enable**. The Desktop OAuth client of the other Google services works as is: put its
   `client_secret*.json` in `<user config dir>/mcp-homielab/` (shared by all six Google services;
   macOS: `~/Library/Application Support/mcp-homielab/`, Linux: `~/.config/mcp-homielab/`).
   `mcp-homielab blogger --help` prints the lookup order.
2. Log in with the Google account that is an author or admin of the blogs:
   ```bash
   mcp-homielab blogger login
   ```
   or call `blogger_login` in a session, then `blogger_login_status`.
   Token: `<user config dir>/mcp-homielab/blogger/token.json` (0600).
3. Register: `mcp-homielab add blogger` (Claude Code, user scope), or
   `mcp-homielab add --client claude-desktop blogger` (also `cursor`; `json` prints the config).
   Manual config entry: `"blogger": { "command": "/path/to/mcp-homielab", "args": ["blogger"] }`.

Env vars instead of a secret file: `BLOGGER_MCP_CLIENT_ID`, `BLOGGER_MCP_CLIENT_SECRET`.

## Example asks

- *"Show my drafts"* → `blogger_list_posts { "status": ["DRAFT"] }`
- *"Publish this article on Monday 8am"* →
  `blogger_create_post { "title": "...", "contentFile": "post.html", "labels": ["SEO"], "publishDate": "2026-11-02T08:00:00+07:00" }`
- *"Fix the typo in https://myblog.blogspot.com/2026/10/x.html"* → `blogger_get_post`, then
  `blogger_update_post { "postId": "<url>", "content": "..." }`
- *"Approve pending comments"* → `blogger_list_comments { "status": ["PENDING"] }`, then
  `blogger_moderate_comment { "action": "approve", ... }` per comment

## Notes

- Listing DRAFT / SCHEDULED / SOFT_TRASHED posts, or PENDING / SPAM comments, uses the admin view:
  the account must be an admin of the blog.
- The API gives page views per blog only, not per post. For search traffic use the `searchconsole` service.
- Revoke access at <https://myaccount.google.com/permissions>.

## Development

```bash
go test ./services/blogger   # blog/post/page resolution, date and enum validation, patch bodies, writes
```
