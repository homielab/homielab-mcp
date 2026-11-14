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
		Name:        "cws_item_review",
		Description: "Review an extension on the Chrome Web Store: release state, policy warnings, reviews, 30-day installs / uninstalls / users, and a to-do list.",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "item",
				Title:       "Extension",
				Description: "Extension id (32 letters) or Chrome Web Store URL. Omit to use the most recent item.",
				Required:    false,
			},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		item := ""
		if v := req.Params.Arguments["item"]; v != "" {
			item = fmt.Sprintf("\nPass \"itemId\": %q to every tool below.", v)
		}
		text := fmt.Sprintf(`Review my Chrome Web Store extension (read it through the cws_* tools: do NOT upload, publish, roll back or cancel anything).%[1]s
Step 1 — cws_status: the published version, rollout %%, versions awaiting review or rejected, policy warnings or takedowns.
Step 2 — cws_rating_summary; cws_reviews {"pageSize": 50}; cws_reviews {"orderBy": "lowest", "pageSize": 30}.
Step 3 — cws_metrics:
1. {"metrics": ["installs", "uninstalls", "users", "impressions", "page-views"]} — 30 days by day
2. {"metrics": ["users"], "dimension": "app-version", "dateRange": "LAST_7_DAYS"} — whether users have moved to the new version
3. {"metrics": ["installs"], "dimension": "country"} and {"metrics": ["installs"], "dimension": "install-source"}

Step 4 — Write the report in the language I am using in this conversation:
1. **Release**: the live version, versions awaiting review, policy problems (top priority if any).
2. **Growth**: installs, uninstalls, active users; page-views → installs conversion; trend.
3. **Reviews**: average rating, star distribution, recurring complaints (grouped by topic, short quotes).
4. **Markets**: countries and main install sources.
5. **To do**: 5-7 concrete actions by priority (fix what users complain about, improve the store page, rollout…).

If a tool reports a missing publisherId, ask me for the Publisher ID (Developer Dashboard → Publisher → Settings).
If a tool returns "not authenticated", call cws_login, wait for me to approve, then call cws_login_status.`, item)

		return &mcp.GetPromptResult{
			Description: "Chrome Web Store extension review",
			Messages: []*mcp.PromptMessage{
				{Role: "user", Content: &mcp.TextContent{Text: text}},
			},
		}, nil
	})
}
