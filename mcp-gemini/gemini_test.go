package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func fixedNow(t *testing.T) {
	old := nowFunc
	nowFunc = func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { nowFunc = old })
}

func TestOutputPath(t *testing.T) {
	fixedNow(t)
	dir := t.TempDir()
	t.Chdir(dir)

	p, err := outputPath("", "image", ".png", 0)
	if err != nil || p != filepath.Join(dir, "generated", "image-20261030-120000.png") {
		t.Fatalf("default: %s %v", p, err)
	}
	os.WriteFile(p, nil, 0o644)
	if p2, _ := outputPath("", "image", ".png", 0); p2 != filepath.Join(dir, "generated", "image-20261030-120000-2.png") {
		t.Fatalf("no overwrite: %s", p2)
	}
	if p, _ := outputPath("assets/voice", "speech", ".wav", 0); p != filepath.Join(dir, "assets", "voice.wav") {
		t.Fatalf("file without ext: %s", p)
	}
	if p, _ := outputPath("out/", "music", ".mp3", 1); p != filepath.Join(dir, "out", "music-20261030-120000-2.mp3") {
		t.Fatalf("folder + index: %s", p)
	}
	t.Setenv("GEMINI_MEDIA_DIR", filepath.Join(dir, "media"))
	if p, _ := outputPath("", "video", ".mp4", 0); p != filepath.Join(dir, "media", "video-20261030-120000.mp4") {
		t.Fatalf("GEMINI_MEDIA_DIR: %s", p)
	}
}

func TestPCMToWAV(t *testing.T) {
	w := pcmToWAV(make([]byte, 480), pcmRate("audio/L16;codec=pcm;rate=24000"))
	if len(w) != 524 || string(w[:4]) != "RIFF" || string(w[8:12]) != "WAVE" {
		t.Fatalf("bad header: %q", w[:12])
	}
	if pcmRate("audio/L16") != 24000 || pcmRate("audio/L16;rate=16000") != 16000 {
		t.Fatal("rate")
	}
}

func TestCosts(t *testing.T) {
	m := ttsModels["flash"]
	if c := m.cost(1_000_000, time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)); c != 9 {
		t.Fatalf("2026 tts: %v", c)
	}
	if c := m.cost(1_000_000, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)); c != 18 {
		t.Fatalf("2027 tts: %v", c)
	}
	if c := videoCost(5792, 0); c < 0.1 || c > 0.102 {
		t.Fatalf("video 1 s: %v", c)
	}
}

func TestUsageReport(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	entries := []usageEntry{
		{Time: now.Add(-time.Hour), Tool: "gemini_image", Cost: 0.0336, Dir: "/w/a"},
		{Time: now.Add(-25 * time.Hour), Tool: "gemini_music", Cost: 0.08, Dir: "/w/b"},
		{Time: now.AddDate(0, 0, -40), Tool: "gemini_video", Cost: 5, Dir: "/w/a"},
	}
	r := usageReport(entries, 30, now)
	if !strings.Contains(r, "$0.11 in 2 call(s)") || strings.Contains(r, "gemini_video") || !strings.Contains(r, "/w/b") {
		t.Fatalf("report:\n%s", r)
	}
}

// fakeAPI serves generateContent, interactions and files like the Gemini API.
func fakeAPI(t *testing.T) {
	t.Helper()
	pngData := base64.StdEncoding.EncodeToString([]byte("\x89PNG fake"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"code":401,"message":"bad key","status":"UNAUTHENTICATED"}}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, ":generateContent") && strings.Contains(r.URL.Path, "tts"):
			pcm := base64.StdEncoding.EncodeToString(make([]byte, 4800))
			io.WriteString(w, `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"audio/L16;codec=pcm;rate=24000","data":"`+pcm+`"}}]},"finishReason":"STOP"}],"usageMetadata":{"candidatesTokenCount":25}}`)
		case r.URL.Path == "/interactions" && strings.Contains(string(body), "nano-banana"):
			var req map[string]any
			json.Unmarshal(body, &req)
			f, _ := req["response_format"].(map[string]any)
			if f["type"] != "image" || f["aspect_ratio"] != "16:9" || f["image_size"] != "1K" || f["mime_type"] != "image/jpeg" {
				t.Errorf("bad response_format: %s", body)
			}
			if !strings.Contains(string(body), `"search_types":["web_search","image_search"]`) || !strings.Contains(string(body), `"thinking_level":"high"`) || req["previous_interaction_id"] != "i0" {
				t.Errorf("bad image request: %s", body)
			}
			io.WriteString(w, `{"id":"i1","status":"completed","usage":{"total_thought_tokens":1000,"output_tokens_by_modality":[{"modality":"image","tokens":1120}]},"steps":[{"type":"google_search_result","content":[{"type":"text","uri":"https://example.com"}]},{"type":"model_output","content":[{"type":"text","text":"here"},{"type":"image","mime_type":"image/jpeg","data":"`+pngData+`"}]}]}`)
		case r.URL.Path == "/interactions" && strings.Contains(string(body), "lyria"):
			io.WriteString(w, `{"id":"m1","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"[Verse] la"}]},{"type":"model_output","content":[{"type":"audio","mime_type":"audio/mpeg","data":"`+base64.StdEncoding.EncodeToString([]byte("ID3"))+`"}]}]}`)
		case r.URL.Path == "/interactions":
			io.WriteString(w, `{"id":"v1","status":"completed","usage":{"total_output_tokens":5792,"output_tokens_by_modality":[{"modality":"video","tokens":5792}]},"steps":[{"type":"thought"},{"type":"model_output","content":[{"type":"video","mime_type":"video/mp4","uri":"`+apiBase+`/files/abc:download?alt=media"}]}]}`)
		case r.URL.Path == "/files/abc":
			io.WriteString(w, `{"name":"files/abc","state":"ACTIVE"}`)
		case r.URL.Path == "/files/abc:download":
			io.WriteString(w, "MP4DATA")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = old })
	t.Setenv("GEMINI_API_KEY", "test-key")
	configBase = t.TempDir()
	t.Cleanup(func() { configBase = "" })
}

func callTool(t *testing.T, name string, args map[string]any) (string, bool) {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	registerTools(s)
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func TestTools(t *testing.T) {
	fakeAPI(t)
	fixedNow(t)
	dir := t.TempDir()
	t.Chdir(dir)

	out, isErr := callTool(t, "gemini_image", map[string]any{"prompt": "a plane", "aspectRatio": "16:9", "format": "jpeg",
		"thinking": "high", "search": []string{"web", "image_search"}, "previous": "i0"})
	if isErr || !strings.Contains(out, "generated/image-20261030-120000.jpg") || !strings.Contains(out, "$0.041") || !strings.Contains(out, "Interaction id (for further edits): i1") {
		t.Fatalf("image: %s", out)
	}
	for _, bad := range []map[string]any{
		{"prompt": "x", "model": "pro", "aspectRatio": "8:1"},
		{"prompt": "x", "model": "lite", "search": []string{"web"}},
		{"prompt": "x", "model": "pro", "search": []string{"image"}},
		{"prompt": "x", "model": "pro", "thinking": "high"},
		{"prompt": "x", "model": "lite", "thinking": "medium"},
		{"prompt": "x", "format": "webp"},
	} {
		if out, isErr := callTool(t, "gemini_image", bad); !isErr {
			t.Fatalf("accepted %v: %s", bad, out)
		}
	}
	out, isErr = callTool(t, "gemini_speech", map[string]any{"text": "Xin chào", "voice": "puck", "out": "voice/hello"})
	if isErr || !strings.Contains(out, "voice/hello.wav") || !strings.Contains(out, "Puck") {
		t.Fatalf("speech: %s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "voice", "hello.wav")); len(b) != 44+4800 {
		t.Fatalf("wav size %d", len(b))
	}
	out, isErr = callTool(t, "gemini_music", map[string]any{"prompt": "lofi"})
	if isErr || !strings.Contains(out, ".mp3") || !strings.Contains(out, "[Verse] la") {
		t.Fatalf("music: %s", out)
	}
	out, isErr = callTool(t, "gemini_video", map[string]any{"prompt": "a plane", "resolution": "360p"})
	if isErr || !strings.Contains(out, "Interaction id (for edit/extend): v1") || !strings.Contains(out, "$0.10") {
		t.Fatalf("video: %s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "generated", "video-20261030-120000.mp4")); string(b) != "MP4DATA" {
		t.Fatalf("video file %q", b)
	}
	out, _ = callTool(t, "gemini_usage", map[string]any{})
	if !strings.Contains(out, "in 4 call(s)") {
		t.Fatalf("usage: %s", out)
	}
	if out, isErr = callTool(t, "gemini_speech", map[string]any{"text": "x", "voice": "nobody"}); !isErr {
		t.Fatalf("bad voice accepted: %s", out)
	}
	t.Setenv("GEMINI_API_KEY", "wrong")
	if out, isErr = callTool(t, "gemini_music", map[string]any{"prompt": "x"}); !isErr || !strings.Contains(out, "UNAUTHENTICATED") {
		t.Fatalf("bad key: %s", out)
	}
}

func TestDialogueParts(t *testing.T) {
	p, err := dialogueParts("An: Hôm nay thế nào?\nvẫn An\n\nBình: Tuyệt!", []string{"An", "Bình"})
	if err != nil || len(p) != 2 || p[0].Text != "Hôm nay thế nào?\nvẫn An" || p[1].SpeechMetadata.Speaker != "Bình" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := dialogueParts("Say happily:\nAn: hi", []string{"An", "Bình"}); err == nil {
		t.Fatal("preamble accepted")
	}
}
