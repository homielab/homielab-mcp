// Package daterange resolves the report date presets shared by the servers
// (TODAY, LAST_30_DAYS, ...) into inclusive [start, end] days and derives
// comparison windows. Days are midnight UTC values of the calendar date.
package daterange

import (
	"fmt"
	"strings"
	"time"
)

// Layout is the YYYY-MM-DD date format used by every API in the collection.
const Layout = time.DateOnly

// Presets lists the accepted preset names; "" means LAST_30_DAYS.
const Presets = "TODAY, YESTERDAY, LAST_7_DAYS, LAST_30_DAYS, MONTH_TO_DATE, LAST_MONTH, YEAR_TO_DATE"

// Valid reports whether s is a real YYYY-MM-DD date.
func Valid(s string) bool {
	_, err := time.Parse(Layout, s)
	return err == nil
}

// Preset returns the window of a named preset relative to now's calendar
// date. LAST_N_DAYS exclude today, matching the AdSense and Search Console APIs.
func Preset(name string, now time.Time) (start, end time.Time, err error) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	firstOfMonth := today.AddDate(0, 0, 1-today.Day())
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "TODAY":
		return today, today, nil
	case "YESTERDAY":
		return yesterday, yesterday, nil
	case "LAST_7_DAYS":
		return today.AddDate(0, 0, -7), yesterday, nil
	case "", "LAST_30_DAYS":
		return today.AddDate(0, 0, -30), yesterday, nil
	case "MONTH_TO_DATE":
		return firstOfMonth, today, nil
	case "LAST_MONTH":
		return firstOfMonth.AddDate(0, -1, 0), firstOfMonth.AddDate(0, 0, -1), nil
	case "YEAR_TO_DATE":
		return time.Date(today.Year(), 1, 1, 0, 0, 0, 0, time.UTC), today, nil
	}
	return time.Time{}, time.Time{}, fmt.Errorf("unknown dateRange %q; use %s, or startDate + endDate (YYYY-MM-DD)", name, Presets)
}

// Resolve returns explicit startDate/endDate when either is given (both must
// be valid, start <= end), otherwise the preset window.
func Resolve(preset, startDate, endDate string, now time.Time) (start, end time.Time, err error) {
	if startDate == "" && endDate == "" {
		return Preset(preset, now)
	}
	s, err1 := time.Parse(Layout, startDate)
	e, err2 := time.Parse(Layout, endDate)
	if err1 != nil || err2 != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("startDate and endDate must both be YYYY-MM-DD dates, got %q and %q", startDate, endDate)
	}
	if e.Before(s) {
		return time.Time{}, time.Time{}, fmt.Errorf("endDate %s is before startDate %s", endDate, startDate)
	}
	return s, e, nil
}

// Previous returns the equal-length window that ends the day before start.
func Previous(start, end time.Time) (time.Time, time.Time) {
	days := int(end.Sub(start).Hours()/24) + 1
	prevEnd := start.AddDate(0, 0, -1)
	return prevEnd.AddDate(0, 0, 1-days), prevEnd
}

// PreviousStrings is Previous for YYYY-MM-DD strings.
func PreviousStrings(start, end string) (string, string, error) {
	s, e, err := Resolve("", start, end, time.Time{})
	if err != nil {
		return "", "", err
	}
	ps, pe := Previous(s, e)
	return ps.Format(Layout), pe.Format(Layout), nil
}
