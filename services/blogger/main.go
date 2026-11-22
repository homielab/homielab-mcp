// Package blogger is the mcp-homielab service exposing the Blogger API v3 as tools: blogs,
// posts and pages (read, create, edit, publish, schedule, revert to draft,
// delete), comment moderation and page view counts.
package blogger

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/googleauth"
	"github.com/homielab/homielab-mcp/internal/mcpkit"
)

var auth = &googleauth.Provider{
	Name:       "blogger",
	Title:      "Blogger",
	Scope:      "https://www.googleapis.com/auth/blogger",
	EnvPrefix:  "BLOGGER_MCP",
	ToolPrefix: "blogger",
	APIName:    "Blogger API v3",
	Account:    "the Google account that is an author or admin of the blogs",
}

const serverInstructions = `Blogger server — reads and WRITES your blogs: posts, pages, comments, page views.

- If a tool says "not authenticated", call blogger_login once; the user approves
  in the browser, then check blogger_login_status.
- blogId may be omitted when the account has one blog, or given as the blog URL
  or host ("myblog.blogspot.com"); blogger_list_blogs returns the ids.
- postId / pageId also accept the post's or page's URL.
- New posts and pages are drafts unless isDraft:false. content is HTML (or pass
  contentFile, a local .html file). blogger_post_status publishes, schedules
  (publishDate in the future) or reverts a post to draft.
- blogger_update_post changes only the fields you pass; labels replaces the
  whole label list.
- Destructive: blogger_delete_post / blogger_delete_page (useTrash:false deletes
  permanently) and blogger_moderate_comment with action delete or
  remove_content — confirm with the user first. Publishing makes content public:
  confirm before publishing unless the user asked for it.`

// Service describes the blogger MCP server.
func Service() mcpkit.Server {
	return mcpkit.Server{
		Auth:         auth,
		Summary:      "MCP server for the Blogger API v3 (read-write)",
		Instructions: serverInstructions,
		Register: func(s *mcp.Server) {
			registerTools(s)
			registerPrompts(s)
		},
	}
}
