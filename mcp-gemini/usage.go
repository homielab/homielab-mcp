package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// Prices: https://ai.google.dev/gemini-api/docs/pricing (paid tier, checked
// 2026-11-10). Costs are estimates from the response's token usage.

var (
	ratiosBase = []string{"1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"}
	ratiosWide = append(slices.Clone(ratiosBase), "1:4", "4:1", "1:8", "8:1")
)

type imageModel struct {
	ID       string
	Sizes    []string           // supported image_size values; first = default
	Prices   map[string]float64 // USD per output image by size
	Ratios   []string           // supported aspect_ratio values
	Thinking []string           // supported thinking_level values (none = not settable)
	Search   []string           // supported google_search search_types
	PerMText float64            // USD per 1M text + thinking output tokens
}

var imageModels = map[string]imageModel{
	"flash": {"gemini-nano-banana-2.1", []string{"1K", "2K", "4K"}, map[string]float64{"1K": 0.0336, "2K": 0.0504, "4K": 0.113},
		ratiosWide, []string{"minimal", "medium", "high"}, []string{"web", "image"}, 7.5},
	"lite": {"gemini-3.1-flash-lite-image", []string{"1K"}, map[string]float64{"1K": 0.0336},
		ratiosBase, []string{"minimal", "high"}, nil, 1.5},
	"pro": {"gemini-3-pro-image", []string{"1K", "2K", "4K"}, map[string]float64{"1K": 0.134, "2K": 0.134, "4K": 0.24},
		ratiosBase, nil, []string{"web"}, 12},
}

// cost is the per-image price plus text and thinking output tokens.
func (m imageModel) cost(size string, images, textTokens int) float64 {
	return m.Prices[size]*float64(images) + float64(textTokens)*m.PerMText/1e6
}

type ttsModel struct {
	ID       string
	PerMOut  float64 // USD per 1M audio output tokens until 2026-12-31
	PerMOut2 float64 // from 2027-01-01
}

var ttsModels = map[string]ttsModel{
	"flash": {"gemini-3.8-flash-tts", 9, 18},
	"lite":  {"gemini-3.8-flash-lite-tts", 6, 12},
}

func (m ttsModel) cost(outTokens int, at time.Time) float64 {
	rate := m.PerMOut
	if !at.Before(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		rate = m.PerMOut2
	}
	return float64(outTokens) * rate / 1e6
}

const (
	musicClipModel = "lyria-3-clip-preview"
	musicFullModel = "lyria-3.5"
	musicClipPrice = 0.04
	musicFullPrice = 0.08

	videoModel       = "gemini-omni-1.1-flash"
	videoPerMOut     = 17.5 // USD per 1M video output tokens (~5,792 tokens per second at 720p)
	videoPerMThought = 9.0  // text/thinking output
)

func videoCost(videoTokens, thoughtTokens int) float64 {
	return float64(videoTokens)*videoPerMOut/1e6 + float64(thoughtTokens)*videoPerMThought/1e6
}

// ---- spend ledger: <config dir>/mcp-gemini/usage.jsonl ----

type usageEntry struct {
	Time  time.Time `json:"time"`
	Tool  string    `json:"tool"`
	Model string    `json:"model"`
	Cost  float64   `json:"cost"`
	File  string    `json:"file,omitempty"`
	Dir   string    `json:"dir,omitempty"` // working directory = which workspace spent it
}

func ledgerPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "usage.jsonl"), nil
}

// record appends one call to the ledger; failures only lose the record.
func record(tool, model string, cost float64, file string) {
	p, err := ledgerPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	wd, _ := os.Getwd()
	line, _ := json.Marshal(usageEntry{Time: nowFunc(), Tool: tool, Model: model, Cost: cost, File: file, Dir: wd})
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}

func readLedger() ([]usageEntry, error) {
	p, err := ledgerPath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []usageEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e usageEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// usageReport sums the ledger for the last days (by day, tool and workspace).
func usageReport(entries []usageEntry, days int, now time.Time) string {
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(days - 1))
	byDay := map[string]float64{}
	byTool := map[string][2]float64{}
	byDir := map[string]float64{}
	var total float64
	var n int
	for _, e := range entries {
		t := e.Time.In(now.Location())
		if t.Before(from) {
			continue
		}
		n++
		total += e.Cost
		byDay[t.Format("2006-01-02")] += e.Cost
		v := byTool[e.Tool]
		byTool[e.Tool] = [2]float64{v[0] + 1, v[1] + e.Cost}
		dir := e.Dir
		if dir == "" {
			dir = "?"
		}
		byDir[dir] += e.Cost
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Gemini media spend, last %d day(s) (estimates): $%.2f in %d call(s)\n", days, total, n)
	if n == 0 {
		return b.String()
	}
	b.WriteString("\nBy day:\n")
	for _, k := range sortedKeys(byDay) {
		fmt.Fprintf(&b, "  %s  $%.2f\n", k, byDay[k])
	}
	b.WriteString("\nBy tool:\n")
	tools := make([]string, 0, len(byTool))
	for k := range byTool {
		tools = append(tools, k)
	}
	sort.Strings(tools)
	for _, k := range tools {
		fmt.Fprintf(&b, "  %-14s %3.0f call(s)  $%.2f\n", k, byTool[k][0], byTool[k][1])
	}
	b.WriteString("\nBy working directory:\n")
	for _, k := range sortedKeys(byDir) {
		fmt.Fprintf(&b, "  $%6.2f  %s\n", byDir[k], k)
	}
	return b.String()
}

func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
