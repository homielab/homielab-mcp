package main

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerPrompts adds MCP prompts — reusable analysis templates the client
// can surface to the user and that expand into a full instruction for the agent.
func registerPrompts(s *mcp.Server) {
	appArg := []*mcp.PromptArgument{{
		Name:        "app",
		Title:       "App",
		Description: "Apple ID, bundle id or app name. Omit if the server remembers the app from an earlier call.",
	}}
	appText := func(req *mcp.GetPromptRequest) string {
		if v := req.Params.Arguments["app"]; v != "" {
			return fmt.Sprintf("%q (pass \"app\": %q to the first tool)", v, v)
		}
		return "not given — call the tools without \"app\"; the server uses the app it remembers (on error, call asc_apps and ask me to pick one)"
	}

	s.AddPrompt(&mcp.Prompt{
		Name:        "asc_release_status",
		Description: "App Store release status report: versions live / in review / being prepared, the latest build, TestFlight, phased release and recent customer reviews.",
		Arguments:   appArg,
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		text := fmt.Sprintf(`Report the App Store RELEASE STATUS of my app (read it through the asc_* tools).

App: %s

Step 1 — The release picture:
1. asc_versions — versions per platform: state, release type, attached build
2. asc_review_submissions — submissions waiting, in review or with UNRESOLVED_ISSUES
3. asc_builds (limit 10) — the latest build, processing state, TestFlight state
4. asc_phased_release (action status) — if the latest version is in a phased release

Step 2 — Find the gaps:
- Is the latest build attached to a version? If not → it was built but never sent to the App Store.
- Is a version PENDING_DEVELOPER_RELEASE (approved, waiting for the release button)?
- Is a submission UNRESOLVED_ISSUES or REJECTED and needs work?
- Is the phased release PAUSED? Which day out of 7 is it on?
- Does a TestFlight build expire within 14 days?

Step 3 — User feedback:
asc_reviews (limit 30) — recent average rating, the newest 1-2★ reviews without a reply.

Step 4 — Write the report in the language I am using in this conversation:
1. **Current state**: one line per platform (live version, version being prepared or in review, build).
2. **Unfinished work**: builds not attached to a version, versions waiting for release, blocked submissions, paused rollouts.
3. **User signals**: rating trend, notable negative reviews.
4. **Recommended actions**: at most 5, each naming the next tool to call (e.g. asc_version_release, asc_phased_release {"action": "resume"}).
If a tool returns "not configured", walk me through mcp-appstoreconnect setup using the error text.`, appText(req))
		return &mcp.GetPromptResult{
			Description: "App Store release status report",
			Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}},
		}, nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        "asc_review_triage",
		Description: "Classify recent App Store reviews by topic, draft replies to the worst unanswered ones, and propose actions.",
		Arguments:   appArg,
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		text := fmt.Sprintf(`TRIAGE the App Store REVIEWS of my app (read them through the asc_* tools).

App: %s

Step 1 — Get the data:
1. asc_reviews {"limit": 100} — the 100 newest reviews
2. asc_reviews {"rating": [1, 2], "unanswered": true, "limit": 30} — bad reviews without a reply

Step 2 — Analyse:
- Group reviews by topic (crash/bug, performance, price/IAP, ads, UX, feature request, praise) — count and average rating per group.
- By territory: where is the rating unusually low?
- Did a problem appear after the latest version (compare review dates with asc_versions)?

Step 3 — Draft replies (do NOT send them):
For at most 10 unanswered 1-2★ reviews: draft a short, polite reply in the review's language, with no promised dates. Include the reviewId.

Step 4 — Write the report in the language I am using in this conversation:
1. **Overview**: number of reviews, average rating, star distribution.
2. **Main topics**: a topic × count × rating table, with one short quote per topic.
3. **Suggested replies**: reviewId and text.
4. **To do**: at most 5 actions for the product team.
Call asc_review_reply only after I approve each reply.`, appText(req))
		return &mcp.GetPromptResult{
			Description: "App Store review triage",
			Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}},
		}, nil
	})
}
