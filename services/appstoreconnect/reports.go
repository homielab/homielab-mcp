package appstoreconnect

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/homielab/homielab-mcp/internal/googleapi"
	"github.com/homielab/homielab-mcp/internal/render"
)

// fetchReport GETs a sales or finance report (gzip TSV) and returns the TSV.
func (c *ascClient) fetchReport(ctx context.Context, path string, q url.Values) (string, error) {
	body, _, err := c.DoHeader(ctx, http.MethodGet, c.url(path, q), nil, http.Header{"Accept": {"application/a-gzip"}})
	if err != nil {
		return "", err
	}
	return gunzipMaybe(body)
}

// gunzipMaybe inflates gzip data; other data is returned as is.
func gunzipMaybe(data []byte) (string, error) {
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return string(data), nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, googleapi.MaxResponseBody))
	if err != nil {
		return "", fmt.Errorf("inflating report: %w", err)
	}
	return string(out), nil
}

// table is a parsed TSV report.
type table struct {
	Header []string
	Rows   [][]string
}

func parseTSV(tsv string) table {
	var t table
	for i, line := range strings.Split(strings.TrimRight(tsv, "\r\n"), "\n") {
		cells := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if i == 0 {
			t.Header = cells
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		t.Rows = append(t.Rows, cells)
	}
	return t
}

func (t table) col(name string) int {
	return slices.IndexFunc(t.Header, func(h string) bool { return strings.EqualFold(strings.TrimSpace(h), name) })
}

func (t table) csv() string {
	var b strings.Builder
	b.WriteString(render.CSVLine(t.Header))
	for _, r := range t.Rows {
		b.WriteString(render.CSVLine(r))
	}
	return b.String()
}

func cell(r []string, i int) string {
	if i < 0 || i >= len(r) {
		return ""
	}
	return strings.TrimSpace(r[i])
}

func num(s string) float64 {
	f, _ := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
	return f
}

// productTypes groups Apple's Product Type Identifier codes.
func productType(code string) string {
	switch code {
	case "1", "1F", "1T", "1-B", "1E", "1EP", "1EU", "F1", "F1-B":
		return "app downloads"
	case "3", "3F", "3T", "3-B", "F3":
		return "re-downloads"
	case "7", "7F", "7T", "7-B", "F7":
		return "updates"
	case "IA1", "IA9", "IAY", "IAC", "IA1-M", "IA9-M", "IAY-M", "FI1":
		return "in-app purchases"
	case "1-A", "F1-A":
		return "app bundles"
	}
	return "other (" + code + ")"
}

type kv struct {
	Key string
	Val float64
}

func sorted(m map[string]float64) []kv {
	out := make([]kv, 0, len(m))
	for k, v := range m {
		out = append(out, kv{k, v})
	}
	slices.SortFunc(out, func(a, b kv) int {
		if a.Val != b.Val {
			if a.Val > b.Val {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out
}

func fmtNum(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', 2, 64)
}

// renderSalesSummary aggregates a SALES report: units by product type, app
// and country; proceeds by currency (Developer Proceeds is per unit).
func renderSalesSummary(t table, top int) string {
	units, title, ptype, country, cur, proceeds := t.col("Units"), t.col("Title"), t.col("Product Type Identifier"), t.col("Country Code"), t.col("Currency of Proceeds"), t.col("Developer Proceeds")
	if units < 0 {
		return ""
	}
	byType, byTitle, byCountry, byCur := map[string]float64{}, map[string]float64{}, map[string]float64{}, map[string]float64{}
	var total float64
	for _, r := range t.Rows {
		u := num(cell(r, units))
		total += u
		if ptype >= 0 {
			byType[productType(cell(r, ptype))] += u
		}
		if title >= 0 {
			byTitle[cell(r, title)] += u
		}
		if country >= 0 {
			byCountry[cell(r, country)] += u
		}
		if cur >= 0 && proceeds >= 0 {
			if p := num(cell(r, proceeds)) * u; p != 0 {
				byCur[cell(r, cur)] += p
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Total units: %s\n", fmtNum(total))
	section := func(name string, m map[string]float64) {
		if len(m) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s:\n", name)
		for i, x := range sorted(m) {
			if i == top {
				fmt.Fprintf(&b, "  … %d more\n", len(m)-top)
				break
			}
			fmt.Fprintf(&b, "  %-30s %s\n", render.OrDash(x.Key), fmtNum(x.Val))
		}
	}
	section("Units by type", byType)
	section("Units by app/product", byTitle)
	section("Units by country", byCountry)
	section("Developer proceeds by currency", byCur)
	return b.String()
}

// renderTablePreview shows the first rows as a markdown table, keeping only
// columns that have a value in the preview.
func renderTablePreview(t table, rows int) string {
	n := min(rows, len(t.Rows))
	var keep []int
	for i := range t.Header {
		for _, r := range t.Rows[:n] {
			if cell(r, i) != "" {
				keep = append(keep, i)
				break
			}
		}
	}
	if len(keep) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("|")
	for _, i := range keep {
		b.WriteString(" " + render.MDCell(t.Header[i]) + " |")
	}
	b.WriteString("\n|" + strings.Repeat(" --- |", len(keep)) + "\n")
	for _, r := range t.Rows[:n] {
		b.WriteString("|")
		for _, i := range keep {
			b.WriteString(" " + render.MDCell(cell(r, i)) + " |")
		}
		b.WriteString("\n")
	}
	if more := len(t.Rows) - n; more > 0 {
		fmt.Fprintf(&b, "\n… %d more rows (pass saveToPath for the full report)\n", more)
	}
	return b.String()
}
