# mcp-gemini

MCP server (Go) for **Gemini API media generation**: images (Nano Banana),
speech (Gemini TTS), music (Lyria) and video (Gemini Omni). Results are saved
as files; each call logs its estimated cost.

Auth is a Gemini **API key** (`GEMINI_API_KEY`). OAuth would cost the same:
the Gemini API bills per project and per use, whatever the login.

## Tools

| Tool | What it does | Model | Cost (paid tier, 2026-10) |
|---|---|---|---|
| `gemini_image` | Text-to-image; edit, combine or keep consistent with input images; multi-turn edit by interaction id; Google Search grounding; thinking level; PNG or JPEG | `flash` Nano Banana 2.1 (default), `lite`, `pro` | $0.034 per 1K image; pro $0.134 |
| `gemini_speech` | Text to speech, one voice or a two-speaker dialogue ("Name: line"); WAV, or MP3 via ffmpeg | `flash` Gemini 3.8 Flash TTS, `lite` | ~$0.002 per 10 s (doubles from 2027) |
| `gemini_music` | 30 s clip, or a full song (`full:true`) with lyrics from the prompt; MP3 | Lyria 3 Clip / Lyria 3.5 | $0.04 clip, $0.08 song |
| `gemini_video` | Short video with sound: text, start image, references; edit/extend a result by its interaction id | Gemini Omni 1.1 Flash | ~$0.10/s at 720p, ~$0.034/s at 360p |
| `gemini_usage` | Estimated spend by day, tool and working directory | — | — |

Output: `out` (a file or folder), default `./generated/` under the working
directory (or `$GEMINI_MEDIA_DIR`). Existing files are never overwritten.

Spend log: `~/Library/Application Support/mcp-gemini/usage.jsonl`. Prices are in
`usage.go` (from https://ai.google.dev/gemini-api/docs/pricing); update them
when Google changes prices.

## Setup

```bash
./build.sh                                   # from the repo root
claude mcp add -s user mcp-gemini "$(go env GOPATH)/bin/mcp-gemini"
```

The key comes from `GEMINI_API_KEY` or `GOOGLE_API_KEY`. Without them,
`mcp-gemini setup` stores a pasted key in `<user config dir>/mcp-gemini/api_key`
(mode 0600). MP3 speech and durations need `ffmpeg` / `ffprobe` on PATH.

## API notes

- Images: the Interactions API, `response_format` `{type:"image", image_size,
  aspect_ratio, mime_type}`; `generation_config.thinking_level`; tools
  `google_search` with `search_types` `web_search` / `image_search`. Thinking
  and text tokens are billed on top of the per-image price.
- Speech: `models/<model>:generateContent`. Gemini 3.8 TTS returns
  WAV; older TTS models return raw PCM (`audio/L16`), which is wrapped in a WAV
  header. Multi-speaker TTS needs `speechMetadata.speaker` on every text part.
- Music and video: also the Interactions API (`POST /v1beta/interactions`).
  - Music comes back inline as `audio/mpeg`, with `store:false`.
  - Video is requested with `delivery:"uri"`, then downloaded from
    `files/<id>:download`. Generated files expire after 2 days.
  - Video interactions are stored, which keeps the id usable for edit and extend.
