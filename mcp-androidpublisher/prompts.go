package main

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerPrompts adds MCP prompts — reusable analysis templates the client
// can surface to the user and that expand into a full instruction for the agent.
func registerPrompts(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "play_release_status",
		Description: "Google Play release status report: tracks and the versions they serve, the latest uploaded bundle that is not on a track yet, recent reviews and refunds (voided purchases).",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "package_name",
				Title:       "Package name",
				Description: "com.example.app. Omit if the server remembers the package from an earlier call.",
				Required:    false,
			},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		pkg := "not given — call play_tracks without packageName; the server uses the package it remembers from an earlier call"
		if v := req.Params.Arguments["package_name"]; v != "" {
			pkg = v
		}
		text := fmt.Sprintf(`Report the Google Play RELEASE STATUS of my app (read it through the play_* tools).

Package: %s

Step 1 — The release picture:
1. play_tracks — every track: status, versionCodes, rollout %%, release notes
2. play_bundles — the latest uploaded version code
3. play_apks — if the app still ships APKs

Step 2 — Find the gaps:
- Is the latest bundle in any release from play_tracks? If not → the app was built but NOT distributed.
- Production track: is the rollout below 100%% (staged rollout running or halted)? Is any release halted?
- Do internal or beta hold a newer version than production (waiting to be promoted)?

Step 3 — User feedback:
1. play_reviews (maxResults 20) — recent average rating, the newest 1-2★ reviews
2. play_voided_purchases (last 7 days: startTime = 7 days ago) — unusual refunds?

Step 4 — Write the report in the language I am using in this conversation:
1. **Current state**: one line per track (version, rollout, status).
2. **Unfinished work**: bundles not on a track, stalled rollouts, halted releases.
3. **User signals**: rating trend, notable negative reviews, refunds.
4. **Recommended actions**: at most 5, each naming the next tool to call (e.g. play_track_update to raise the rollout to 50%%).
If a tool returns "not authenticated", call play_login, wait for me to approve, then call play_login_status.`, pkg)

		return &mcp.GetPromptResult{
			Description: "Google Play release status report",
			Messages: []*mcp.PromptMessage{
				{Role: "user", Content: &mcp.TextContent{Text: text}},
			},
		}, nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        "play_review_triage",
		Description: "Summarise and classify the latest Google Play reviews, draft a reply for each, and list actions by problem topic.",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "package_name",
				Title:       "Package name",
				Description: "com.example.app. Omit if the server remembers the package from an earlier call.",
				Required:    false,
			},
			{
				Name:        "max_results",
				Title:       "Number of reviews",
				Description: "How many recent reviews to analyse (default 30).",
				Required:    false,
			},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		pkg := ""
		if v := req.Params.Arguments["package_name"]; v != "" {
			pkg = fmt.Sprintf(`"packageName": %q, `, v)
		}
		n := "30"
		if v := req.Params.Arguments["max_results"]; v != "" {
			n = v
		}
		text := fmt.Sprintf(`Classify my Google Play REVIEWS and plan the replies (read them through the play_* tools).

Step 1 — play_reviews {"maxResults": %s%s} — if there is a next page, fetch at most 2 more pages.

Step 2 — Classify each review by topic (crash, performance, UI, payment, feature, other), star rating, whether it has a reply, and device / app version.

Step 3 — Write, in the language I am using in this conversation:
1. **Overview**: average rating, 1-5★ distribution, the topics that recur most.
2. **Reply first**: 1-2★ reviews without a reply, reviews with an email or contact details, and crash reports — for each, draft a reply of at most 350 characters in the review's language (short, sincere, with concrete guidance; no promises).
3. **By device and version**: devices or app versions that show up unusually often in bad reviews.
4. **To do**: 3-5 engineering or product actions drawn from the problem topics.

Step 4 — Ask me to approve, then post the approved replies with play_review_reply (one call per review).
If a tool returns "not authenticated", call play_login first.`, n, pkg)

		return &mcp.GetPromptResult{
			Description: "Review triage and reply drafts",
			Messages: []*mcp.PromptMessage{
				{Role: "user", Content: &mcp.TextContent{Text: text}},
			},
		}, nil
	})
}
