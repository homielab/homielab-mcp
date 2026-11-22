package googleplay

// One-time in-app products via the monetization API (oneTimeProducts). The
// legacy inappproducts API answers 403 "Please migrate to the new publishing
// API" for migrated developer accounts.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/googleapi"
	"github.com/homielab/homielab-mcp/internal/render"
)

// ---- resources ----

type money struct {
	CurrencyCode string `json:"currencyCode"`
	Units        string `json:"units,omitempty"`
	Nanos        int64  `json:"nanos,omitempty"`
}

func (m money) amount() float64 {
	u, _ := strconv.ParseFloat(m.Units, 64)
	return u + float64(m.Nanos)/1e9
}

func (m money) String() string {
	if m.CurrencyCode == "" {
		return "—"
	}
	return strconv.FormatFloat(m.amount(), 'f', -1, 64) + " " + m.CurrencyCode
}

// parseMoney turns "2.99" + "usd" into Money (units + nanos).
func parseMoney(amount, currency string) (money, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if !currencyRE.MatchString(currency) {
		return money{}, fmt.Errorf("currency %q must be a 3-letter ISO code like USD", currency)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(amount), 64)
	if err != nil || v <= 0 || math.IsInf(v, 0) {
		return money{}, fmt.Errorf("price %q must be a positive decimal like 2.99", amount)
	}
	units := math.Floor(v)
	nanos := math.Round((v - units) * 1e9)
	return money{CurrencyCode: currency, Units: strconv.FormatFloat(units, 'f', 0, 64), Nanos: int64(nanos)}, nil
}

type oneTimeProduct struct {
	PackageName     string           `json:"packageName"`
	ProductID       string           `json:"productId"`
	Listings        []otpListing     `json:"listings"`
	PurchaseOptions []purchaseOption `json:"purchaseOptions"`
	RegionsVersion  struct {
		Version string `json:"version"`
	} `json:"regionsVersion"`
}

type otpListing struct {
	LanguageCode string `json:"languageCode"`
	Title        string `json:"title"`
	Description  string `json:"description"`
}

type purchaseOption struct {
	PurchaseOptionID string `json:"purchaseOptionId"`
	State            string `json:"state"`
	BuyOption        *struct {
		LegacyCompatible     bool `json:"legacyCompatible"`
		MultiQuantityEnabled bool `json:"multiQuantityEnabled"`
	} `json:"buyOption"`
	RentOption *struct {
		RentalPeriod string `json:"rentalPeriod"`
	} `json:"rentOption"`
	RegionalConfigs []struct {
		RegionCode   string `json:"regionCode"`
		Price        money  `json:"price"`
		Availability string `json:"availability"`
	} `json:"regionalPricingAndAvailabilityConfigs"`
}

// price returns the option's price in region (e.g. "US"), if configured.
func (o purchaseOption) price(region string) (money, bool) {
	for _, rc := range o.RegionalConfigs {
		if rc.RegionCode == region {
			return rc.Price, true
		}
	}
	return money{}, false
}

// convertedPrices is pricing:convertRegionPrices: one price converted to
// every Play region, plus USD/EUR prices for regions added later.
type convertedPrices struct {
	ConvertedRegionPrices map[string]struct {
		RegionCode string `json:"regionCode"`
		Price      money  `json:"price"`
	} `json:"convertedRegionPrices"`
	ConvertedOtherRegionsPrice struct {
		USDPrice money `json:"usdPrice"`
		EURPrice money `json:"eurPrice"`
	} `json:"convertedOtherRegionsPrice"`
	RegionVersion struct {
		Version string `json:"version"`
	} `json:"regionVersion"`
}

// pricing returns the purchase-option pricing fields for these prices.
func (cp convertedPrices) pricing() (regional []map[string]any, newRegions map[string]any) {
	regions := make([]string, 0, len(cp.ConvertedRegionPrices))
	for r := range cp.ConvertedRegionPrices {
		regions = append(regions, r)
	}
	slices.Sort(regions)
	for _, r := range regions {
		regional = append(regional, map[string]any{
			"regionCode": r, "price": cp.ConvertedRegionPrices[r].Price, "availability": "AVAILABLE",
		})
	}
	newRegions = map[string]any{
		"usdPrice": cp.ConvertedOtherRegionsPrice.USDPrice, "eurPrice": cp.ConvertedOtherRegionsPrice.EURPrice,
		"availability": "AVAILABLE",
	}
	return regional, newRegions
}

// ---- API calls ----

func otpURL(pkg, productID string) string {
	u := googleapi.JoinURL(apiBase, "/applications/"+pkg+"/oneTimeProducts")
	if productID != "" {
		u += "/" + url.PathEscape(productID)
	}
	return u
}

func (c *playClient) listOneTimeProducts(ctx context.Context, pkg string) ([]oneTimeProduct, error) {
	return googleapi.ListAll[oneTimeProduct](ctx, c.Client, otpURL(pkg, ""), url.Values{"pageSize": {"1000"}}, "oneTimeProducts")
}

// getOneTimeProduct returns the product as raw JSON fields, so an update can
// send back fields this server does not model (offer tags, tax settings, ...).
func (c *playClient) getOneTimeProduct(ctx context.Context, pkg, productID string) (map[string]any, int, error) {
	body, status, err := c.Do(ctx, http.MethodGet, otpURL(pkg, productID), nil)
	if err != nil {
		return nil, status, err
	}
	var out map[string]any
	return out, status, json.Unmarshal(body, &out)
}

// patchOneTimeProduct creates (allowMissing) or updates the fields in mask.
func (c *playClient) patchOneTimeProduct(ctx context.Context, pkg, productID string, product map[string]any, mask []string, regionsVersion string, allowMissing bool) (oneTimeProduct, error) {
	q := url.Values{"regionsVersion.version": {regionsVersion}, "updateMask": {strings.Join(mask, ",")}}
	if allowMissing {
		q.Set("allowMissing", "true")
	}
	var out oneTimeProduct
	body, _, err := c.Do(ctx, http.MethodPatch, otpURL(pkg, productID)+"?"+q.Encode(), product)
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(body, &out)
}

func (c *playClient) deleteOneTimeProduct(ctx context.Context, pkg, productID string) error {
	_, _, err := c.Do(ctx, http.MethodDelete, otpURL(pkg, productID), nil)
	return err
}

func (c *playClient) convertPrice(ctx context.Context, pkg string, price money) (convertedPrices, error) {
	var out convertedPrices
	body, _, err := c.Do(ctx, http.MethodPost, googleapi.JoinURL(apiBase, "/applications/"+pkg+"/pricing:convertRegionPrices"),
		map[string]any{"price": price})
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(body, &out)
}

// setPurchaseOptionState activates or deactivates one purchase option.
func (c *playClient) setPurchaseOptionState(ctx context.Context, pkg, productID, optionID string, active bool) error {
	key := "deactivatePurchaseOptionRequest"
	if active {
		key = "activatePurchaseOptionRequest"
	}
	req := map[string]any{"requests": []any{map[string]any{key: map[string]string{
		"packageName": pkg, "productId": productID, "purchaseOptionId": optionID,
	}}}}
	_, _, err := c.Do(ctx, http.MethodPost, otpURL(pkg, productID)+"/purchaseOptions:batchUpdateStates", req)
	return err
}

// ---- tool arguments ----

var productIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._]*$`)
var currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)

type iapListingArgs struct {
	Title       string `json:"title,omitempty" jsonschema:"Localized product title (max 55 chars)."`
	Description string `json:"description,omitempty" jsonschema:"Localized description (max 200 chars)."`
}

type iapWriteArgs struct {
	PackageName      string                    `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
	ProductID        string                    `json:"productId" jsonschema:"Product id: starts with a lowercase letter or digit; lowercase letters, digits, periods and underscores."`
	Language         string                    `json:"language,omitempty" jsonschema:"BCP-47 language of title/description, e.g. en-US (default en-US)."`
	Title            string                    `json:"title,omitempty" jsonschema:"Title in language (max 55 chars)."`
	Description      string                    `json:"description,omitempty" jsonschema:"Description in language (max 200 chars)."`
	Listings         map[string]iapListingArgs `json:"listings,omitempty" jsonschema:"More localized listings keyed by language, e.g. {\"vi\": {\"title\": \"...\", \"description\": \"...\"}}."`
	Price            string                    `json:"price,omitempty" jsonschema:"Price as a decimal, e.g. \"2.99\"; converted to every Play region like the Play Console does."`
	Currency         string                    `json:"currency,omitempty" jsonschema:"ISO 4217 currency of price, e.g. USD (default USD)."`
	PurchaseOptionID string                    `json:"purchaseOptionId,omitempty" jsonschema:"Purchase option to create or update; default \"default\" on create, the product's only/legacy option on update."`
	Active           *bool                     `json:"active,omitempty" jsonschema:"Activate (true) or deactivate (false) the purchase option. Create defaults to true."`
}

// listings merges title/description and Listings into per-language entries.
func (a iapWriteArgs) listings() ([]otpListing, error) {
	byLang := map[string]iapListingArgs{}
	for lang, l := range a.Listings {
		byLang[strings.TrimSpace(lang)] = l
	}
	if a.Title != "" || a.Description != "" {
		lang := strings.TrimSpace(a.Language)
		if lang == "" {
			lang = "en-US"
		}
		l := byLang[lang]
		if a.Title != "" {
			l.Title = a.Title
		}
		if a.Description != "" {
			l.Description = a.Description
		}
		byLang[lang] = l
	}
	langs := make([]string, 0, len(byLang))
	for lang := range byLang {
		langs = append(langs, lang)
	}
	slices.Sort(langs)
	out := make([]otpListing, 0, len(langs))
	for _, lang := range langs {
		l := byLang[lang]
		switch {
		case lang == "":
			return nil, errors.New("listing language must not be empty")
		case len([]rune(l.Title)) > 55:
			return nil, fmt.Errorf("%s title is %d characters; max 55", lang, len([]rune(l.Title)))
		case len([]rune(l.Description)) > 200:
			return nil, fmt.Errorf("%s description is %d characters; max 200", lang, len([]rune(l.Description)))
		}
		out = append(out, otpListing{LanguageCode: lang, Title: l.Title, Description: l.Description})
	}
	return out, nil
}

func (a iapWriteArgs) validateID() error {
	if !productIDRE.MatchString(a.ProductID) {
		return fmt.Errorf("productId %q must start with a lowercase letter or digit and contain only lowercase letters, digits, periods and underscores", a.ProductID)
	}
	return nil
}

func (a iapWriteArgs) money() (money, error) {
	cur := a.Currency
	if cur == "" {
		cur = "USD"
	}
	return parseMoney(a.Price, cur)
}

// ---- rendering ----

func renderOneTimeProducts(pkg string, prods []oneTimeProduct) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d one-time %s for %s:\n\n", len(prods), plural(len(prods), "product", "products"), pkg)
	if len(prods) == 0 {
		b.WriteString("No one-time products (create with play_iap_insert).")
		return b.String()
	}
	b.WriteString("| Product id | Title | Purchase options (state) | US price | Regions |\n| --- | --- | --- | --- | --- |\n")
	for _, p := range prods {
		var opts []string
		usPrice, regions := "—", 0
		for _, o := range p.PurchaseOptions {
			kind := "buy"
			if o.RentOption != nil {
				kind = "rent " + o.RentOption.RentalPeriod
			}
			opts = append(opts, fmt.Sprintf("%s: %s, %s", o.PurchaseOptionID, o.State, kind))
			if m, ok := o.price("US"); ok && usPrice == "—" {
				usPrice = m.String()
			}
			regions = max(regions, len(o.RegionalConfigs))
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %d |\n", render.MDCell(p.ProductID), render.MDCell(listingTitle(p.Listings)),
			render.MDCell(strings.Join(opts, "; ")), usPrice, regions)
	}
	return b.String()
}

// listingTitle prefers the en-US title, else the first listing's.
func listingTitle(ls []otpListing) string {
	for _, l := range ls {
		if l.LanguageCode == "en-US" {
			return l.Title
		}
	}
	if len(ls) > 0 {
		return ls[0].Title
	}
	return ""
}

func renderOneTimeProductChange(verb string, p oneTimeProduct) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s one-time product %q for %s.\n", verb, p.ProductID, p.PackageName)
	for _, l := range p.Listings {
		fmt.Fprintf(&b, "  listing %s: %s — %s\n", l.LanguageCode, l.Title, truncate(l.Description, 120))
	}
	for _, o := range p.PurchaseOptions {
		us, _ := o.price("US")
		fmt.Fprintf(&b, "  purchase option %s: %s, US price %s, %d regions\n", o.PurchaseOptionID, render.OrDash(o.State), us, len(o.RegionalConfigs))
	}
	return b.String()
}

// ---- tools ----

func registerIAPTools(s *mcp.Server, readOnly *mcp.ToolAnnotations) {
	type pkgArgs struct {
		PackageName string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "play_iap_list",
		Title:       "List In-App Products",
		Description: "List one-time in-app products: product id, title, purchase options with state (ACTIVE/INACTIVE/DRAFT), US price and number of priced regions. Subscriptions come from play_subscriptions.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args pkgArgs) (*mcp.CallToolResult, any, error) {
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		prods, err := c.listOneTimeProducts(ctx, pkg)
		if err != nil {
			return errResult(err)
		}
		return textResult(renderOneTimeProducts(pkg, prods))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "play_iap_insert",
		Title: "Create In-App Product",
		Description: "Create a one-time in-app product with one buy purchase option. Give title (+ description) and price, e.g. {productId: \"coins_100\", title: \"100 Coins\", description: \"...\", price: \"2.99\"}. " +
			"The price is converted to every Play region like the Play Console does; the purchase option is activated unless active=false. Fails if the product exists — use play_iap_update.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args iapWriteArgs) (*mcp.CallToolResult, any, error) {
		if err := args.validateID(); err != nil {
			return errResult(err)
		}
		listings, err := args.listings()
		if err != nil {
			return errResult(err)
		}
		if len(listings) == 0 || slices.ContainsFunc(listings, func(l otpListing) bool { return l.Title == "" || l.Description == "" }) {
			return errResult(errors.New("every listing needs a title and a description"))
		}
		price, err := args.money()
		if err != nil {
			return errResult(err)
		}
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		msg, err := createIAP(ctx, c, pkg, args, listings, price)
		if err != nil {
			return errResult(err)
		}
		return textResult(msg)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "play_iap_update",
		Title: "Update In-App Product",
		Description: "Update a one-time in-app product: listings (title/description per language; other languages are kept), price of one purchase option (re-converted to every region), and/or active=true|false to activate or deactivate it. " +
			"Only the given parts change.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args iapWriteArgs) (*mcp.CallToolResult, any, error) {
		if err := args.validateID(); err != nil {
			return errResult(err)
		}
		listings, err := args.listings()
		if err != nil {
			return errResult(err)
		}
		if len(listings) == 0 && args.Price == "" && args.Active == nil {
			return errResult(errors.New("nothing to update: give title/description/listings, price, or active"))
		}
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		msg, err := updateIAP(ctx, c, pkg, args, listings)
		if err != nil {
			return errResult(err)
		}
		return textResult(msg)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "play_iap_delete",
		Title: "Delete In-App Product",
		Description: "Permanently delete a one-time in-app product (destructive; cannot be undone). " +
			"To stop selling but keep purchase history, use play_iap_update {active: false} instead. Confirm with the user first.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		PackageName string `json:"packageName,omitempty" jsonschema:"App package name; omit to reuse the last used one."`
		ProductID   string `json:"productId" jsonschema:"Id of the product to delete."`
	}) (*mcp.CallToolResult, any, error) {
		if args.ProductID == "" {
			return errResult(errors.New("productId is required"))
		}
		c, res := mustClient(ctx)
		if res != nil {
			return res, nil, nil
		}
		pkg, res := resolvePackageArg(args.PackageName)
		if res != nil {
			return res, nil, nil
		}
		if err := c.deleteOneTimeProduct(ctx, pkg, args.ProductID); err != nil {
			return errResult(err)
		}
		return textResult(fmt.Sprintf("Deleted one-time product %q from %s.", args.ProductID, pkg))
	})
}

// createIAP creates a one-time product with one buy option priced in every
// region from price, then activates it unless args.Active is false.
func createIAP(ctx context.Context, c *playClient, pkg string, args iapWriteArgs, listings []otpListing, price money) (string, error) {
	if _, status, err := c.getOneTimeProduct(ctx, pkg, args.ProductID); err == nil {
		return "", fmt.Errorf("product %q already exists in %s; use play_iap_update", args.ProductID, pkg)
	} else if status != http.StatusNotFound {
		return "", err
	}
	conv, err := c.convertPrice(ctx, pkg, price)
	if err != nil {
		return "", fmt.Errorf("converting the price: %w", err)
	}
	regional, newRegions := conv.pricing()
	optionID := args.PurchaseOptionID
	if optionID == "" {
		optionID = "default"
	}
	product := map[string]any{
		"packageName": pkg, "productId": args.ProductID, "listings": listings,
		"purchaseOptions": []any{map[string]any{
			"purchaseOptionId":                      optionID,
			"buyOption":                             map[string]any{"legacyCompatible": true},
			"regionalPricingAndAvailabilityConfigs": regional,
			"newRegionsConfig":                      newRegions,
		}},
	}
	out, err := c.patchOneTimeProduct(ctx, pkg, args.ProductID, product, []string{"listings", "purchaseOptions"}, conv.RegionVersion.Version, true)
	if err != nil {
		return "", err
	}
	note := ""
	if args.Active == nil || *args.Active {
		if err := c.setPurchaseOptionState(ctx, pkg, args.ProductID, optionID, true); err != nil {
			note = fmt.Sprintf("\n⚠ created, but activating purchase option %s failed: %v", optionID, err)
		} else {
			for i := range out.PurchaseOptions {
				out.PurchaseOptions[i].State = "ACTIVE"
			}
		}
	}
	return renderOneTimeProductChange("Created", out) + note, nil
}

// updateIAP changes listings, the price of one purchase option and/or its
// state, sending back unmodeled fields untouched.
func updateIAP(ctx context.Context, c *playClient, pkg string, args iapWriteArgs, listings []otpListing) (string, error) {
	raw, _, err := c.getOneTimeProduct(ctx, pkg, args.ProductID)
	if err != nil {
		return "", err
	}
	optionID, err := pickPurchaseOption(raw, args.PurchaseOptionID)
	if err != nil {
		return "", err
	}
	var mask []string
	rv, _ := raw["regionsVersion"].(map[string]any)
	version, _ := rv["version"].(string)
	if len(listings) > 0 {
		raw["listings"] = mergeListings(raw["listings"], listings)
		mask = append(mask, "listings")
	}
	if args.Price != "" {
		price, err := args.money()
		if err != nil {
			return "", err
		}
		conv, err := c.convertPrice(ctx, pkg, price)
		if err != nil {
			return "", fmt.Errorf("converting the price: %w", err)
		}
		regional, newRegions := conv.pricing()
		opts, _ := raw["purchaseOptions"].([]any)
		for _, o := range opts {
			if om, _ := o.(map[string]any); om["purchaseOptionId"] == optionID {
				om["regionalPricingAndAvailabilityConfigs"], om["newRegionsConfig"] = regional, newRegions
			}
		}
		version = conv.RegionVersion.Version
		mask = append(mask, "purchaseOptions")
	}
	if version == "" && len(mask) > 0 { // the API needs a regions version on every write
		conv, err := c.convertPrice(ctx, pkg, money{CurrencyCode: "USD", Units: "1"})
		if err != nil {
			return "", fmt.Errorf("reading the current regions version: %w", err)
		}
		version = conv.RegionVersion.Version
	}
	out := oneTimeProduct{}
	if len(mask) > 0 {
		delete(raw, "regionsVersion") // output only
		if out, err = c.patchOneTimeProduct(ctx, pkg, args.ProductID, raw, mask, version, false); err != nil {
			return "", err
		}
	}
	if args.Active != nil {
		if err := c.setPurchaseOptionState(ctx, pkg, args.ProductID, optionID, *args.Active); err != nil {
			return "", fmt.Errorf("changing the state of purchase option %s: %w", optionID, err)
		}
	}
	if len(mask) == 0 || args.Active != nil { // re-read to show the final state
		if raw, _, err = c.getOneTimeProduct(ctx, pkg, args.ProductID); err != nil {
			return "", err
		}
		data, _ := json.Marshal(raw)
		_ = json.Unmarshal(data, &out)
	}
	return renderOneTimeProductChange("Updated", out), nil
}

// pickPurchaseOption returns want if the product has it; with want empty, the
// only option, else the legacy-compatible buy option.
func pickPurchaseOption(product map[string]any, want string) (string, error) {
	opts, _ := product["purchaseOptions"].([]any)
	var ids []string
	legacy := ""
	for _, o := range opts {
		om, _ := o.(map[string]any)
		id, _ := om["purchaseOptionId"].(string)
		ids = append(ids, id)
		if buy, ok := om["buyOption"].(map[string]any); ok && buy["legacyCompatible"] == true {
			legacy = id
		}
	}
	switch {
	case want != "" && slices.Contains(ids, want):
		return want, nil
	case want != "":
		return "", fmt.Errorf("purchase option %q not found; options: %s", want, strings.Join(ids, ", "))
	case len(ids) == 1:
		return ids[0], nil
	case legacy != "":
		return legacy, nil
	}
	return "", fmt.Errorf("product has %d purchase options (%s); pass purchaseOptionId", len(ids), strings.Join(ids, ", "))
}

// mergeListings replaces or adds the given languages, keeping the others.
func mergeListings(existing any, updates []otpListing) []any {
	var out []any
	done := map[string]bool{}
	list, _ := existing.([]any)
	for _, l := range list {
		lm, _ := l.(map[string]any)
		lang, _ := lm["languageCode"].(string)
		if i := slices.IndexFunc(updates, func(u otpListing) bool { return u.LanguageCode == lang }); i >= 0 {
			u := updates[i]
			if u.Title != "" {
				lm["title"] = u.Title
			}
			if u.Description != "" {
				lm["description"] = u.Description
			}
			done[lang] = true
		}
		out = append(out, lm)
	}
	for _, u := range updates {
		if !done[u.LanguageCode] {
			out = append(out, map[string]any{"languageCode": u.LanguageCode, "title": u.Title, "description": u.Description})
		}
	}
	return out
}
