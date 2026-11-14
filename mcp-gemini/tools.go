package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/mcpkit"
)

func textResult(s string) (*mcp.CallToolResult, any, error) { return mcpkit.TextResult(s) }

func errResult(err error) (*mcp.CallToolResult, any, error) { return mcpkit.ErrorResult(err) }

// voices are the prebuilt Gemini TTS voices.
var voices = []string{
	"Zephyr (bright)", "Puck (upbeat)", "Charon (informative)", "Kore (firm)", "Fenrir (excitable)",
	"Leda (youthful)", "Orus (firm)", "Aoede (breezy)", "Callirrhoe (easy-going)", "Autonoe (bright)",
	"Enceladus (breathy)", "Iapetus (clear)", "Umbriel (easy-going)", "Algieba (smooth)", "Despina (smooth)",
	"Erinome (clear)", "Algenib (gravelly)", "Rasalgethi (informative)", "Laomedeia (upbeat)", "Achernar (soft)",
	"Alnilam (firm)", "Schedar (even)", "Gacrux (mature)", "Pulcherrima (forward)", "Achird (friendly)",
	"Zubenelgenubi (casual)", "Vindemiatrix (gentle)", "Sadachbia (lively)", "Sadaltager (knowledgeable)", "Sulafat (warm)",
}

func voiceName(v string) (string, error) {
	if v == "" {
		return "Kore", nil
	}
	for _, full := range voices {
		name := strings.Fields(full)[0]
		if strings.EqualFold(name, v) {
			return name, nil
		}
	}
	return "", fmt.Errorf("unknown voice %q; voices: %s", v, strings.Join(voices, ", "))
}

func pick[T any](m map[string]T, key, def, what string) (T, error) {
	if key == "" {
		key = def
	}
	v, ok := m[strings.ToLower(key)]
	if !ok {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		return v, fmt.Errorf("unknown %s %q; use one of: %s", what, key, strings.Join(keys, ", "))
	}
	return v, nil
}

func fileLine(path string, size int64) string {
	return fmt.Sprintf("%s (%s)", path, humanSize(size))
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

type imageArgs struct {
	Prompt      string   `json:"prompt" jsonschema:"What to draw, or how to change the input images (or the previous result)."`
	Images      []string `json:"images,omitempty" jsonschema:"Local image paths to edit or use as references (png, jpg, webp, gif, heic; up to 14)."`
	AspectRatio string   `json:"aspectRatio,omitempty" jsonschema:"1:1, 2:3, 3:2, 3:4, 4:3, 4:5, 5:4, 9:16, 16:9 or 21:9; flash also 1:4, 4:1, 1:8, 8:1. Default: the input image's ratio, else 1:1."`
	Size        string   `json:"size,omitempty" jsonschema:"1K (default), 2K or 4K (not for lite)."`
	Model       string   `json:"model,omitempty" jsonschema:"flash (default, Nano Banana 2.1, $0.034 at 1K), lite ($0.034, 1K only, fastest) or pro (Nano Banana Pro, best text rendering, $0.134)."`
	Format      string   `json:"format,omitempty" jsonschema:"png (default) or jpeg."`
	Thinking    string   `json:"thinking,omitempty" jsonschema:"Thinking level: minimal, medium or high (flash: all, default medium; lite: minimal or high). Higher = better layout and text, more thinking tokens billed. Not settable for pro."`
	Search      []string `json:"search,omitempty" jsonschema:"Ground on Google Search: web (facts, current events, real places) and/or image (visual references; flash only). Not for lite."`
	Previous    string   `json:"previous,omitempty" jsonschema:"Interaction id of an image made by this tool, to keep editing it in conversation (no need to pass the image again)."`
	Out         string   `json:"out,omitempty" jsonschema:"Output file or folder. Default ./generated/."`
	Preview     bool     `json:"preview,omitempty" jsonschema:"Also return the image to you to look at (costs context tokens)."`
}

type speaker struct {
	Name  string `json:"name" jsonschema:"Speaker name exactly as used in the text (\"Name: line\")."`
	Voice string `json:"voice" jsonschema:"Voice name, e.g. Kore."`
}

type speechArgs struct {
	Text     string    `json:"text" jsonschema:"What to say. Steer the delivery in words: \"Say slowly and warmly: …\". For two speakers write lines as \"Name: text\"."`
	Voice    string    `json:"voice,omitempty" jsonschema:"Voice for one speaker (default Kore). See the tool description for the list."`
	Speakers []speaker `json:"speakers,omitempty" jsonschema:"Exactly two speakers for a dialogue; replaces voice."`
	Model    string    `json:"model,omitempty" jsonschema:"flash (default, Gemini 3.8 Flash TTS, ~$0.002 per 10 s) or lite (~$0.0015 per 10 s)."`
	Out      string    `json:"out,omitempty" jsonschema:"Output file or folder. Default ./generated/ as .wav; a path ending in .mp3 converts with ffmpeg."`
}

type musicArgs struct {
	Prompt string   `json:"prompt" jsonschema:"Genre, mood, instruments, tempo, structure; lyrics with [Verse] / [Chorus] tags, or \"instrumental only, no vocals\"."`
	Images []string `json:"images,omitempty" jsonschema:"Local images to inspire the music (up to 10)."`
	Full   bool     `json:"full,omitempty" jsonschema:"true = full song with Lyria 3.5 ($0.08, a couple of minutes; length can be asked in the prompt). false (default) = 30 s clip ($0.04)."`
	Out    string   `json:"out,omitempty" jsonschema:"Output file or folder (MP3). Default ./generated/."`
}

type videoArgs struct {
	Prompt      string   `json:"prompt" jsonschema:"The shot: subject, action, camera, style, sound. For edit/extend: what to change or what happens next."`
	Images      []string `json:"images,omitempty" jsonschema:"Local images: a start frame (image-to-video) or references for characters / products / style."`
	AspectRatio string   `json:"aspectRatio,omitempty" jsonschema:"16:9 (default) or 9:16."`
	Resolution  string   `json:"resolution,omitempty" jsonschema:"360p (cheap drafts), 720p (default), 1080p or 4k (upscaled, cost more)."`
	Previous    string   `json:"previous,omitempty" jsonschema:"Interaction id of a video made by this tool, to edit or extend it (kept 55 days)."`
	Out         string   `json:"out,omitempty" jsonschema:"Output file or folder (MP4). Default ./generated/."`
}

type usageArgs struct {
	Days int `json:"days,omitempty" jsonschema:"How many days back, today included (default 30)."`
}

func registerTools(s *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	openWorld := true
	paid := &mcp.ToolAnnotations{OpenWorldHint: &openWorld}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gemini_image",
		Title:       "Generate Image",
		Description: "Generate or edit an image with Nano Banana and save it. Text-to-image, or pass images to edit / combine / keep a character or product consistent; pass previous (an interaction id from an earlier result) to keep editing. Optional Google Search grounding and thinking level. Returns the file path, the interaction id and the estimated cost.",
		Annotations: paid,
	}, func(ctx context.Context, req *mcp.CallToolRequest, a imageArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(a.Prompt) == "" {
			return errResult(errors.New("prompt is required"))
		}
		m, err := pick(imageModels, a.Model, "flash", "model")
		if err != nil {
			return errResult(err)
		}
		body, size, err := imageRequest(m, a)
		if err != nil {
			return errResult(err)
		}
		c, err := newClient()
		if err != nil {
			return errResult(err)
		}
		r, err := c.interact(ctx, body)
		if err != nil {
			return errResult(err)
		}
		all, text := r.outputs()
		var media []stepContent
		for _, c := range all { // skip search results and other non-image items
			if c.Type == "image" && c.Data != "" {
				media = append(media, c)
			}
		}
		if len(media) == 0 {
			return errResult(fmt.Errorf("no image returned. %s", text))
		}
		textTokens := r.Usage.TotalThoughtTokens + r.modalityTokens("text")
		cost := m.cost(size, len(media), textTokens)
		var b strings.Builder
		res := &mcp.CallToolResult{}
		for i, img := range media {
			path, err := outputPath(a.Out, "image", extOr(img.MimeType, ".png"), i)
			if err != nil {
				return errResult(err)
			}
			n, err := writeBase64(path, img.Data)
			if err != nil {
				return errResult(err)
			}
			fmt.Fprintf(&b, "Saved %s\n", fileLine(path, n))
			if a.Preview {
				raw, _ := base64.StdEncoding.DecodeString(img.Data)
				res.Content = append(res.Content, &mcp.ImageContent{Data: raw, MIMEType: img.MimeType})
			}
			record("gemini_image", m.ID, cost/float64(len(media)), path)
		}
		fmt.Fprintf(&b, "Model %s, %s, est. $%.3f\nInteraction id (for further edits): %s", m.ID, size, cost, r.ID)
		if text != "" {
			b.WriteString("\nModel note: " + text)
		}
		res.Content = append([]mcp.Content{&mcp.TextContent{Text: b.String()}}, res.Content...)
		return res, nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gemini_speech",
		Title:       "Text to Speech",
		Description: "Turn text into natural speech with Gemini TTS (many languages incl. Vietnamese; one voice or a two-speaker dialogue) and save it as WAV or MP3. Voices: " + strings.Join(voices, ", ") + ".",
		Annotations: paid,
	}, func(ctx context.Context, req *mcp.CallToolRequest, a speechArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(a.Text) == "" {
			return errResult(errors.New("text is required"))
		}
		m, err := pick(ttsModels, a.Model, "flash", "model")
		if err != nil {
			return errResult(err)
		}
		speech := map[string]any{}
		parts := []part{{Text: a.Text}}
		var voiceDesc string
		switch len(a.Speakers) {
		case 0:
			v, err := voiceName(a.Voice)
			if err != nil {
				return errResult(err)
			}
			speech["voiceConfig"] = map[string]any{"prebuiltVoiceConfig": map[string]any{"voiceName": v}}
			voiceDesc = v
		case 2:
			var list []map[string]any
			var names []string
			for _, sp := range a.Speakers {
				v, err := voiceName(sp.Voice)
				if err != nil {
					return errResult(err)
				}
				if strings.TrimSpace(sp.Name) == "" {
					return errResult(errors.New("each speaker needs a name"))
				}
				list = append(list, map[string]any{"speaker": sp.Name, "voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]any{"voiceName": v}}})
				names = append(names, sp.Name+"="+v)
			}
			speech["multiSpeakerVoiceConfig"] = map[string]any{"speakerVoiceConfigs": list}
			voiceDesc = strings.Join(names, ", ")
			if parts, err = dialogueParts(a.Text, []string{a.Speakers[0].Name, a.Speakers[1].Name}); err != nil {
				return errResult(err)
			}
		default:
			return errResult(errors.New("speakers needs exactly two entries (or use voice for one speaker)"))
		}
		c, err := newClient()
		if err != nil {
			return errResult(err)
		}
		r, err := c.generate(ctx, m.ID, parts, map[string]any{"responseModalities": []string{"AUDIO"}, "speechConfig": speech})
		if err != nil {
			return errResult(err)
		}
		media, text, finish := r.outputs()
		if len(media) == 0 {
			return errResult(fmt.Errorf("no audio returned (finish: %s). %s", finish, text))
		}
		raw, err := base64.StdEncoding.DecodeString(media[0].Data)
		if err != nil {
			return errResult(err)
		}
		if mime := strings.ToLower(media[0].MimeType); strings.Contains(mime, "l16") || strings.Contains(mime, "pcm") {
			raw = pcmToWAV(raw, pcmRate(mime))
		}
		mp3 := strings.EqualFold(filepath.Ext(a.Out), ".mp3")
		path, err := outputPath(a.Out, "speech", ".wav", 0)
		if err != nil {
			return errResult(err)
		}
		wavPath := path
		if mp3 {
			wavPath = strings.TrimSuffix(path, filepath.Ext(path)) + ".tmp.wav"
		}
		if err := os.WriteFile(wavPath, raw, 0o644); err != nil {
			return errResult(err)
		}
		if mp3 {
			err := toMP3(ctx, wavPath, path)
			os.Remove(wavPath)
			if err != nil {
				return errResult(err)
			}
		}
		st, err := os.Stat(path)
		if err != nil {
			return errResult(err)
		}
		cost := m.cost(r.UsageMetadata.CandidatesTokenCount, nowFunc())
		record("gemini_speech", m.ID, cost, path)
		return textResult(fmt.Sprintf("Saved %s%s\nModel %s, voice %s, est. $%.4f",
			fileLine(path, st.Size()), withDuration(ctx, path), m.ID, voiceDesc, cost))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gemini_music",
		Title:       "Generate Music",
		Description: "Compose music with Lyria and save it as MP3 (44.1 kHz stereo, SynthID watermark): a 30 s clip by default, or a full song with full:true. Vocals and lyrics come from the prompt. Returns the file path, the lyrics or structure, and the cost.",
		Annotations: paid,
	}, func(ctx context.Context, req *mcp.CallToolRequest, a musicArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(a.Prompt) == "" {
			return errResult(errors.New("prompt is required"))
		}
		model, price := musicClipModel, musicClipPrice
		if a.Full {
			model, price = musicFullModel, musicFullPrice
		}
		if len(a.Images) > 10 {
			return errResult(errors.New("at most 10 images"))
		}
		input, err := interactionInput(a.Prompt, a.Images)
		if err != nil {
			return errResult(err)
		}
		c, err := newClient()
		if err != nil {
			return errResult(err)
		}
		r, err := c.interact(ctx, map[string]any{"model": model, "input": input, "response_format": map[string]any{"type": "audio"}, "store": false})
		if err != nil {
			return errResult(err)
		}
		media, text := r.outputs()
		if len(media) == 0 || media[0].Data == "" {
			return errResult(fmt.Errorf("no audio returned. %s", text))
		}
		path, err := outputPath(a.Out, "music", extOr(media[0].MimeType, ".mp3"), 0)
		if err != nil {
			return errResult(err)
		}
		n, err := writeBase64(path, media[0].Data)
		if err != nil {
			return errResult(err)
		}
		record("gemini_music", model, price, path)
		out := fmt.Sprintf("Saved %s%s\nModel %s, est. $%.2f", fileLine(path, n), withDuration(ctx, path), model, price)
		if text != "" {
			out += "\n\nLyrics / structure:\n" + text
		}
		return textResult(out)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gemini_video",
		Title:       "Generate Video",
		Description: "Make a short video with sound using Gemini Omni Flash and save it as MP4: text-to-video, image-to-video, reference images, or edit/extend an earlier result (pass previous). About 4–10 s per call, ~$0.10 per second at 720p. Returns the file path, the interaction id for later edits, and the cost.",
		Annotations: paid,
	}, func(ctx context.Context, req *mcp.CallToolRequest, a videoArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(a.Prompt) == "" {
			return errResult(errors.New("prompt is required"))
		}
		format := map[string]any{"type": "video", "delivery": "uri"}
		if a.AspectRatio != "" {
			if a.AspectRatio != "16:9" && a.AspectRatio != "9:16" {
				return errResult(errors.New("aspectRatio must be 16:9 or 9:16"))
			}
			format["aspect_ratio"] = a.AspectRatio
		}
		if a.Resolution != "" {
			r := strings.ToLower(a.Resolution)
			if !slices.Contains([]string{"360p", "720p", "1080p", "4k"}, r) {
				return errResult(errors.New("resolution must be 360p, 720p, 1080p or 4k"))
			}
			format["resolution"] = r
		}
		input, err := interactionInput(a.Prompt, a.Images)
		if err != nil {
			return errResult(err)
		}
		body := map[string]any{"model": videoModel, "input": input, "response_format": format}
		if a.Previous != "" {
			body["previous_interaction_id"] = a.Previous
		}
		c, err := newClient()
		if err != nil {
			return errResult(err)
		}
		r, err := c.interact(ctx, body)
		if err != nil {
			return errResult(err)
		}
		media, text := r.outputs()
		if len(media) == 0 {
			return errResult(fmt.Errorf("no video returned. %s", text))
		}
		path, err := outputPath(a.Out, "video", extOr(media[0].MimeType, ".mp4"), 0)
		if err != nil {
			return errResult(err)
		}
		var n int64
		if media[0].URI != "" {
			n, err = c.download(ctx, media[0].URI, path)
		} else {
			n, err = writeBase64(path, media[0].Data)
		}
		if err != nil {
			return errResult(err)
		}
		vt := r.modalityTokens("video")
		if vt == 0 {
			vt = r.Usage.TotalOutputTokens
		}
		cost := videoCost(vt, r.Usage.TotalThoughtTokens)
		record("gemini_video", videoModel, cost, path)
		out := fmt.Sprintf("Saved %s%s\nModel %s, est. $%.2f\nInteraction id (for edit/extend): %s",
			fileLine(path, n), withDuration(ctx, path), videoModel, cost, r.ID)
		if text != "" {
			out += "\nModel note: " + text
		}
		return textResult(out)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gemini_usage",
		Title:       "Media Spend",
		Description: "Estimated Gemini media spend from this machine's log: total, by day, by tool and by working directory (workspace).",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, a usageArgs) (*mcp.CallToolResult, any, error) {
		days := a.Days
		if days <= 0 {
			days = 30
		}
		entries, err := readLedger()
		if err != nil {
			return errResult(err)
		}
		return textResult(usageReport(entries, days, nowFunc()))
	})
}

func withDuration(ctx context.Context, path string) string {
	if d := duration(ctx, path); d != "" {
		return ", " + d
	}
	return ""
}

func extOr(mime, def string) string {
	if e, ok := extByMime[strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0]))]; ok {
		return e
	}
	return def
}

// imageRequest validates the arguments against the model and builds the
// Interactions API body. It returns the image size used.
func imageRequest(m imageModel, a imageArgs) (map[string]any, string, error) {
	size := strings.ToUpper(a.Size)
	if size == "" {
		size = m.Sizes[0]
	}
	if !slices.Contains(m.Sizes, size) {
		return nil, "", fmt.Errorf("size %s not supported by %s; use %s", size, m.ID, strings.Join(m.Sizes, ", "))
	}
	if len(a.Images) > 14 {
		return nil, "", errors.New("at most 14 input images")
	}
	format := map[string]any{"type": "image"}
	if len(m.Sizes) > 1 { // lite takes no image_size
		format["image_size"] = size
	}
	if a.AspectRatio != "" {
		if !slices.Contains(m.Ratios, a.AspectRatio) {
			return nil, "", fmt.Errorf("aspectRatio %s not supported by %s; use %s", a.AspectRatio, m.ID, strings.Join(m.Ratios, ", "))
		}
		format["aspect_ratio"] = a.AspectRatio
	}
	switch strings.ToLower(a.Format) {
	case "", "png":
	case "jpeg", "jpg":
		format["mime_type"] = "image/jpeg"
	default:
		return nil, "", errors.New("format must be png or jpeg")
	}
	input, err := interactionInput(a.Prompt, a.Images)
	if err != nil {
		return nil, "", err
	}
	body := map[string]any{"model": m.ID, "input": input, "response_format": format}
	if a.Thinking != "" {
		t := strings.ToLower(a.Thinking)
		if !slices.Contains(m.Thinking, t) {
			if len(m.Thinking) == 0 {
				return nil, "", fmt.Errorf("%s has no thinking level setting", m.ID)
			}
			return nil, "", fmt.Errorf("thinking %s not supported by %s; use %s", a.Thinking, m.ID, strings.Join(m.Thinking, ", "))
		}
		body["generation_config"] = map[string]any{"thinking_level": t}
	}
	if len(a.Search) > 0 {
		var types []string
		for _, st := range a.Search {
			st = strings.TrimSuffix(strings.ToLower(st), "_search")
			if !slices.Contains(m.Search, st) {
				if len(m.Search) == 0 {
					return nil, "", fmt.Errorf("%s has no Google Search grounding", m.ID)
				}
				return nil, "", fmt.Errorf("search %q not supported by %s; use %s", st, m.ID, strings.Join(m.Search, ", "))
			}
			if !slices.Contains(types, st+"_search") {
				types = append(types, st+"_search")
			}
		}
		body["tools"] = []map[string]any{{"type": "google_search", "search_types": types}}
	}
	if a.Previous != "" {
		body["previous_interaction_id"] = a.Previous
	}
	return body, size, nil
}

// interactionInput is the prompt alone, or text plus inline images.
func interactionInput(prompt string, images []string) (any, error) {
	if len(images) == 0 {
		return prompt, nil
	}
	items := []map[string]any{{"type": "text", "text": prompt}}
	for _, p := range images {
		mime, data, err := readImage(p)
		if err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"type": "image", "mime_type": mime, "data": data})
	}
	return items, nil
}
