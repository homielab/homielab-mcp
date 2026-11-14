package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/mcpkit"
	"github.com/homielab/homielab-mcp/internal/render"
)

type emptyArgs = mcpkit.EmptyArgs

func textResult(s string) (*mcp.CallToolResult, any, error) { return mcpkit.TextResult(s) }

func errResult(err error) (*mcp.CallToolResult, any, error) { return mcpkit.ErrResult(auth, err) }

// errRes is errResult as a bare result, for helpers that return early.
func errRes(err error) *mcp.CallToolResult {
	res, _, _ := errResult(err)
	return res
}

// withBlog returns an API client and the resolved blog id, or an error result.
func withBlog(ctx context.Context, blogArg string) (*bloggerClient, string, *mcp.CallToolResult) {
	c, err := newBloggerClient(ctx)
	if err != nil {
		return nil, "", errRes(err)
	}
	id, err := c.resolveBlog(ctx, blogArg)
	if err != nil {
		return nil, "", errRes(err)
	}
	return c, id, nil
}

var (
	postStatuses    = []string{"LIVE", "DRAFT", "SCHEDULED", "SOFT_TRASHED"}
	pageStatuses    = []string{"LIVE", "DRAFT", "SOFT_TRASHED"}
	commentStatuses = []string{"LIVE", "EMPTIED", "PENDING", "SPAM"}
)

func registerTools(s *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	notDestructive := false
	createLike := &mcp.ToolAnnotations{DestructiveHint: &notDestructive}
	updateLike := &mcp.ToolAnnotations{DestructiveHint: &notDestructive, IdempotentHint: true}
	deleteLike := &mcp.ToolAnnotations{IdempotentHint: true}

	mcpkit.AddLoginTools(s, auth, mcpkit.LoginTools{
		Description: "Authorize access to Blogger: opens the browser at Google's OAuth consent page and stores the refresh token locally. Non-blocking: returns immediately while approval is pending — call blogger_login_status afterwards. Required once before any other tool. Scope: blogger (read and write posts, pages and comments).",
		Authorized: func(ctx context.Context) (string, bool) {
			c, err := newBloggerClient(ctx)
			if err != nil {
				return "", false
			}
			blogs, _, err := c.listBlogs(ctx)
			if err != nil {
				return "", false
			}
			return fmt.Sprintf("Already authorized — %d blog(s) accessible. No need to log in again.", len(blogs)), true
		},
	})

	// ---- blogs ----

	mcp.AddTool(s, &mcp.Tool{
		Name:        "blogger_list_blogs",
		Title:       "List Blogs",
		Description: "List the logged-in user's blogs: id, name, URL, post and page counts, and the user's role (ADMIN / AUTHOR). Pass the id (or URL) as blogId to other tools.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		c, err := newBloggerClient(ctx)
		if err != nil {
			return errResult(err)
		}
		blogs, infos, err := c.listBlogs(ctx)
		if err != nil {
			return errResult(err)
		}
		if len(blogs) == 0 {
			return textResult("No blogs found for this Google login.")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d blog(s):\n\n| id | name | url | posts | pages | role | status |\n|---|---|---|---|---|---|---|\n", len(blogs))
		for i, bl := range blogs {
			fmt.Fprintf(&b, "| %s | %s | %s | %d | %d | %s | %s |\n", bl.ID, render.MDCell(bl.Name), bl.URL,
				bl.Posts.TotalItems, bl.Pages.TotalItems, render.OrDash(infos[i].Role), bl.Status)
		}
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "blogger_get_blog",
		Title:       "Get Blog",
		Description: "Details of one blog: name, description, URL, locale, created/updated dates, post and page counts.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BlogID string `json:"blogId,omitempty" jsonschema:"Blog id, blog URL or host (myblog.blogspot.com). Omit if the account has exactly one blog."`
	}) (*mcp.CallToolResult, any, error) {
		c, id, res := withBlog(ctx, args.BlogID)
		if res != nil {
			return res, nil, nil
		}
		bl, err := c.getBlog(ctx, id)
		if err != nil {
			return errResult(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s (id %s)\nURL: %s\nStatus: %s\n", bl.Name, bl.ID, bl.URL, bl.Status)
		if bl.Description != "" {
			fmt.Fprintf(&b, "Description: %s\n", bl.Description)
		}
		fmt.Fprintf(&b, "Locale: %s-%s\nCreated: %s\nUpdated: %s\nPosts: %d\nPages: %d\n",
			bl.Locale.Language, bl.Locale.Country, bl.Published, bl.Updated, bl.Posts.TotalItems, bl.Pages.TotalItems)
		return textResult(b.String())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "blogger_pageviews",
		Title:       "Blog Page Views",
		Description: "Page view counts of a blog for the last 7 days, last 30 days and all time (Blogger's own stats; no per-post breakdown in the API).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BlogID string `json:"blogId,omitempty" jsonschema:"Blog id, blog URL or host (myblog.blogspot.com). Omit if the account has exactly one blog."`
	}) (*mcp.CallToolResult, any, error) {
		c, id, res := withBlog(ctx, args.BlogID)
		if res != nil {
			return res, nil, nil
		}
		var pv pageviews
		q := url.Values{"range": {"7DAYS", "30DAYS", "all"}}
		if err := c.getJSON(ctx, blogPath(id)+"/pageviews", q, &pv); err != nil {
			return errResult(err)
		}
		labels := map[string]string{"SEVEN_DAYS": "Last 7 days", "THIRTY_DAYS": "Last 30 days", "ALL_TIME": "All time"}
		var b strings.Builder
		fmt.Fprintf(&b, "Page views for blog %s:\n", id)
		for _, c := range pv.Counts {
			name := labels[c.TimeRange]
			if name == "" {
				name = c.TimeRange
			}
			fmt.Fprintf(&b, "- %s: %s\n", name, c.Count)
		}
		return textResult(b.String())
	})

	// ---- posts ----

	mcp.AddTool(s, &mcp.Tool{
		Name:  "blogger_list_posts",
		Title: "List Posts",
		Description: "List posts of a blog, newest first: id, status, published date, title, labels, comment count, URL. " +
			"Filter by status (LIVE, DRAFT, SCHEDULED, SOFT_TRASHED; default LIVE), labels, and a published date range. " +
			"Paginate with pageToken. Bodies are left out unless fetchBodies is true.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BlogID      string   `json:"blogId,omitempty" jsonschema:"Blog id, blog URL or host (myblog.blogspot.com). Omit if the account has exactly one blog."`
		Status      []string `json:"status,omitempty" jsonschema:"Any of LIVE, DRAFT, SCHEDULED, SOFT_TRASHED (default LIVE)."`
		Labels      []string `json:"labels,omitempty" jsonschema:"Only posts with all of these labels."`
		StartDate   string   `json:"startDate,omitempty" jsonschema:"Published on or after: YYYY-MM-DD or RFC 3339."`
		EndDate     string   `json:"endDate,omitempty" jsonschema:"Published on or before: YYYY-MM-DD (whole day) or RFC 3339."`
		OrderBy     string   `json:"orderBy,omitempty" jsonschema:"PUBLISHED (default) or UPDATED."`
		Ascending   bool     `json:"ascending,omitempty" jsonschema:"Oldest first instead of newest first."`
		FetchBodies bool     `json:"fetchBodies,omitempty" jsonschema:"Include each post's HTML body (large)."`
		MaxResults  int      `json:"maxResults,omitempty" jsonschema:"Items per call, 1-500 (default 20)."`
		PageToken   string   `json:"pageToken,omitempty" jsonschema:"nextPageToken from the previous call, to get the next page."`
	}) (*mcp.CallToolResult, any, error) {
		q, err := listQuery(args.MaxResults, args.PageToken, args.FetchBodies)
		if err != nil {
			return errResult(err)
		}
		if err := addStatuses(q, args.Status, postStatuses); err != nil {
			return errResult(err)
		}
		if len(args.Labels) > 0 {
			q.Set("labels", strings.Join(args.Labels, ","))
		}
		if err := addDates(q, args.StartDate, args.EndDate); err != nil {
			return errResult(err)
		}
		if args.OrderBy != "" {
			o, err := enumArg("orderBy", args.OrderBy, "PUBLISHED", "UPDATED")
			if err != nil {
				return errResult(err)
			}
			q.Set("orderBy", o)
		}
		if args.Ascending {
			q.Set("sortOption", "ASCENDING")
		}
		c, id, res := withBlog(ctx, args.BlogID)
		if res != nil {
			return res, nil, nil
		}
		var out listPage[post]
		if err := c.getJSON(ctx, blogPath(id)+"/posts", q, &out); err != nil {
			return errResult(err)
		}
		return textResult(renderPosts(out, args.FetchBodies))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "blogger_search_posts",
		Title:       "Search Posts",
		Description: "Full-text search of a blog's published posts. Returns id, date, title, labels and URL per match.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BlogID      string `json:"blogId,omitempty" jsonschema:"Blog id, blog URL or host (myblog.blogspot.com). Omit if the account has exactly one blog."`
		Query       string `json:"query" jsonschema:"Search words."`
		OrderBy     string `json:"orderBy,omitempty" jsonschema:"PUBLISHED (default) or UPDATED."`
		FetchBodies bool   `json:"fetchBodies,omitempty" jsonschema:"Include each post's HTML body (large)."`
	}) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.Query) == "" {
			return errResult(fmt.Errorf("query is required"))
		}
		q := url.Values{"q": {args.Query}, "fetchBodies": {strconv.FormatBool(args.FetchBodies)}}
		if args.OrderBy != "" {
			o, err := enumArg("orderBy", args.OrderBy, "PUBLISHED", "UPDATED")
			if err != nil {
				return errResult(err)
			}
			q.Set("orderBy", o)
		}
		c, id, res := withBlog(ctx, args.BlogID)
		if res != nil {
			return res, nil, nil
		}
		var out listPage[post]
		if err := c.getJSON(ctx, blogPath(id)+"/posts/search", q, &out); err != nil {
			return errResult(err)
		}
		return textResult(renderPosts(out, args.FetchBodies))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "blogger_get_post",
		Title:       "Get Post",
		Description: "One post with metadata and its HTML body. Drafts and scheduled posts need the numeric postId (a URL only finds published posts).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BlogID      string `json:"blogId,omitempty" jsonschema:"Blog id, blog URL or host (myblog.blogspot.com). Omit if the account has exactly one blog."`
		PostID      string `json:"postId" jsonschema:"Post id, or the post's URL."`
		WithoutBody bool   `json:"withoutBody,omitempty" jsonschema:"Metadata only, no HTML body."`
	}) (*mcp.CallToolResult, any, error) {
		c, id, res := withBlog(ctx, args.BlogID)
		if res != nil {
			return res, nil, nil
		}
		pid, err := c.resolvePost(ctx, id, args.PostID)
		if err != nil {
			return errResult(err)
		}
		p, err := c.getPost(ctx, id, pid, url.Values{"view": {"AUTHOR"}, "fetchBody": {strconv.FormatBool(!args.WithoutBody)}})
		if err != nil {
			return errResult(err)
		}
		return textResult(renderPost(p, !args.WithoutBody))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "blogger_create_post",
		Title: "Create Post",
		Description: "WRITE: create a post. It is a DRAFT unless isDraft is false (publishes now, publicly). " +
			"publishDate (future RFC 3339 time) schedules it instead. Body is HTML from content or contentFile.",
		Annotations: createLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BlogID      string   `json:"blogId,omitempty" jsonschema:"Blog id, blog URL or host (myblog.blogspot.com). Omit if the account has exactly one blog."`
		Title       string   `json:"title" jsonschema:"Post title."`
		Content     string   `json:"content,omitempty" jsonschema:"Body as HTML (Blogger stores HTML; convert Markdown first)."`
		ContentFile string   `json:"contentFile,omitempty" jsonschema:"Local path of an .html file to use as the body, instead of content."`
		Labels      []string `json:"labels,omitempty" jsonschema:"Labels (tags) of the post."`
		IsDraft     *bool    `json:"isDraft,omitempty" jsonschema:"Default true. false publishes the post immediately."`
		PublishDate string   `json:"publishDate,omitempty" jsonschema:"Schedule publication at this RFC 3339 time (e.g. 2026-11-05T08:00:00+07:00); ignores isDraft."`
	}) (*mcp.CallToolResult, any, error) {
		body, err := contentArg(args.Content, args.ContentFile)
		if err != nil {
			return errResult(err)
		}
		if strings.TrimSpace(args.Title) == "" {
			return errResult(fmt.Errorf("title is required"))
		}
		publishDate, err := publishTime(args.PublishDate)
		if err != nil {
			return errResult(err)
		}
		draft := args.IsDraft == nil || *args.IsDraft || publishDate != ""
		c, id, res := withBlog(ctx, args.BlogID)
		if res != nil {
			return res, nil, nil
		}
		in := map[string]any{"title": args.Title, "content": body}
		if len(args.Labels) > 0 {
			in["labels"] = args.Labels
		}
		var p post
		if err := c.send(ctx, http.MethodPost, blogPath(id)+"/posts", url.Values{"isDraft": {strconv.FormatBool(draft)}, "fetchBody": {"false"}}, in, &p); err != nil {
			return errResult(err)
		}
		if publishDate != "" {
			if err := c.send(ctx, http.MethodPost, blogPath(id)+"/posts/"+p.ID+"/publish", url.Values{"publishDate": {publishDate}}, nil, &p); err != nil {
				return errResult(fmt.Errorf("created draft %s but scheduling failed: %w", p.ID, err))
			}
		}
		return textResult("Created post.\n" + renderPost(p, false))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "blogger_update_post",
		Title: "Update Post",
		Description: "WRITE: change a post's title, body or labels; only the fields you pass change (labels replaces the whole list, [] clears it). " +
			"A published post changes publicly at once. Status stays as is — use blogger_post_status to publish or revert.",
		Annotations: updateLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BlogID      string    `json:"blogId,omitempty" jsonschema:"Blog id, blog URL or host (myblog.blogspot.com). Omit if the account has exactly one blog."`
		PostID      string    `json:"postId" jsonschema:"Post id, or the post's URL."`
		Title       string    `json:"title,omitempty" jsonschema:"New title."`
		Content     string    `json:"content,omitempty" jsonschema:"New body as HTML."`
		ContentFile string    `json:"contentFile,omitempty" jsonschema:"Local path of an .html file to use as the new body, instead of content."`
		Labels      *[]string `json:"labels,omitempty" jsonschema:"New full label list; [] removes all labels."`
	}) (*mcp.CallToolResult, any, error) {
		patch, err := patchBody(args.Title, args.Content, args.ContentFile, args.Labels)
		if err != nil {
			return errResult(err)
		}
		c, id, res := withBlog(ctx, args.BlogID)
		if res != nil {
			return res, nil, nil
		}
		pid, err := c.resolvePost(ctx, id, args.PostID)
		if err != nil {
			return errResult(err)
		}
		var p post
		if err := c.send(ctx, http.MethodPatch, blogPath(id)+"/posts/"+pid, url.Values{"fetchBody": {"false"}}, patch, &p); err != nil {
			return errResult(err)
		}
		return textResult("Updated post.\n" + renderPost(p, false))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "blogger_post_status",
		Title: "Publish, Schedule or Revert Post",
		Description: "WRITE: action publish makes a draft public now, or schedules it when publishDate is in the future; " +
			"action draft reverts a published or scheduled post to draft (it disappears from the blog).",
		Annotations: updateLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BlogID      string `json:"blogId,omitempty" jsonschema:"Blog id, blog URL or host (myblog.blogspot.com). Omit if the account has exactly one blog."`
		PostID      string `json:"postId" jsonschema:"Post id (drafts have no public URL)."`
		Action      string `json:"action" jsonschema:"publish or draft."`
		PublishDate string `json:"publishDate,omitempty" jsonschema:"publish only: RFC 3339 time; future = scheduled, past = backdated."`
	}) (*mcp.CallToolResult, any, error) {
		action := strings.ToLower(strings.TrimSpace(args.Action))
		q := url.Values{}
		switch action {
		case "publish":
			d, err := publishTime(args.PublishDate)
			if err != nil {
				return errResult(err)
			}
			if d != "" {
				q.Set("publishDate", d)
			}
		case "draft":
			if args.PublishDate != "" {
				return errResult(fmt.Errorf("publishDate only applies to action publish"))
			}
			action = "revert"
		default:
			return errResult(fmt.Errorf("action must be publish or draft, got %q", args.Action))
		}
		c, id, res := withBlog(ctx, args.BlogID)
		if res != nil {
			return res, nil, nil
		}
		pid, err := c.resolvePost(ctx, id, args.PostID)
		if err != nil {
			return errResult(err)
		}
		var p post
		if err := c.send(ctx, http.MethodPost, blogPath(id)+"/posts/"+pid+"/"+action, q, nil, &p); err != nil {
			return errResult(err)
		}
		return textResult(renderPost(p, false))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "blogger_delete_post",
		Title: "Delete Post",
		Description: "DESTRUCTIVE: delete a post. Default moves it to trash (status SOFT_TRASHED, restorable in the Blogger UI); " +
			"useTrash:false deletes it permanently. Confirm with the user first.",
		Annotations: deleteLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BlogID   string `json:"blogId,omitempty" jsonschema:"Blog id, blog URL or host (myblog.blogspot.com). Omit if the account has exactly one blog."`
		PostID   string `json:"postId" jsonschema:"Post id, or the post's URL."`
		UseTrash *bool  `json:"useTrash,omitempty" jsonschema:"Default true (restorable). false deletes permanently."`
	}) (*mcp.CallToolResult, any, error) {
		trash := args.UseTrash == nil || *args.UseTrash
		c, id, res := withBlog(ctx, args.BlogID)
		if res != nil {
			return res, nil, nil
		}
		pid, err := c.resolvePost(ctx, id, args.PostID)
		if err != nil {
			return errResult(err)
		}
		if err := c.send(ctx, http.MethodDelete, blogPath(id)+"/posts/"+pid, url.Values{"useTrash": {strconv.FormatBool(trash)}}, nil, nil); err != nil {
			return errResult(err)
		}
		if trash {
			return textResult("Moved post " + pid + " to trash.")
		}
		return textResult("Deleted post " + pid + " permanently.")
	})

	// ---- pages ----

	mcp.AddTool(s, &mcp.Tool{
		Name:        "blogger_list_pages",
		Title:       "List Pages",
		Description: "List a blog's static pages (About, Contact, …): id, status, dates, title, URL. Filter by status (LIVE, DRAFT, SOFT_TRASHED; default LIVE).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BlogID      string   `json:"blogId,omitempty" jsonschema:"Blog id, blog URL or host (myblog.blogspot.com). Omit if the account has exactly one blog."`
		Status      []string `json:"status,omitempty" jsonschema:"Any of LIVE, DRAFT, SOFT_TRASHED (default LIVE)."`
		FetchBodies bool     `json:"fetchBodies,omitempty" jsonschema:"Include each page's HTML body."`
		MaxResults  int      `json:"maxResults,omitempty" jsonschema:"Items per call, 1-500 (default 20)."`
		PageToken   string   `json:"pageToken,omitempty" jsonschema:"nextPageToken from the previous call, to get the next page."`
	}) (*mcp.CallToolResult, any, error) {
		q, err := listQuery(args.MaxResults, args.PageToken, args.FetchBodies)
		if err != nil {
			return errResult(err)
		}
		if err := addStatuses(q, args.Status, pageStatuses); err != nil {
			return errResult(err)
		}
		c, id, res := withBlog(ctx, args.BlogID)
		if res != nil {
			return res, nil, nil
		}
		var out listPage[page]
		if err := c.getJSON(ctx, blogPath(id)+"/pages", q, &out); err != nil {
			return errResult(err)
		}
		return textResult(renderPages(out, args.FetchBodies))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "blogger_get_page",
		Title:       "Get Page",
		Description: "One static page with metadata and its HTML body.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BlogID string `json:"blogId,omitempty" jsonschema:"Blog id, blog URL or host (myblog.blogspot.com). Omit if the account has exactly one blog."`
		PageID string `json:"pageId" jsonschema:"Page id, or the page's URL (…/p/name.html)."`
	}) (*mcp.CallToolResult, any, error) {
		c, id, res := withBlog(ctx, args.BlogID)
		if res != nil {
			return res, nil, nil
		}
		pid, err := c.resolvePage(ctx, id, args.PageID)
		if err != nil {
			return errResult(err)
		}
		p, err := c.getPage(ctx, id, pid, url.Values{"view": {"AUTHOR"}})
		if err != nil {
			return errResult(err)
		}
		return textResult(renderPage(p, true))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "blogger_create_page",
		Title:       "Create Page",
		Description: "WRITE: create a static page. It is a DRAFT unless isDraft is false (publishes now, publicly). Body is HTML from content or contentFile.",
		Annotations: createLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BlogID      string `json:"blogId,omitempty" jsonschema:"Blog id, blog URL or host (myblog.blogspot.com). Omit if the account has exactly one blog."`
		Title       string `json:"title" jsonschema:"Page title."`
		Content     string `json:"content,omitempty" jsonschema:"Body as HTML (Blogger stores HTML; convert Markdown first)."`
		ContentFile string `json:"contentFile,omitempty" jsonschema:"Local path of an .html file to use as the body, instead of content."`
		IsDraft     *bool  `json:"isDraft,omitempty" jsonschema:"Default true. false publishes the page immediately."`
	}) (*mcp.CallToolResult, any, error) {
		body, err := contentArg(args.Content, args.ContentFile)
		if err != nil {
			return errResult(err)
		}
		if strings.TrimSpace(args.Title) == "" {
			return errResult(fmt.Errorf("title is required"))
		}
		draft := args.IsDraft == nil || *args.IsDraft
		c, id, res := withBlog(ctx, args.BlogID)
		if res != nil {
			return res, nil, nil
		}
		var p page
		in := map[string]any{"title": args.Title, "content": body}
		if err := c.send(ctx, http.MethodPost, blogPath(id)+"/pages", url.Values{"isDraft": {strconv.FormatBool(draft)}}, in, &p); err != nil {
			return errResult(err)
		}
		return textResult("Created page.\n" + renderPage(p, false))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "blogger_update_page",
		Title:       "Update Page",
		Description: "WRITE: change a page's title or body; only the fields you pass change. A published page changes publicly at once.",
		Annotations: updateLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BlogID      string `json:"blogId,omitempty" jsonschema:"Blog id, blog URL or host (myblog.blogspot.com). Omit if the account has exactly one blog."`
		PageID      string `json:"pageId" jsonschema:"Page id, or the page's URL (…/p/name.html)."`
		Title       string `json:"title,omitempty" jsonschema:"New title."`
		Content     string `json:"content,omitempty" jsonschema:"New body as HTML."`
		ContentFile string `json:"contentFile,omitempty" jsonschema:"Local path of an .html file to use as the new body, instead of content."`
	}) (*mcp.CallToolResult, any, error) {
		patch, err := patchBody(args.Title, args.Content, args.ContentFile, nil)
		if err != nil {
			return errResult(err)
		}
		c, id, res := withBlog(ctx, args.BlogID)
		if res != nil {
			return res, nil, nil
		}
		pid, err := c.resolvePage(ctx, id, args.PageID)
		if err != nil {
			return errResult(err)
		}
		var p page
		if err := c.send(ctx, http.MethodPatch, blogPath(id)+"/pages/"+pid, nil, patch, &p); err != nil {
			return errResult(err)
		}
		return textResult("Updated page.\n" + renderPage(p, false))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "blogger_page_status",
		Title:       "Publish or Revert Page",
		Description: "WRITE: action publish makes a draft page public; action draft reverts a published page to draft.",
		Annotations: updateLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BlogID string `json:"blogId,omitempty" jsonschema:"Blog id, blog URL or host (myblog.blogspot.com). Omit if the account has exactly one blog."`
		PageID string `json:"pageId" jsonschema:"Page id, or the page's URL (…/p/name.html)."`
		Action string `json:"action" jsonschema:"publish or draft."`
	}) (*mcp.CallToolResult, any, error) {
		action := strings.ToLower(strings.TrimSpace(args.Action))
		switch action {
		case "publish":
		case "draft":
			action = "revert"
		default:
			return errResult(fmt.Errorf("action must be publish or draft, got %q", args.Action))
		}
		c, id, res := withBlog(ctx, args.BlogID)
		if res != nil {
			return res, nil, nil
		}
		pid, err := c.resolvePage(ctx, id, args.PageID)
		if err != nil {
			return errResult(err)
		}
		var p page
		if err := c.send(ctx, http.MethodPost, blogPath(id)+"/pages/"+pid+"/"+action, nil, nil, &p); err != nil {
			return errResult(err)
		}
		return textResult(renderPage(p, false))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "blogger_delete_page",
		Title: "Delete Page",
		Description: "DESTRUCTIVE: delete a page. Default moves it to trash (restorable in the Blogger UI); useTrash:false deletes it permanently. " +
			"Confirm with the user first.",
		Annotations: deleteLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BlogID   string `json:"blogId,omitempty" jsonschema:"Blog id, blog URL or host (myblog.blogspot.com). Omit if the account has exactly one blog."`
		PageID   string `json:"pageId" jsonschema:"Page id, or the page's URL (…/p/name.html)."`
		UseTrash *bool  `json:"useTrash,omitempty" jsonschema:"Default true (restorable). false deletes permanently."`
	}) (*mcp.CallToolResult, any, error) {
		trash := args.UseTrash == nil || *args.UseTrash
		c, id, res := withBlog(ctx, args.BlogID)
		if res != nil {
			return res, nil, nil
		}
		pid, err := c.resolvePage(ctx, id, args.PageID)
		if err != nil {
			return errResult(err)
		}
		if err := c.send(ctx, http.MethodDelete, blogPath(id)+"/pages/"+pid, url.Values{"useTrash": {strconv.FormatBool(trash)}}, nil, nil); err != nil {
			return errResult(err)
		}
		if trash {
			return textResult("Moved page " + pid + " to trash.")
		}
		return textResult("Deleted page " + pid + " permanently.")
	})

	// ---- comments ----

	mcp.AddTool(s, &mcp.Tool{
		Name:  "blogger_list_comments",
		Title: "List Comments",
		Description: "List comments of one post (postId) or of the whole blog, newest first: id, post id, status, date, author, text. " +
			"Filter by status (LIVE, PENDING = awaiting moderation, SPAM, EMPTIED; default LIVE) and date range.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BlogID     string   `json:"blogId,omitempty" jsonschema:"Blog id, blog URL or host (myblog.blogspot.com). Omit if the account has exactly one blog."`
		PostID     string   `json:"postId,omitempty" jsonschema:"Post id or URL; omit for comments across the whole blog."`
		Status     []string `json:"status,omitempty" jsonschema:"Any of LIVE, PENDING, SPAM, EMPTIED (default LIVE)."`
		StartDate  string   `json:"startDate,omitempty" jsonschema:"Posted on or after: YYYY-MM-DD or RFC 3339."`
		EndDate    string   `json:"endDate,omitempty" jsonschema:"Posted on or before: YYYY-MM-DD (whole day) or RFC 3339."`
		MaxResults int      `json:"maxResults,omitempty" jsonschema:"Items per call, 1-500 (default 20)."`
		PageToken  string   `json:"pageToken,omitempty" jsonschema:"nextPageToken from the previous call, to get the next page."`
	}) (*mcp.CallToolResult, any, error) {
		q, err := listQuery(args.MaxResults, args.PageToken, true)
		if err != nil {
			return errResult(err)
		}
		if err := addStatuses(q, args.Status, commentStatuses); err != nil {
			return errResult(err)
		}
		if err := addDates(q, args.StartDate, args.EndDate); err != nil {
			return errResult(err)
		}
		c, id, res := withBlog(ctx, args.BlogID)
		if res != nil {
			return res, nil, nil
		}
		path := blogPath(id) + "/comments"
		if args.PostID != "" {
			pid, err := c.resolvePost(ctx, id, args.PostID)
			if err != nil {
				return errResult(err)
			}
			path = blogPath(id) + "/posts/" + pid + "/comments"
		} else {
			q.Del("view") // comments.listByBlog takes no view
		}
		var out listPage[comment]
		if err := c.getJSON(ctx, path, q, &out); err != nil {
			return errResult(err)
		}
		return textResult(renderComments(out))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "blogger_moderate_comment",
		Title: "Moderate Comment",
		Description: "WRITE: moderate one comment. approve = publish a PENDING comment; spam = mark as spam (hidden); " +
			"remove_content = blank its text but keep the thread (DESTRUCTIVE); delete = remove it permanently (DESTRUCTIVE). " +
			"Confirm destructive actions with the user first.",
		Annotations: deleteLike,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		BlogID    string `json:"blogId,omitempty" jsonschema:"Blog id, blog URL or host (myblog.blogspot.com). Omit if the account has exactly one blog."`
		PostID    string `json:"postId" jsonschema:"Id or URL of the post the comment belongs to (shown by blogger_list_comments)."`
		CommentID string `json:"commentId" jsonschema:"Comment id."`
		Action    string `json:"action" jsonschema:"approve, spam, remove_content or delete."`
	}) (*mcp.CallToolResult, any, error) {
		method, suffix := http.MethodPost, ""
		switch strings.ToLower(strings.TrimSpace(args.Action)) {
		case "approve":
			suffix = "/approve"
		case "spam":
			suffix = "/spam"
		case "remove_content":
			suffix = "/removecontent"
		case "delete":
			method = http.MethodDelete
		default:
			return errResult(fmt.Errorf("action must be approve, spam, remove_content or delete, got %q", args.Action))
		}
		if !isID(args.CommentID) {
			return errResult(fmt.Errorf("commentId must be a numeric id, got %q", args.CommentID))
		}
		c, id, res := withBlog(ctx, args.BlogID)
		if res != nil {
			return res, nil, nil
		}
		pid, err := c.resolvePost(ctx, id, args.PostID)
		if err != nil {
			return errResult(err)
		}
		path := blogPath(id) + "/posts/" + pid + "/comments/" + args.CommentID + suffix
		if err := c.send(ctx, method, path, nil, nil, nil); err != nil {
			return errResult(err)
		}
		return textResult(fmt.Sprintf("Comment %s: %s done.", args.CommentID, strings.ToLower(args.Action)))
	})
}

// ---- argument helpers ----

func listQuery(maxResults int, pageToken string, fetchBodies bool) (url.Values, error) {
	if maxResults == 0 {
		maxResults = 20
	}
	if maxResults < 1 || maxResults > 500 {
		return nil, fmt.Errorf("maxResults must be 1-500, got %d", maxResults)
	}
	q := url.Values{"maxResults": {strconv.Itoa(maxResults)}, "fetchBodies": {strconv.FormatBool(fetchBodies)}}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	return q, nil
}

// addStatuses validates statuses into q. Any status other than LIVE needs
// an author/admin view, so it also sets view=ADMIN.
func addStatuses(q url.Values, statuses, allowed []string) error {
	for _, s := range statuses {
		v, err := enumArg("status", s, allowed...)
		if err != nil {
			return err
		}
		q.Add("status", v)
		if v != "LIVE" {
			q.Set("view", "ADMIN")
		}
	}
	return nil
}

func addDates(q url.Values, start, end string) error {
	s, err := apiTime(start, false)
	if err != nil {
		return err
	}
	e, err := apiTime(end, true)
	if err != nil {
		return err
	}
	if s != "" {
		q.Set("startDate", s)
	}
	if e != "" {
		q.Set("endDate", e)
	}
	return nil
}

// publishTime validates an RFC 3339 publish time (empty allowed).
func publishTime(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if _, err := apiTime(s, false); err != nil || len(s) == 10 {
		return "", fmt.Errorf("publishDate %q must be RFC 3339 with time and zone, e.g. 2026-11-05T08:00:00+07:00", s)
	}
	return s, nil
}

// patchBody builds a PATCH body from the fields the caller set.
func patchBody(title, content, contentFile string, labels *[]string) (map[string]any, error) {
	body, err := contentArg(content, contentFile)
	if err != nil {
		return nil, err
	}
	patch := map[string]any{}
	if title != "" {
		patch["title"] = title
	}
	if body != "" {
		patch["content"] = body
	}
	if labels != nil {
		patch["labels"] = *labels
		if len(*labels) == 0 {
			patch["labels"] = []string{}
		}
	}
	if len(patch) == 0 {
		return nil, fmt.Errorf("nothing to update: pass title, content, contentFile or labels")
	}
	return patch, nil
}

// ---- rendering ----

func renderPosts(out listPage[post], bodies bool) string {
	if len(out.Items) == 0 {
		return "No posts found."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d post(s):\n\n| id | status | published | title | labels | comments | url |\n|---|---|---|---|---|---|---|\n", len(out.Items))
	for _, p := range out.Items {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s |\n", p.ID, render.OrDash(p.Status), render.OrDash(p.Published),
			render.MDCell(p.Title), render.MDCell(strings.Join(p.Labels, ", ")), render.OrDash(p.Replies.TotalItems), render.OrDash(p.URL))
	}
	if bodies {
		for _, p := range out.Items {
			fmt.Fprintf(&b, "\n---\n## %s (%s)\n%s\n", p.Title, p.ID, p.Content)
		}
	}
	if out.NextPageToken != "" {
		fmt.Fprintf(&b, "\nMore results: pageToken=%s\n", out.NextPageToken)
	}
	return b.String()
}

func renderPost(p post, body bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Post %s: %s\nStatus: %s\nURL: %s\nPublished: %s\nUpdated: %s\n",
		p.ID, p.Title, p.Status, render.OrDash(p.URL), render.OrDash(p.Published), render.OrDash(p.Updated))
	if p.Author.DisplayName != "" {
		fmt.Fprintf(&b, "Author: %s\n", p.Author.DisplayName)
	}
	if len(p.Labels) > 0 {
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(p.Labels, ", "))
	}
	if p.Replies.TotalItems != "" {
		fmt.Fprintf(&b, "Comments: %s\n", p.Replies.TotalItems)
	}
	if body {
		fmt.Fprintf(&b, "\nContent (HTML):\n%s\n", p.Content)
	}
	return b.String()
}

func renderPages(out listPage[page], bodies bool) string {
	if len(out.Items) == 0 {
		return "No pages found."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d page(s):\n\n| id | status | published | updated | title | url |\n|---|---|---|---|---|---|\n", len(out.Items))
	for _, p := range out.Items {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", p.ID, render.OrDash(p.Status), render.OrDash(p.Published),
			render.OrDash(p.Updated), render.MDCell(p.Title), render.OrDash(p.URL))
	}
	if bodies {
		for _, p := range out.Items {
			fmt.Fprintf(&b, "\n---\n## %s (%s)\n%s\n", p.Title, p.ID, p.Content)
		}
	}
	if out.NextPageToken != "" {
		fmt.Fprintf(&b, "\nMore results: pageToken=%s\n", out.NextPageToken)
	}
	return b.String()
}

func renderPage(p page, body bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Page %s: %s\nStatus: %s\nURL: %s\nPublished: %s\nUpdated: %s\n",
		p.ID, p.Title, p.Status, render.OrDash(p.URL), render.OrDash(p.Published), render.OrDash(p.Updated))
	if body {
		fmt.Fprintf(&b, "\nContent (HTML):\n%s\n", p.Content)
	}
	return b.String()
}

func renderComments(out listPage[comment]) string {
	if len(out.Items) == 0 {
		return "No comments found."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d comment(s):\n\n| id | post | status | published | author | reply to | text |\n|---|---|---|---|---|---|---|\n", len(out.Items))
	for _, c := range out.Items {
		reply := "—"
		if c.InReplyTo != nil && c.InReplyTo.ID != "" {
			reply = c.InReplyTo.ID
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s |\n", c.ID, c.Post.ID, render.OrDash(c.Status), c.Published,
			render.MDCell(c.Author.DisplayName), reply, render.MDCell(c.Content))
	}
	if out.NextPageToken != "" {
		fmt.Fprintf(&b, "\nMore results: pageToken=%s\n", out.NextPageToken)
	}
	return b.String()
}
