package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/homielab/homielab-mcp/internal/googleapi"
)

func withTestServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	old := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = old })
}

func TestGetJSONSuccess(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/pub-1" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Write([]byte(`{"name":"accounts/pub-1","displayName":"My Account","premium":true}`))
	})
	c := &adsenseClient{googleapi.New(&http.Client{Timeout: 5 * time.Second}, "AdSense API")}
	var out account
	if err := c.getJSON(context.Background(), "/accounts/pub-1", nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.DisplayName != "My Account" || !out.Premium || out.Name != "accounts/pub-1" {
		t.Errorf("decoded %+v", out)
	}
}

func TestGetErrorMessageMapping(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"code":403,"message":"User does not have an AdSense account","status":"PERMISSION_DENIED"}}`))
	})
	c := &adsenseClient{googleapi.New(&http.Client{Timeout: 5 * time.Second}, "AdSense API")}
	_, _, err := c.get(context.Background(), "/accounts/pub-1/reports:generate", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "403 (PERMISSION_DENIED)") || !strings.Contains(err.Error(), "User does not have an AdSense account") {
		t.Errorf("error not mapped well: %v", err)
	}
}

func TestListAllPagination(t *testing.T) {
	var calls int32
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch atomic.AddInt32(&calls, 1) {
		case 1:
			if r.URL.Query().Get("pageToken") != "" {
				t.Error("first page must not carry pageToken")
			}
			w.Write([]byte(`{"adClients":[{"name":"accounts/pub-1/adclients/ca-pub-1","productCode":"AFC"}],"nextPageToken":"p2"}`))
		case 2:
			if got := r.URL.Query().Get("pageToken"); got != "p2" {
				t.Errorf("second page pageToken = %q, want p2", got)
			}
			w.Write([]byte(`{"adClients":[{"name":"accounts/pub-1/adclients/ca-pub-2","productCode":"AFS"}]}`))
		default:
			t.Error("too many pages requested")
		}
	})
	c := &adsenseClient{googleapi.New(&http.Client{Timeout: 5 * time.Second}, "AdSense API")}
	acs, err := c.listAdClients(context.Background(), "accounts/pub-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(acs) != 2 || acs[0].ProductCode != "AFC" || acs[1].ProductCode != "AFS" {
		t.Errorf("paginated results wrong: %+v", acs)
	}
}

func TestRetryOnServerError(t *testing.T) {
	old := googleapi.RetryBackoff
	googleapi.RetryBackoff = time.Millisecond
	t.Cleanup(func() { googleapi.RetryBackoff = old })

	var calls int32
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":{"message":"backend error","status":"INTERNAL"}}`))
			return
		}
		w.Write([]byte(`{"totalMatchedRows":"0","headers":[],"rows":[]}`))
	})
	c := &adsenseClient{googleapi.New(&http.Client{Timeout: 5 * time.Second}, "AdSense API")}
	r, _, err := c.generateReport(context.Background(), "accounts/pub-1", nil, false)
	if err != nil {
		t.Fatalf("retry should have recovered, got %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("calls = %d, want 2 (one failure + one success)", calls)
	}
	if r.TotalMatchedRows != "0" {
		t.Errorf("decoded %+v", r)
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	var calls int32
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"bad request","status":"INVALID_ARGUMENT"}}`))
	})
	c := &adsenseClient{googleapi.New(&http.Client{Timeout: 5 * time.Second}, "AdSense API")}
	_, _, err := c.get(context.Background(), "/x", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("client errors must not be retried, calls = %d", calls)
	}
}

func TestLinkedCustomChannelsURL(t *testing.T) {
	var gotPath string
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"customChannels":[{"name":"accounts/pub-1/adclients/ca-pub-1/customchannels/CC1","displayName":"Slot top"}]}`))
	})
	c := &adsenseClient{googleapi.New(&http.Client{Timeout: 5 * time.Second}, "AdSense API")}
	chans, err := c.listLinkedCustomChannels(context.Background(), "accounts/pub-1/adclients/ca-pub-1/adunits/U1")
	if err != nil {
		t.Fatal(err)
	}
	want := "/accounts/pub-1/adclients/ca-pub-1/adunits/U1:listLinkedCustomChannels"
	if gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if len(chans) != 1 || chans[0].DisplayName != "Slot top" {
		t.Errorf("decoded %+v", chans)
	}
}

func TestListLinkedAdUnitsURL(t *testing.T) {
	var gotPath string
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"adUnits":[{"name":"accounts/pub-1/adclients/ca-pub-1/adunits/U9","displayName":"Unit 9"}]}`))
	})
	c := &adsenseClient{googleapi.New(&http.Client{Timeout: 5 * time.Second}, "AdSense API")}
	units, err := c.listLinkedAdUnits(context.Background(), "accounts/pub-1/adclients/ca-pub-1/customchannels/CC1")
	if err != nil {
		t.Fatal(err)
	}
	want := "/accounts/pub-1/adclients/ca-pub-1/customchannels/CC1:listLinkedAdUnits"
	if gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if len(units) != 1 || units[0].DisplayName != "Unit 9" {
		t.Errorf("decoded %+v", units)
	}
}

// The API returns timeZone as a TimeZone object, not a string.
func TestAccountDecode(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"accounts":[{"name":"accounts/pub-1","displayName":"Me","timeZone":{"id":"Asia/Ho_Chi_Minh","version":""},"createTime":"2020-01-01T00:00:00Z","state":"READY"}]}`))
	})
	c := &adsenseClient{googleapi.New(&http.Client{Timeout: 5 * time.Second}, "AdSense API")}
	accs, err := c.listAccounts(context.Background())
	if err != nil || len(accs) != 1 || accs[0].TimeZone.ID != "Asia/Ho_Chi_Minh" {
		t.Fatalf("listAccounts = %+v, %v", accs, err)
	}
}

// Real reports:generate shape: rows/totals/averages are {"cells": [{"value"}]}.
func TestReportDecode(t *testing.T) {
	var r reportResult
	err := json.Unmarshal([]byte(`{"totalMatchedRows":"2","headers":[{"name":"DATE","type":"DIMENSION"},{"name":"ESTIMATED_EARNINGS","type":"METRIC_CURRENCY","currencyCode":"USD"}],
		"rows":[{"cells":[{"value":"2026-10-21"},{"value":"1.23"}]},{"cells":[{"value":"2026-10-22"},{"value":"2.00"}]}],
		"totals":{"cells":[{},{"value":"3.23"}]},"averages":{"cells":[{},{"value":"1.62"}]},
		"startDate":{"year":2026,"month":10,"day":21},"endDate":{"year":2026,"month":10,"day":22}}`), &r)
	if err != nil {
		t.Fatal(err)
	}
	if r.TotalMatchedRows != "2" || len(r.Rows) != 2 || r.Rows[1][1] != "2.00" || r.Totals[1] != "3.23" || r.Averages[1] != "1.62" || r.Headers[1].CurrencyCode != "USD" {
		t.Errorf("decoded %+v", r)
	}
}

func TestNormalizeOrderBy(t *testing.T) {
	for in, want := range map[string]string{"-metric.ESTIMATED_EARNINGS": "-ESTIMATED_EARNINGS", "+dimension.DATE": "+DATE", "DATE": "DATE", "-CLICKS": "-CLICKS"} {
		if got := normalizeOrderBy(in); got != want {
			t.Errorf("normalizeOrderBy(%q) = %q, want %q", in, got, want)
		}
	}
}

// Fixtures copied from real responses (ids shortened).
func TestListShapes(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/payments"):
			w.Write([]byte(`{"payments":[{"name":"accounts/pub-1/payments/unpaid","amount":"$0.00"},{"name":"accounts/pub-1/payments/20261021","date":{"year":2026,"month":10,"day":21},"amount":"$336.92"}]}`))
		case strings.HasSuffix(r.URL.Path, "/adclients"):
			w.Write([]byte(`{"adClients":[{"name":"accounts/pub-1/adclients/ca-yt-pub-1","state":"REQUIRES_REVIEW"},{"name":"accounts/pub-1/adclients/ca-pub-1","reportingDimensionId":"ca-pub-1","productCode":"AFC","state":"READY"}]}`))
		case strings.HasSuffix(r.URL.Path, "/adunits"):
			w.Write([]byte(`{"adUnits":[{"name":"accounts/pub-1/adclients/ca-pub-1/adunits/1044231899","reportingDimensionId":"ca-pub-1:1044231899","displayName":"BANNER","state":"ACTIVE","contentAdsSettings":{"size":"1x3","type":"DISPLAY"}}]}`))
		case strings.HasSuffix(r.URL.Path, "/alerts"):
			if r.URL.Query().Get("languageCode") == "" {
				t.Error("alerts need languageCode")
			}
			w.Write([]byte(`{"alerts":[]}`))
		}
	})
	c, ctx := &adsenseClient{googleapi.New(&http.Client{Timeout: 5 * time.Second}, "AdSense API")}, context.Background()
	pays, err := c.listPayments(ctx, "accounts/pub-1")
	if err != nil || len(pays) != 2 || pays[0].Date != nil || pays[1].Amount != "$336.92" || pays[1].Date.String() != "2026-10-21" {
		t.Errorf("payments = %+v, %v", pays, err)
	}
	client, err := firstContentAdClient(ctx, c, "accounts/pub-1")
	if err != nil || client != "accounts/pub-1/adclients/ca-pub-1" {
		t.Errorf("firstContentAdClient = %q, %v", client, err)
	}
	units, err := c.listAdUnits(ctx, client)
	if err != nil || len(units) != 1 || units[0].code() != "1044231899" || units[0].ContentAdsSettings.Type != "DISPLAY" {
		t.Errorf("ad units = %+v, %v", units, err)
	}
	if _, err := c.listAlerts(ctx, "accounts/pub-1"); err != nil {
		t.Error(err)
	}
}
