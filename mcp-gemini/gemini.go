package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/homielab/homielab-mcp/internal/googleapi"
)

// The Gemini API authenticates with an API key (x-goog-api-key). Speech uses
// models.generateContent; images, music and video use the Interactions API.

// apiBase is overridden in tests.
var apiBase = "https://generativelanguage.googleapis.com/v1beta"

const credentialSources = `  - GEMINI_API_KEY, then GOOGLE_API_KEY
  - <user config dir>/mcp-gemini/api_key (written by "mcp-gemini setup")
`

// configBase overrides os.UserConfigDir in tests.
var configBase string

func configDir() (string, error) {
	base := configBase
	if base == "" {
		var err error
		if base, err = os.UserConfigDir(); err != nil {
			return "", err
		}
	}
	return filepath.Join(base, serverName), nil
}

var errNoKey = errors.New(`no Gemini API key: set GEMINI_API_KEY, or run "mcp-gemini setup" and paste a key from https://aistudio.google.com/apikey`)

func apiKey() (string, error) {
	for _, env := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		if k := strings.TrimSpace(os.Getenv(env)); k != "" {
			return k, nil
		}
	}
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(dir, "api_key"))
	if k := strings.TrimSpace(string(b)); err == nil && k != "" {
		return k, nil
	}
	return "", errNoKey
}

func runSetup(ctx context.Context, _ []string) error {
	fmt.Fprintln(os.Stderr, "Paste the Gemini API key, then press Enter:")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	key := strings.TrimSpace(line)
	if key == "" {
		if err != nil {
			return err
		}
		return errors.New("empty key")
	}
	dir, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "api_key"), []byte(key+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Println("Key stored in", filepath.Join(dir, "api_key"))
	return nil
}

type keyTransport struct{ key string }

func (t keyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("x-goog-api-key", t.key)
	return http.DefaultTransport.RoundTrip(r)
}

type client struct {
	hc  *http.Client
	api *googleapi.Client
}

func newClient() (*client, error) {
	key, err := apiKey()
	if err != nil {
		return nil, err
	}
	// Generation takes seconds to minutes; the default 90 s is too short.
	hc := &http.Client{Timeout: 10 * time.Minute, Transport: keyTransport{key}}
	return &client{hc: hc, api: googleapi.New(hc, "Gemini API")}, nil
}

// ---- generateContent (speech) ----

type inlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type part struct {
	Text           string          `json:"text,omitempty"`
	InlineData     *inlineData     `json:"inlineData,omitempty"`
	SpeechMetadata *speechMetadata `json:"speechMetadata,omitempty"`
}

// speechMetadata names the speaker of a text part (multi-speaker TTS).
type speechMetadata struct {
	Speaker string `json:"speaker"`
}

type usageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
}

type generateResponse struct {
	Candidates []struct {
		Content struct {
			Parts []part `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata usageMetadata `json:"usageMetadata"`
}

func (c *client) generate(ctx context.Context, model string, parts []part, config map[string]any) (*generateResponse, error) {
	body := map[string]any{
		"contents":         []map[string]any{{"role": "user", "parts": parts}},
		"generationConfig": config,
	}
	data, _, err := c.api.Do(ctx, http.MethodPost, apiBase+"/models/"+model+":generateContent", body)
	if err != nil {
		return nil, err
	}
	var r generateResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("decoding generateContent: %w", err)
	}
	if r.PromptFeedback != nil && r.PromptFeedback.BlockReason != "" {
		return nil, fmt.Errorf("prompt blocked: %s", r.PromptFeedback.BlockReason)
	}
	return &r, nil
}

// outputs splits the first candidate into media parts and text.
func (r *generateResponse) outputs() (media []inlineData, text string, finish string) {
	if len(r.Candidates) == 0 {
		return nil, "", "no candidates"
	}
	var texts []string
	for _, p := range r.Candidates[0].Content.Parts {
		if p.InlineData != nil {
			media = append(media, *p.InlineData)
		} else if strings.TrimSpace(p.Text) != "" {
			texts = append(texts, strings.TrimSpace(p.Text))
		}
	}
	return media, strings.Join(texts, "\n"), r.Candidates[0].FinishReason
}

// ---- Interactions API (music, video) ----

type stepContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	URI      string `json:"uri,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

type interaction struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Steps  []struct {
		Type    string        `json:"type"`
		Content []stepContent `json:"content"`
	} `json:"steps"`
	Usage struct {
		TotalOutputTokens      int `json:"total_output_tokens"`
		TotalThoughtTokens     int `json:"total_thought_tokens"`
		OutputTokensByModality []struct {
			Modality string `json:"modality"`
			Tokens   int    `json:"tokens"`
		} `json:"output_tokens_by_modality"`
	} `json:"usage"`
}

func (c *client) interact(ctx context.Context, body map[string]any) (*interaction, error) {
	data, _, err := c.api.Do(ctx, http.MethodPost, apiBase+"/interactions", body)
	if err != nil {
		return nil, err
	}
	var r interaction
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("decoding interaction: %w", err)
	}
	if r.Status != "" && r.Status != "completed" {
		return nil, fmt.Errorf("interaction %s ended with status %q", r.ID, r.Status)
	}
	return &r, nil
}

// outputs returns the model_output media items and text.
func (r *interaction) outputs() (media []stepContent, text string) {
	var texts []string
	for _, s := range r.Steps {
		if s.Type != "model_output" {
			continue
		}
		for _, c := range s.Content {
			switch {
			case c.Type == "text":
				if t := strings.TrimSpace(c.Text); t != "" {
					texts = append(texts, t)
				}
			case c.Data != "" || c.URI != "":
				media = append(media, c)
			}
		}
	}
	return media, strings.Join(texts, "\n")
}

func (r *interaction) modalityTokens(m string) int {
	for _, t := range r.Usage.OutputTokensByModality {
		if strings.EqualFold(t.Modality, m) {
			return t.Tokens
		}
	}
	return 0
}

// download saves a generated file (an API files/... URI) to path, waiting
// until the file is ACTIVE.
func (c *client) download(ctx context.Context, uri, path string) (int64, error) {
	if !strings.HasPrefix(uri, apiBase+"/files/") {
		return 0, fmt.Errorf("unexpected file URI %q", uri)
	}
	name := strings.SplitN(strings.TrimPrefix(uri, apiBase+"/"), ":", 2)[0] // files/<id>
	for i := 0; ; i++ {
		data, _, err := c.api.Do(ctx, http.MethodGet, apiBase+"/"+name, nil)
		if err != nil {
			return 0, err
		}
		var f struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal(data, &f)
		if f.State == "ACTIVE" || f.State == "" {
			break
		}
		if f.State == "FAILED" || i >= 60 {
			return 0, fmt.Errorf("%s: state %s", name, f.State)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/"+name+":download?alt=media", nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, c.api.Error(resp.StatusCode, b)
	}
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	return n, os.Rename(tmp, path)
}

// ---- files ----

var extByMime = map[string]string{
	"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif", "image/heic": ".heic",
	"audio/wav": ".wav", "audio/x-wav": ".wav", "audio/mpeg": ".mp3", "audio/mp3": ".mp3",
	"video/mp4": ".mp4",
}

var mimeByExt = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp", ".gif": "image/gif",
	".heic": "image/heic", ".heif": "image/heif",
}

const maxInputImage = 20 << 20

// readImage loads a local image as base64 for a request.
func readImage(path string) (mime, b64 string, err error) {
	path = expandHome(path)
	mime = mimeByExt[strings.ToLower(filepath.Ext(path))]
	if mime == "" {
		return "", "", fmt.Errorf("%s: not an image (png, jpg, webp, gif, heic)", path)
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", "", err
	}
	if st.Size() > maxInputImage {
		return "", "", fmt.Errorf("%s: larger than 20 MB", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	return mime, base64.StdEncoding.EncodeToString(b), nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// nowFunc is overridden in tests.
var nowFunc = time.Now

// outputPath picks where to write a result: out may be a file, a folder
// (existing, or ending in /) or empty (default folder). ext includes the dot;
// a file without an extension gets it. Never returns an existing file.
func outputPath(out, kind, ext string, index int) (string, error) {
	out = expandHome(strings.TrimSpace(out))
	var path string
	isDir := out == "" || strings.HasSuffix(out, "/")
	if !isDir {
		if st, err := os.Stat(out); err == nil && st.IsDir() {
			isDir = true
		}
	}
	if isDir {
		dir := out
		if dir == "" {
			dir = os.Getenv("GEMINI_MEDIA_DIR")
		}
		if dir == "" {
			dir = "generated"
		}
		path = filepath.Join(dir, kind+"-"+nowFunc().Format("20060102-150405")+ext)
	} else {
		path = out
		if filepath.Ext(path) == "" {
			path += ext
		}
	}
	if index > 0 {
		e := filepath.Ext(path)
		path = strings.TrimSuffix(path, e) + "-" + strconv.Itoa(index+1) + e
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	base, e := strings.TrimSuffix(path, filepath.Ext(path)), filepath.Ext(path)
	for n := 2; ; n++ {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			break
		}
		path = fmt.Sprintf("%s-%d%s", base, n, e)
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return path, nil
}

// writeBase64 decodes data into path.
func writeBase64(path, data string) (int64, error) {
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return 0, fmt.Errorf("decoding output: %w", err)
	}
	return int64(len(b)), os.WriteFile(path, b, 0o644)
}

// pcmToWAV wraps raw 16-bit little-endian mono PCM (audio/L16;rate=N) in a WAV header.
func pcmToWAV(pcm []byte, rate int) []byte {
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(pcm)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], 1) // mono
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(pcm)))
	return append(h, pcm...)
}

// pcmRate reads rate=N from an audio/L16 mime type (24000 if absent).
func pcmRate(mime string) int {
	for _, p := range strings.Split(mime, ";") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(p), "rate="); ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return n
			}
		}
	}
	return 24000
}

// dialogueParts splits "Name: line" text into one part per line for the
// given speakers; a line without a known name continues the previous one.
func dialogueParts(text string, names []string) ([]part, error) {
	var parts []part
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		matched := false
		for _, n := range names {
			if rest, ok := strings.CutPrefix(line, n+":"); ok {
				parts = append(parts, part{Text: strings.TrimSpace(rest), SpeechMetadata: &speechMetadata{Speaker: n}})
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if len(parts) == 0 {
			return nil, fmt.Errorf("dialogue must start with a speaker line (\"%s: …\"); put delivery hints inside a line", names[0])
		}
		parts[len(parts)-1].Text += "\n" + line
	}
	if len(parts) == 0 {
		return nil, errors.New("no \"Name: line\" lines for the speakers")
	}
	return parts, nil
}

// duration returns a media file's length via ffprobe ("" without it).
func duration(ctx context.Context, path string) string {
	fp, err := exec.LookPath("ffprobe")
	if err != nil {
		return ""
	}
	out, err := exec.CommandContext(ctx, fp, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path).Output()
	if err != nil {
		return ""
	}
	sec, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%.1f s", sec)
}

// toMP3 converts src to dst with ffmpeg.
func toMP3(ctx context.Context, src, dst string) error {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		return errors.New("MP3 output needs ffmpeg on PATH (brew install ffmpeg); ask for .wav instead")
	}
	out, err := exec.CommandContext(ctx, ff, "-v", "error", "-y", "-i", src, "-codec:a", "libmp3lame", "-q:a", "2", dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
