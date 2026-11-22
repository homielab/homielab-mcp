package admob

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/homielab/homielab-mcp/internal/googleapi"
)

var apiBase = "https://admob.googleapis.com/v1"

type admobClient struct {
	*googleapi.Client
}

func newAdmobClient(ctx context.Context) (*admobClient, error) {
	hc, err := auth.HTTPClient(ctx)
	if err != nil {
		return nil, err
	}
	return &admobClient{googleapi.New(hc, "AdMob API")}, nil
}

// ---- resources ----

type publisherAccount struct {
	Name              string `json:"name"`
	PublisherID       string `json:"publisherId"`
	ReportingTimeZone string `json:"reportingTimeZone"`
	CurrencyCode      string `json:"currencyCode"`
}

type app struct {
	Name          string `json:"name"`
	AppID         string `json:"appId"`
	Platform      string `json:"platform"`
	ManualAppInfo *struct {
		DisplayName string `json:"displayName"`
	} `json:"manualAppInfo"`
	LinkedAppInfo *struct {
		AppStoreID  string `json:"appStoreId"`
		DisplayName string `json:"displayName"`
	} `json:"linkedAppInfo"`
	AppApprovalState string `json:"appApprovalState"`
}

// label is the app's display name: the store listing name when linked,
// otherwise the name typed in the AdMob UI.
func (a app) label() string {
	if a.LinkedAppInfo != nil && a.LinkedAppInfo.DisplayName != "" {
		return a.LinkedAppInfo.DisplayName
	}
	if a.ManualAppInfo != nil && a.ManualAppInfo.DisplayName != "" {
		return a.ManualAppInfo.DisplayName
	}
	return a.AppID
}

type adUnit struct {
	Name        string   `json:"name"`
	AdUnitID    string   `json:"adUnitId"`
	AppID       string   `json:"appId"`
	DisplayName string   `json:"displayName"`
	AdFormat    string   `json:"adFormat"`
	AdTypes     []string `json:"adTypes"`
}

// normalizeAccount accepts "pub-123", "accounts/pub-123" or "" and returns
// the resource name ("" stays "").
func normalizeAccount(id string) string {
	id = strings.Trim(strings.TrimSpace(id), "/")
	if id == "" || strings.HasPrefix(id, "accounts/") {
		return id
	}
	return "accounts/" + id
}

func (c *admobClient) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	u := googleapi.JoinURL(apiBase, path)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	body, _, err := c.Do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding response of %s: %w", path, err)
	}
	return nil
}

// pageSize is the API maximum for apps and ad units.
var pageSize = url.Values{"pageSize": {"20000"}}

func (c *admobClient) listAccounts(ctx context.Context) ([]publisherAccount, error) {
	// The API names the list "account".
	return googleapi.ListAll[publisherAccount](ctx, c.Client, googleapi.JoinURL(apiBase, "/accounts"), pageSize, "account")
}

func (c *admobClient) listApps(ctx context.Context, acc string) ([]app, error) {
	return googleapi.ListAll[app](ctx, c.Client, googleapi.JoinURL(apiBase, "/"+acc+"/apps"), pageSize, "apps")
}

func (c *admobClient) listAdUnits(ctx context.Context, acc string) ([]adUnit, error) {
	return googleapi.ListAll[adUnit](ctx, c.Client, googleapi.JoinURL(apiBase, "/"+acc+"/adUnits"), pageSize, "adUnits")
}

// resolveAccount returns the account for id ("" = the first accessible one).
func (c *admobClient) resolveAccount(ctx context.Context, id string) (publisherAccount, error) {
	name := normalizeAccount(id)
	if name != "" {
		var acc publisherAccount
		err := c.getJSON(ctx, "/"+name, nil, &acc)
		return acc, err
	}
	accs, err := c.listAccounts(ctx)
	if err != nil {
		return publisherAccount{}, err
	}
	if len(accs) == 0 {
		return publisherAccount{}, fmt.Errorf("no AdMob account accessible for this Google login (sign in to AdMob once with it, then retry)")
	}
	return accs[0], nil
}

// ---- reports ----

type apiDate struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

func (d apiDate) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day) }

type reportSpec struct {
	DateRange struct {
		StartDate apiDate `json:"startDate"`
		EndDate   apiDate `json:"endDate"`
	} `json:"dateRange"`
	Dimensions       []string          `json:"dimensions,omitempty"`
	Metrics          []string          `json:"metrics"`
	DimensionFilters []dimensionFilter `json:"dimensionFilters,omitempty"`
	SortConditions   []sortCondition   `json:"sortConditions,omitempty"`
	Localization     *localization     `json:"localizationSettings,omitempty"`
	MaxReportRows    int               `json:"maxReportRows,omitempty"`
}

type dimensionFilter struct {
	Dimension  string `json:"dimension"`
	MatchesAny struct {
		Values []string `json:"values"`
	} `json:"matchesAny"`
}

type sortCondition struct {
	Dimension string `json:"dimension,omitempty"`
	Metric    string `json:"metric,omitempty"`
	Order     string `json:"order"`
}

type localization struct {
	CurrencyCode string `json:"currencyCode,omitempty"`
	LanguageCode string `json:"languageCode,omitempty"`
}

type dimensionValue struct {
	Value        string `json:"value"`
	DisplayLabel string `json:"displayLabel"`
}

type metricValue struct {
	IntegerValue string   `json:"integerValue"`
	DoubleValue  *float64 `json:"doubleValue"`
	MicrosValue  string   `json:"microsValue"`
}

// number returns the metric as a float in natural units (micros → units).
func (m metricValue) number() float64 {
	switch {
	case m.MicrosValue != "":
		n, _ := strconv.ParseFloat(m.MicrosValue, 64)
		return n / 1e6
	case m.DoubleValue != nil:
		return *m.DoubleValue
	default:
		n, _ := strconv.ParseFloat(m.IntegerValue, 64)
		return n
	}
}

type reportRow struct {
	DimensionValues map[string]dimensionValue `json:"dimensionValues"`
	MetricValues    map[string]metricValue    `json:"metricValues"`
}

// reportMessage is one element of the (streamed) report response: exactly one
// of header, row or footer is set.
type reportMessage struct {
	Header *struct {
		Localization *localization `json:"localizationSettings"`
	} `json:"header"`
	Row    *reportRow `json:"row"`
	Footer *struct {
		MatchingRowCount json.RawMessage `json:"matchingRowCount"`
	} `json:"footer"`
}

// isoDate turns the API's "20261022" / "202610" into "2026-10-22" / "2026-10".
func isoDate(v string) string {
	switch len(v) {
	case 8:
		return v[:4] + "-" + v[4:6] + "-" + v[6:]
	case 6:
		return v[:4] + "-" + v[4:]
	}
	return v
}

type report struct {
	Kind     string // "network" or "mediation"
	Spec     reportSpec
	Currency string
	Rows     []reportRow
	Matching string // total rows matching before maxReportRows, as reported
}

// generateReport runs a network or mediation report for acc.
func (c *admobClient) generateReport(ctx context.Context, acc, kind string, spec reportSpec) (report, error) {
	u := googleapi.JoinURL(apiBase, "/"+acc+"/"+kind+"Report:generate")
	body, _, err := c.Do(ctx, http.MethodPost, u, map[string]any{"reportSpec": spec})
	if err != nil {
		return report{}, err
	}
	var msgs []reportMessage
	if err := json.Unmarshal(body, &msgs); err != nil {
		return report{}, fmt.Errorf("decoding %s report: %w", kind, err)
	}
	r := report{Kind: kind, Spec: spec}
	for _, m := range msgs {
		switch {
		case m.Header != nil && m.Header.Localization != nil:
			r.Currency = m.Header.Localization.CurrencyCode
		case m.Row != nil:
			for _, d := range []string{"DATE", "WEEK", "MONTH"} {
				if v, ok := m.Row.DimensionValues[d]; ok {
					v.Value = isoDate(v.Value)
					m.Row.DimensionValues[d] = v
				}
			}
			r.Rows = append(r.Rows, *m.Row)
		case m.Footer != nil:
			r.Matching = strings.Trim(string(m.Footer.MatchingRowCount), `"`)
		}
	}
	return r, nil
}
