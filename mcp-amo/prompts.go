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
		Name:        "amo_addon_review",
		Description: "Review a Firefox add-on on addons.mozilla.org: status, versions awaiting review, the listing page, user ratings, and a to-do list.",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "addon",
				Title:       "Add-on",
				Description: "Slug, id, guid or AMO URL. Omit to use the most recent add-on.",
				Required:    false,
			},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		addon := ""
		if v := req.Params.Arguments["addon"]; v != "" {
			addon = fmt.Sprintf("\nPass \"addon\": %q to every tool below.", v)
		}
		text := fmt.Sprintf(`Review my Firefox add-on on AMO (read-only through the amo_* tools: do NOT edit, upload, submit, delete or roll back anything).%[1]s
Step 1 — amo_addon: status, channel, current version, daily users, weekly downloads, rating and star distribution, categories, tags, screenshots, EULA and privacy policy.
Step 2 — amo_versions {"pageSize": 10}: versions awaiting review (unreviewed), rejected or disabled, unlisted; amo_version for the latest one (permissions, data collection, release notes, source code).
Step 3 — amo_ratings {"withText": true, "pageSize": 50} and amo_ratings {"score": [1, 2], "pageSize": 30}.
Step 4 — amo_search {"query": "<the add-on's main topic>", "type": "extension", "sort": "users", "pageSize": 10}: competitors on the same topic.

Step 5 — Write the report in the language I am using in this conversation:
1. **Release**: the live version, versions awaiting review or rejected, problems (add-on disabled, missing source code…) — top priority if any.
2. **Listing**: whether name, summary and description cover the needed languages; missing categories, tags, screenshots, homepage or support links; comparison with competitors.
3. **Ratings**: average, star distribution, recurring complaints (grouped by topic, short quotes), bad ratings without a reply.
4. **Permissions**: whether permissions and host permissions are broader than needed (this affects install rate and review).
5. **To do**: 5-7 concrete actions by priority.

If a tool returns "not configured", tell me to run mcp-amo setup (the steps are in the error).`, addon)

		return &mcp.GetPromptResult{
			Description: "Firefox add-on review on AMO",
			Messages: []*mcp.PromptMessage{
				{Role: "user", Content: &mcp.TextContent{Text: text}},
			},
		}, nil
	})
}
