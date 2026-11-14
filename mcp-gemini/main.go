// mcp-gemini is an MCP server for Gemini API media generation: images (Nano
// Banana), speech (Gemini TTS), music (Lyria) and video (Gemini Omni). Files
// are written to disk; every call is logged with its estimated cost.
//
// Usage:
//
//	mcp-gemini                # run the MCP server on stdio (default)
//	mcp-gemini setup          # store an API key read from stdin
package main

import "github.com/homielab/homielab-mcp/internal/mcpkit"

var version = "0.1.0"

const serverName = "mcp-gemini"

const serverInstructions = `Gemini media server — generate images, speech, music and video with the Gemini API and save them as files. Every call costs money (estimate in each result; gemini_usage shows the totals).

- Files go to "out" (a file or folder path); default ./generated/ under the
  current directory (or $GEMINI_MEDIA_DIR). Existing files are never
  overwritten: a -2, -3… suffix is added. Pass inputs as local file paths.
- gemini_image: text-to-image and editing (pass images). Cheapest: model
  "lite" (1K only); "pro" for text-heavy or detailed images. To change a
  result, pass its interaction id as "previous" with what to change.
  search ["web"] / ["image"] grounds on Google Search (real places, current
  facts, visual references); thinking "high" for complex layouts.
- gemini_speech: one voice, or two speakers with "Name: line" text. Style goes
  in the text ("Say calmly: …"). Vietnamese works. Output WAV, or MP3 when
  "out" ends in .mp3 (needs ffmpeg).
- gemini_music: Lyria. Default a 30 s clip ($0.04); full:true for a whole song
  ($0.08, a couple of minutes, lyrics with [Verse]/[Chorus] tags in the
  prompt). Say "instrumental only, no vocals" for background music. MP3.
- gemini_video: Gemini Omni, about 4–10 s per call, ~$0.10/s at 720p. Pass
  images for image-to-video or references. To edit or extend a generated
  video, pass its interaction id (from the result) as "previous".
- Start cheap (lite image, clip music, 360p video) to iterate, then spend on
  the final version.`

func main() {
	mcpkit.Main(mcpkit.Server{
		Name:         serverName,
		Version:      version,
		Summary:      "MCP server for Gemini API media generation (image, speech, music, video)",
		Instructions: serverInstructions,
		Credentials:  credentialSources,
		Commands: []mcpkit.Command{
			{Name: "setup", Usage: "store a Gemini API key read from stdin (0600 file in the config dir)", Run: runSetup},
		},
		Register: registerTools,
	})
}
