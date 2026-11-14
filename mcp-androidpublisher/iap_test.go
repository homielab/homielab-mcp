package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/homielab/homielab-mcp/internal/googleapi"
)

// Shapes below are trimmed copies of real oneTimeProducts / convertRegionPrices responses.
const otpFixture = `{"packageName":"com.x","productId":"pro","listings":[{"languageCode":"en-US","title":"Pro","description":"Pay once."}],
 "purchaseOptions":[{"purchaseOptionId":"default","state":"ACTIVE","buyOption":{"legacyCompatible":true},"offerTags":[{"tag":"keep"}],
  "regionalPricingAndAvailabilityConfigs":[{"regionCode":"US","price":{"currencyCode":"USD","units":"19","nanos":990000000},"availability":"AVAILABLE"},
   {"regionCode":"VN","price":{"currencyCode":"VND","units":"500000"},"availability":"AVAILABLE"}]}],
 "regionsVersion":{"version":"2026/01"}}`

const convertFixture = `{"convertedRegionPrices":{"VN":{"regionCode":"VN","price":{"currencyCode":"VND","units":"77000"},"taxAmount":{"currencyCode":"VND"}},
 "US":{"regionCode":"US","price":{"currencyCode":"USD","units":"2","nanos":990000000}}},
 "convertedOtherRegionsPrice":{"usdPrice":{"currencyCode":"USD","units":"2","nanos":990000000},"eurPrice":{"currencyCode":"EUR","units":"2","nanos":680000000}},
 "regionVersion":{"version":"2026/01"}}`

func TestParseMoney(t *testing.T) {
	m, err := parseMoney("2.99", "usd")
	if err != nil || m != (money{CurrencyCode: "USD", Units: "2", Nanos: 990000000}) || m.String() != "2.99 USD" {
		t.Errorf("parseMoney = %+v (%s), %v", m, m, err)
	}
	if m, _ := parseMoney("77000", "VND"); m.String() != "77000 VND" || m.Nanos != 0 {
		t.Errorf("whole amount = %+v", m)
	}
	for _, bad := range [][2]string{{"0", "USD"}, {"-1", "USD"}, {"abc", "USD"}, {"1", "DOLLAR"}} {
		if _, err := parseMoney(bad[0], bad[1]); err == nil {
			t.Errorf("parseMoney(%q, %q) should fail", bad[0], bad[1])
		}
	}
}

func TestIAPArgsListings(t *testing.T) {
	ls, err := iapWriteArgs{Title: "Coins", Description: "100 coins", Listings: map[string]iapListingArgs{"vi": {Title: "Xu", Description: "100 xu"}}}.listings()
	if err != nil || len(ls) != 2 || ls[0] != (otpListing{"en-US", "Coins", "100 coins"}) || ls[1].LanguageCode != "vi" {
		t.Errorf("listings = %+v, %v", ls, err)
	}
	if _, err := (iapWriteArgs{Title: strings.Repeat("x", 56)}).listings(); err == nil {
		t.Error("56-char title should fail")
	}
	for id, ok := range map[string]bool{"coins_100": true, "app.donate.book": true, "9lives": true, "Coins": false, "_x": false, "": false} {
		if err := (iapWriteArgs{ProductID: id}).validateID(); (err == nil) != ok {
			t.Errorf("validateID(%q) error = %v", id, err)
		}
	}
}

func TestPickPurchaseOptionAndMergeListings(t *testing.T) {
	var raw map[string]any
	json.Unmarshal([]byte(otpFixture), &raw)
	if id, err := pickPurchaseOption(raw, ""); err != nil || id != "default" {
		t.Errorf("pick = %q, %v", id, err)
	}
	if _, err := pickPurchaseOption(raw, "missing"); err == nil {
		t.Error("unknown option should fail")
	}
	merged := mergeListings(raw["listings"], []otpListing{{LanguageCode: "en-US", Title: "Pro 2"}, {LanguageCode: "vi", Title: "Pro", Description: "Trả một lần."}})
	data, _ := json.Marshal(merged)
	if string(data) != `[{"description":"Pay once.","languageCode":"en-US","title":"Pro 2"},{"description":"Trả một lần.","languageCode":"vi","title":"Pro"}]` {
		t.Errorf("merged = %s", data)
	}
}

func TestListOneTimeProductsAndRender(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/applications/com.x/oneTimeProducts" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Write([]byte(`{"oneTimeProducts":[` + otpFixture + `]}`))
	})
	prods, err := testClient().listOneTimeProducts(context.Background(), "com.x")
	if err != nil || len(prods) != 1 {
		t.Fatalf("list = %+v, %v", prods, err)
	}
	out := renderOneTimeProducts("com.x", prods)
	if !strings.Contains(out, "| pro | Pro | default: ACTIVE, buy | 19.99 USD | 2 |") {
		t.Errorf("render:\n%s", out)
	}
}

// Create: existence check (404), price conversion, PATCH with allowMissing and
// the regions version, then activation.
func TestCreateIAP(t *testing.T) {
	var calls []string
	var patched map[string]any
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":404,"message":"not found","status":"NOT_FOUND"}}`))
		case strings.HasSuffix(r.URL.Path, ":convertRegionPrices"):
			w.Write([]byte(convertFixture))
		case r.Method == http.MethodPatch:
			q := r.URL.Query()
			if q.Get("allowMissing") != "true" || q.Get("regionsVersion.version") != "2026/01" {
				t.Errorf("patch query %s", r.URL.RawQuery)
			}
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &patched)
			w.Write(body)
		case strings.HasSuffix(r.URL.Path, ":batchUpdateStates"):
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"activatePurchaseOptionRequest":{"packageName":"com.x","productId":"coins","purchaseOptionId":"default"}`) {
				t.Errorf("activate body %s", body)
			}
			w.Write([]byte(`{}`))
		}
	})
	args := iapWriteArgs{ProductID: "coins", Title: "Coins", Description: "100 coins", Price: "2.99"}
	ls, _ := args.listings()
	price, _ := args.money()
	out, err := createIAP(context.Background(), testClient(), "com.x", args, ls, price)
	if err != nil {
		t.Fatal(err)
	}
	want := "GET /applications/com.x/oneTimeProducts/coins\n" +
		"POST /applications/com.x/pricing:convertRegionPrices\n" +
		"PATCH /applications/com.x/oneTimeProducts/coins\n" +
		"POST /applications/com.x/oneTimeProducts/coins/purchaseOptions:batchUpdateStates"
	if strings.Join(calls, "\n") != want {
		t.Errorf("calls:\n%s", strings.Join(calls, "\n"))
	}
	opt := patched["purchaseOptions"].([]any)[0].(map[string]any)
	if len(opt["regionalPricingAndAvailabilityConfigs"].([]any)) != 2 || opt["newRegionsConfig"].(map[string]any)["eurPrice"] == nil {
		t.Errorf("purchase option %v", opt)
	}
	if !strings.Contains(out, "purchase option default: ACTIVE, US price 2.99 USD, 2 regions") {
		t.Errorf("out:\n%s", out)
	}
}

// Update of the price keeps unmodeled fields (offerTags) and other regions'
// settings out of the request only where the new pricing replaces them.
func TestUpdateIAPPrice(t *testing.T) {
	var patched map[string]any
	var mask string
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			w.Write([]byte(otpFixture))
		case strings.HasSuffix(r.URL.Path, ":convertRegionPrices"):
			w.Write([]byte(convertFixture))
		case r.Method == http.MethodPatch:
			mask = r.URL.Query().Get("updateMask")
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &patched)
			w.Write(body)
		}
	})
	args := iapWriteArgs{ProductID: "pro", Price: "2.99", Title: "Pro+"}
	ls, _ := args.listings()
	out, err := updateIAP(context.Background(), testClient(), "com.x", args, ls)
	if err != nil {
		t.Fatal(err)
	}
	if mask != "listings,purchaseOptions" {
		t.Errorf("updateMask = %q", mask)
	}
	if _, ok := patched["regionsVersion"]; ok {
		t.Error("output-only regionsVersion must not be sent")
	}
	opt := patched["purchaseOptions"].([]any)[0].(map[string]any)
	if opt["offerTags"] == nil {
		t.Error("unmodeled fields must be kept")
	}
	if !strings.Contains(out, "listing en-US: Pro+") || !strings.Contains(out, "US price 2.99 USD") {
		t.Errorf("out:\n%s", out)
	}
}

func TestRetryOnServerErrorGET(t *testing.T) {
	old := googleapi.RetryBackoff
	googleapi.RetryBackoff = time.Millisecond
	t.Cleanup(func() { googleapi.RetryBackoff = old })
	var calls int32
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"oneTimeProducts":[]}`))
	})
	if _, err := testClient().listOneTimeProducts(context.Background(), "com.x"); err != nil || calls != 2 {
		t.Errorf("err = %v, calls = %d", err, calls)
	}
}
