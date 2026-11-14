package main

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerPrompts adds MCP prompts — reusable templates the client can
// surface to the user and that expand into a full instruction for the agent.
func registerPrompts(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "blogger_blog_review",
		Description: "Review a Blogger blog: page views, posting rhythm, labels, stale drafts, pending and spam comments, and a to-do list.",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "blog",
				Title:       "Blog",
				Description: "Blog id, URL or host (myblog.blogspot.com). Omit if there is only one blog.",
				Required:    false,
			},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		blog := ""
		if v := req.Params.Arguments["blog"]; v != "" {
			blog = fmt.Sprintf("\nPass \"blogId\": %q to every tool below.", v)
		}
		text := fmt.Sprintf(`Review my Blogger blog (read it through the blogger_* tools: do NOT edit, delete or publish anything).%[1]s
Step 1 — blogger_list_blogs to pick the right blog; blogger_get_blog.
Step 2 — blogger_pageviews: views for 7 days, 30 days and all time.
Step 3 — Posts:
1. blogger_list_posts {"maxResults": 100} — posting rhythm, labels, comment counts
2. blogger_list_posts {"status": ["DRAFT", "SCHEDULED"]} — stale drafts, scheduled posts
3. blogger_list_pages — static pages (About, Contact, Policy…)
Step 4 — Comments: blogger_list_comments {"status": ["PENDING"]} and {"status": ["SPAM"]}.

Step 5 — Write the report in the language I am using in this conversation:
1. **Overview**: page views, number of posts, recent posting frequency.
2. **Content**: most and least used labels, labels with the same meaning to merge, posts without labels.
3. **Backlog**: old drafts to finish or drop, scheduled posts.
4. **Static pages**: which ones are missing (About, Contact, Privacy policy).
5. **Comments**: pending and spam counts; suggest what to approve or delete (do not do it).
6. **To do**: 5-7 concrete actions by priority.

If a tool returns "not authenticated", call blogger_login, wait for me to approve, then call blogger_login_status.`, blog)

		return &mcp.GetPromptResult{
			Description: "Blogger blog review",
			Messages: []*mcp.PromptMessage{
				{Role: "user", Content: &mcp.TextContent{Text: text}},
			},
		}, nil
	})
}
