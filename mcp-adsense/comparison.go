package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/homielab/homielab-mcp/internal/render"
)

// ---- period-over-period comparison for AdSense reports ----

// parseMetricCell converts a report cell ("$1,234.56", "59.91%", "395398")
// into a float, tolerating currency symbols, group separators and percent
// signs. Unparseable cells count as 0.
func parseMetricCell(s string) float64 {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '$', ',', '%', ' ':
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

type periodRow struct {
	keys []string
	prev []float64 // one value per metric column
	curr []float64
}

// mergeReportPeriods joins two report results on their dimension columns.
// nDims is the number of leading columns that form the key; the rest are
// metric columns. Rows missing from one period get zeros for that period.
func mergeReportPeriods(prev, curr reportResult, nDims int) []periodRow {
	type pair struct{ p, c []float64 }
	index := map[string]*pair{}
	var order []string

	values := func(r reportResult, isCurr bool) {
		for _, row := range r.Rows {
			if len(row) < nDims {
				continue
			}
			key := strings.Join(row[:nDims], "\x1f")
			metrics := make([]float64, 0, len(row)-nDims)
			for _, cell := range row[nDims:] {
				metrics = append(metrics, parseMetricCell(cell))
			}
			p, ok := index[key]
			if !ok {
				p = &pair{p: make([]float64, len(r.Headers)-nDims), c: make([]float64, len(r.Headers)-nDims)}
				index[key] = p
				order = append(order, key)
			}
			for i, v := range metrics {
				if i >= len(p.p) || i >= len(p.c) {
					break
				}
				if isCurr {
					p.c[i] = v
				} else {
					p.p[i] = v
				}
			}
		}
	}
	values(prev, false)
	values(curr, true)

	out := make([]periodRow, 0, len(order))
	for _, k := range order {
		p := index[k]
		out = append(out, periodRow{
			keys: strings.Split(k, "\x1f"),
			prev: p.p,
			curr: p.c,
		})
	}
	// biggest absolute change of the FIRST metric first (typically earnings)
	sort.SliceStable(out, func(i, j int) bool {
		return abs(out[i].curr[0]-out[i].prev[0]) > abs(out[j].curr[0]-out[j].prev[0])
	})
	return out
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func fmtMetric(v float64) string {
	if v == 0 {
		return "0"
	}
	if abs(v) >= 1000 {
		return fmt.Sprintf("%.0f", v)
	}
	// Trim trailing zeros but never leave a bare dot ("3.00" → "3").
	s := strings.TrimRight(fmt.Sprintf("%.2f", v), "0")
	return strings.TrimSuffix(s, ".")
}

// renderPeriodComparison writes the delta table. metricNames are the header
// names of the metric columns.
func renderPeriodComparison(rows []periodRow, metricNames []string, maxRows int) string {
	headers := make([]string, 0, 2+len(metricNames)*3)
	for i := range metricNames {
		headers = append(headers,
			fmt.Sprintf("%s before", metricNames[i]),
			fmt.Sprintf("%s after", metricNames[i]),
			fmt.Sprintf("Δ %s", metricNames[i]))
	}
	var b strings.Builder
	b.WriteString("| Key | " + strings.Join(headers, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat(" --- |", len(headers)+1) + "\n")
	shown := len(rows)
	if shown > maxRows {
		shown = maxRows
	}
	for _, r := range rows[:shown] {
		cells := make([]string, 0, len(headers))
		for _, k := range r.keys {
			cells = append(cells, render.MDCell(k))
		}
		for i := range metricNames {
			cells = append(cells,
				fmtMetric(r.prev[i]),
				fmtMetric(r.curr[i]),
				fmt.Sprintf("%+.2f", r.curr[i]-r.prev[i]))
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	if len(rows) > shown {
		fmt.Fprintf(&b, "\n(%d more rows omitted — sorted by absolute change of %s.)\n", len(rows)-shown, metricNames[0])
	}
	return b.String()
}
