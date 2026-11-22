package adsense

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/homielab/homielab-mcp/internal/googleapi"
)

var apiBase = "https://adsense.googleapis.com/v2"

type adsenseClient struct {
	*googleapi.Client
}

func newAdsenseClient(ctx context.Context) (*adsenseClient, error) {
	hc, err := auth.HTTPClient(ctx)
	if err != nil {
		return nil, err
	}
	return &adsenseClient{googleapi.New(hc, "AdSense API")}, nil
}

// normalizeAccount accepts "pub-123", "accounts/pub-123" or "" and returns
// the resource name used in URL paths.
func normalizeAccount(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "/")
	if id == "" {
		return ""
	}
	if strings.HasPrefix(id, "accounts/") {
		return id
	}
	return "accounts/" + id
}

// get fetches path (relative to apiBase) with query q.
func (c *adsenseClient) get(ctx context.Context, path string, q url.Values) ([]byte, int, error) {
	u := googleapi.JoinURL(apiBase, path)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return c.Do(ctx, http.MethodGet, u, nil)
}

func (c *adsenseClient) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	body, _, err := c.get(ctx, path, q)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding response of %s: %w", path, err)
	}
	return nil
}

// listAll walks a paginated collection endpoint, extracting the array stored
// under itemsKey (e.g. "accounts", "sites", "adUnits") from every page.
func listAll[T any](ctx context.Context, c *adsenseClient, path, itemsKey string, q url.Values) ([]T, error) {
	return googleapi.ListAll[T](ctx, c.Client, googleapi.JoinURL(apiBase, path), q, itemsKey)
}

// ---- API resource types (only fields we need) ----

type account struct {
	Name        string `json:"name"` // "accounts/pub-XXXX"
	DisplayName string `json:"displayName"`
	Premium     bool   `json:"premium"`
	TimeZone    struct {
		ID string `json:"id"` // IANA name, e.g. "America/Los_Angeles"
	} `json:"timeZone"`
}

type site struct {
	Name                 string `json:"name"`
	Domain               string `json:"domain"`
	ReportingDimensionID string `json:"reportingDimensionId"`
	State                string `json:"state"`
	AutoAdsEnabled       bool   `json:"autoAdsEnabled"`
}

type adClient struct {
	Name                 string `json:"name"`
	ReportingDimensionID string `json:"reportingDimensionId"` // ca-pub-XXXX; empty for some clients
	ProductCode          string `json:"productCode"`          // AFC, AFS, AFC_HOST...
	State                string `json:"state"`                // READY, GETTING_READY, REQUIRES_REVIEW
}

// id is the client's short id (ca-pub-XXXX), from its resource name.
func (a adClient) id() string { return a.Name[strings.LastIndex(a.Name, "/")+1:] }

type adUnit struct {
	Name                 string `json:"name"` // .../adclients/ca-pub-X/adunits/1234567890
	DisplayName          string `json:"displayName"`
	ReportingDimensionID string `json:"reportingDimensionId"` // ca-pub-X:1234567890, the AD_UNIT_ID dimension
	State                string `json:"state"`
	ContentAdsSettings   struct {
		Type string `json:"type"` // DISPLAY, FEED, ARTICLE, MATCHED_CONTENT, LINK
		Size string `json:"size"`
	} `json:"contentAdsSettings"`
}

// code is the unit's numeric id, from its resource name.
func (u adUnit) code() string { return u.Name[strings.LastIndex(u.Name, "/")+1:] }

type payment struct {
	Name   string   `json:"name"`   // .../payments/unpaid or .../payments/20261021
	Date   *apiDate `json:"date"`   // set for paid payments only
	Amount string   `json:"amount"` // formatted with currency, e.g. "$336.92"
}

type alert struct {
	Name     string `json:"name"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
	Type     string `json:"type"`
}

type apiDate struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

func (d apiDate) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

type reportHeader struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	CurrencyCode string `json:"currencyCode,omitempty"`
}

type reportResult struct {
	TotalMatchedRows string         `json:"totalMatchedRows"`
	Headers          []reportHeader `json:"headers"`
	Rows             [][]string     `json:"rows"`
	Totals           []string       `json:"totals"`
	Averages         []string       `json:"averages"`
	StartDate        apiDate        `json:"startDate"`
	EndDate          apiDate        `json:"endDate"`
}

// UnmarshalJSON decodes the API's row shape, {"cells": [{"value": "..."}]},
// into plain string rows.
func (r *reportResult) UnmarshalJSON(b []byte) error {
	type row struct {
		Cells []struct {
			Value string `json:"value"`
		} `json:"cells"`
	}
	var w struct {
		TotalMatchedRows string         `json:"totalMatchedRows"`
		Headers          []reportHeader `json:"headers"`
		Rows             []row          `json:"rows"`
		Totals           *row           `json:"totals"`
		Averages         *row           `json:"averages"`
		StartDate        apiDate        `json:"startDate"`
		EndDate          apiDate        `json:"endDate"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	values := func(r *row) []string {
		if r == nil {
			return nil
		}
		out := make([]string, len(r.Cells))
		for i, c := range r.Cells {
			out[i] = c.Value
		}
		return out
	}
	*r = reportResult{TotalMatchedRows: w.TotalMatchedRows, Headers: w.Headers,
		Totals: values(w.Totals), Averages: values(w.Averages), StartDate: w.StartDate, EndDate: w.EndDate}
	for i := range w.Rows {
		r.Rows = append(r.Rows, values(&w.Rows[i]))
	}
	return nil
}

// ---- convenience wrappers ----

func (c *adsenseClient) listAccounts(ctx context.Context) ([]account, error) {
	return listAll[account](ctx, c, "/accounts", "accounts", url.Values{"pageSize": {"1000"}})
}

func (c *adsenseClient) defaultAccountID(ctx context.Context) (string, error) {
	accs, err := c.listAccounts(ctx)
	if err != nil {
		return "", err
	}
	if len(accs) == 0 {
		return "", fmt.Errorf("no AdSense accounts accessible for this Google login")
	}
	return accs[0].Name, nil
}

func (c *adsenseClient) resolveAccount(ctx context.Context, accountID string) (string, error) {
	if acc := normalizeAccount(accountID); acc != "" {
		return acc, nil
	}
	return c.defaultAccountID(ctx)
}

func (c *adsenseClient) listSites(ctx context.Context, acc string) ([]site, error) {
	return listAll[site](ctx, c, acc+"/sites", "sites", url.Values{"pageSize": {"1000"}})
}

func (c *adsenseClient) listAdClients(ctx context.Context, acc string) ([]adClient, error) {
	return listAll[adClient](ctx, c, acc+"/adclients", "adClients", url.Values{"pageSize": {"1000"}})
}

func (c *adsenseClient) listAdUnits(ctx context.Context, adClientName string) ([]adUnit, error) {
	return listAll[adUnit](ctx, c, adClientName+"/adunits", "adUnits", url.Values{"pageSize": {"1000"}})
}

type savedReport struct {
	Name  string `json:"name"` // accounts/pub-X/reports/saved/1234
	Title string `json:"title"`
	ID    string `json:"id"`
}

func (c *adsenseClient) listSavedReports(ctx context.Context, acc string) ([]savedReport, error) {
	return listAll[savedReport](ctx, c, acc+"/reports/saved", "reports", url.Values{"pageSize": {"1000"}})
}

// resolveSavedReport accepts a full resource name, a numeric id, or "" (with a
// pre-resolved account) and returns the path used by saved:generate.
func (c *adsenseClient) resolveSavedReport(ctx context.Context, acc, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("savedReportId is required — call adsense_list_saved_reports first")
	}
	if strings.HasPrefix(id, "accounts/") {
		return id, nil
	}
	if acc == "" {
		var err error
		if acc, err = c.defaultAccountID(ctx); err != nil {
			return "", err
		}
	}
	return acc + "/reports/saved/" + id, nil
}

// generateSavedReport runs a user-saved report; q may carry dateRange/startDate/
// endDate/currencyCode/reportingTimeZone/languageCode overrides only.
func (c *adsenseClient) generateSavedReport(ctx context.Context, name string, q url.Values, csv bool) (reportResult, string, error) {
	var res reportResult
	method := "saved:generate"
	if csv {
		method = "saved:generateCsv"
	}
	if csv {
		body, _, err := c.get(ctx, name+"/"+method, q)
		if err != nil {
			return res, "", err
		}
		return res, string(body), nil
	}
	if err := c.getJSON(ctx, name+"/"+method, q, &res); err != nil {
		return res, "", err
	}
	return res, "", nil
}

// rawItems fetches a collection and returns its raw JSON items under the given
// key, for endpoints whose schemas we render generically (policy issues, ...).
func (c *adsenseClient) rawItems(ctx context.Context, path, itemsKey string, q url.Values) ([]json.RawMessage, error) {
	return listAll[json.RawMessage](ctx, c, path, itemsKey, q)
}

func (c *adsenseClient) listChildAccounts(ctx context.Context, acc string) ([]account, error) {
	return listAll[account](ctx, c, acc+":listChildAccounts", "childAccounts", url.Values{"pageSize": {"1000"}})
}

type channel struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	URIPattern  string `json:"uriPattern"`
}

func (c *adsenseClient) listCustomChannels(ctx context.Context, adClientName string) ([]channel, error) {
	return listAll[channel](ctx, c, adClientName+"/customchannels", "customChannels", url.Values{"pageSize": {"1000"}})
}

func (c *adsenseClient) listURLChannels(ctx context.Context, adClientName string) ([]channel, error) {
	return listAll[channel](ctx, c, adClientName+"/urlchannels", "urlChannels", url.Values{"pageSize": {"1000"}})
}

// listLinkedCustomChannels returns the custom channels an ad unit belongs to
// (custom method GET {adUnit}:listLinkedCustomChannels).
func (c *adsenseClient) listLinkedCustomChannels(ctx context.Context, adUnitName string) ([]channel, error) {
	return listAll[channel](ctx, c, adUnitName+":listLinkedCustomChannels", "customChannels", url.Values{"pageSize": {"10000"}})
}

// listLinkedAdUnits returns the ad units belonging to a custom channel
// (custom method GET {customChannel}:listLinkedAdUnits).
func (c *adsenseClient) listLinkedAdUnits(ctx context.Context, customChannelName string) ([]adUnit, error) {
	return listAll[adUnit](ctx, c, customChannelName+":listLinkedAdUnits", "adUnits", url.Values{"pageSize": {"10000"}})
}

// adCode fetches the ad code snippet of one ad unit.
func (c *adsenseClient) adCode(ctx context.Context, adUnitName string) (string, error) {
	var out struct {
		AdCode string `json:"adCode"`
	}
	if err := c.getJSON(ctx, adUnitName+"/adcode", nil, &out); err != nil {
		return "", err
	}
	return out.AdCode, nil
}

func (c *adsenseClient) listPayments(ctx context.Context, acc string) ([]payment, error) {
	return listAll[payment](ctx, c, acc+"/payments", "payments", nil)
}

func (c *adsenseClient) listAlerts(ctx context.Context, acc string) ([]alert, error) {
	return listAll[alert](ctx, c, acc+"/alerts", "alerts", url.Values{"languageCode": {"en"}})
}

// generateReport calls reports:generate (format "json") or reports:generateCsv.
func (c *adsenseClient) generateReport(ctx context.Context, acc string, q url.Values, csv bool) (reportResult, string, error) {
	var res reportResult
	method := "reports:generate"
	if csv {
		method = "reports:generateCsv"
	}
	if csv {
		body, _, err := c.get(ctx, acc+"/"+method, q)
		if err != nil {
			return res, "", err
		}
		return res, string(body), nil
	}
	if err := c.getJSON(ctx, acc+"/"+method, q, &res); err != nil {
		return res, "", err
	}
	return res, "", nil
}
