package daterange

import (
	"testing"
	"time"
)

func TestPreset(t *testing.T) {
	now := time.Date(2026, 3, 12, 23, 30, 0, 0, time.FixedZone("PT", -7*3600))
	cases := map[string][2]string{
		"TODAY":         {"2026-03-12", "2026-03-12"},
		"yesterday":     {"2026-03-11", "2026-03-11"},
		"LAST_7_DAYS":   {"2026-03-05", "2026-03-11"},
		"":              {"2026-02-10", "2026-03-11"},
		"LAST_30_DAYS":  {"2026-02-10", "2026-03-11"},
		"MONTH_TO_DATE": {"2026-03-01", "2026-03-12"},
		"LAST_MONTH":    {"2026-02-01", "2026-02-28"},
		"YEAR_TO_DATE":  {"2026-01-01", "2026-03-12"},
	}
	for name, want := range cases {
		s, e, err := Preset(name, now)
		if err != nil || s.Format(Layout) != want[0] || e.Format(Layout) != want[1] {
			t.Errorf("Preset(%q) = %s → %s, %v; want %v", name, s.Format(Layout), e.Format(Layout), err, want)
		}
	}
	if _, _, err := Preset("FOREVER", now); err == nil {
		t.Error("unknown preset should fail")
	}
}

func TestResolve(t *testing.T) {
	s, e, err := Resolve("LAST_7_DAYS", "2026-01-02", "2026-01-31", time.Now())
	if err != nil || s.Format(Layout) != "2026-01-02" || e.Format(Layout) != "2026-01-31" {
		t.Errorf("explicit dates must win: %s → %s, %v", s, e, err)
	}
	for _, bad := range [][2]string{{"2026-01-02", ""}, {"2026-13-01", "2026-12-31"}, {"2026-02-01", "2026-01-01"}} {
		if _, _, err := Resolve("", bad[0], bad[1], time.Now()); err == nil {
			t.Errorf("Resolve(%q, %q) should fail", bad[0], bad[1])
		}
	}
}

func TestPrevious(t *testing.T) {
	ps, pe, err := PreviousStrings("2026-09-27", "2026-10-26") // 30 days inclusive
	if err != nil || ps != "2026-08-28" || pe != "2026-09-26" {
		t.Errorf("PreviousStrings = %s → %s, %v", ps, pe, err)
	}
	if _, _, err := PreviousStrings("oops", "2026-10-26"); err == nil {
		t.Error("bad date should fail")
	}
}

func TestValid(t *testing.T) {
	for s, want := range map[string]bool{"2026-10-28": true, "2026-02-30": false, "2026-1-02": false, "": false} {
		if Valid(s) != want {
			t.Errorf("Valid(%q) = %v", s, !want)
		}
	}
}
