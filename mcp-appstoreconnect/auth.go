package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// App Store Connect authenticates every request with a short-lived ES256 JWT
// signed by an API key (.p8) — there is no OAuth flow. Credentials come from
// env vars, then <user config dir>/mcp-appstoreconnect/config.json, then
// AuthKey_<KEYID>.p8 files in the usual places.

const (
	jwtAudience = "appstoreconnect-v1"
	jwtLifetime = 19 * time.Minute // Apple rejects tokens valid > 20 min
	jwtRefresh  = time.Minute      // re-sign when less than this is left
)

// configBase overrides os.UserConfigDir in tests.
var configBase string

func configDir() (string, error) {
	base := configBase
	if base == "" {
		var err error
		if base, err = os.UserConfigDir(); err != nil {
			return "", err
		}
	}
	return filepath.Join(base, serverName), nil
}

func configPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// fileConfig is config.json.
type fileConfig struct {
	KeyID          string `json:"keyId,omitempty"`
	IssuerID       string `json:"issuerId,omitempty"`
	PrivateKeyPath string `json:"privateKeyPath,omitempty"`
	VendorNumber   string `json:"vendorNumber,omitempty"`
}

// credentials is a resolved API key.
type credentials struct {
	KeyID        string
	IssuerID     string // empty = individual key (JWT "sub": "user")
	KeyPath      string // empty when the key came from ASC_PRIVATE_KEY
	VendorNumber string
	key          *ecdsa.PrivateKey
}

const credentialSources = `  - ASC_KEY_ID, ASC_ISSUER_ID, ASC_PRIVATE_KEY_PATH (or ASC_PRIVATE_KEY = PEM text),
    ASC_VENDOR_NUMBER environment variables (each overrides config.json)
  - <user config dir>/mcp-appstoreconnect/config.json, written by "mcp-appstoreconnect setup"
  - AuthKey_<KEYID>.p8 in <user config dir>/mcp-appstoreconnect/, ./private_keys,
    ~/private_keys, ~/.private_keys, ~/.appstoreconnect/private_keys
    (with no key id set, a single AuthKey_*.p8 in the config dir is used)
`

var errNoCredentials = errors.New("App Store Connect API key not configured")

var keyFileRE = regexp.MustCompile(`^AuthKey_([A-Z0-9]+)\.p8$`)

// loadCredentials resolves the API key from env, config.json and key files.
func loadCredentials() (*credentials, error) {
	var fc fileConfig
	if p, err := configPath(); err == nil {
		if data, err := os.ReadFile(p); err == nil {
			if err := json.Unmarshal(data, &fc); err != nil {
				return nil, fmt.Errorf("parsing %s: %w", p, err)
			}
		}
	}
	c := &credentials{
		KeyID:        firstNonEmpty(os.Getenv("ASC_KEY_ID"), fc.KeyID),
		IssuerID:     firstNonEmpty(os.Getenv("ASC_ISSUER_ID"), fc.IssuerID),
		VendorNumber: firstNonEmpty(os.Getenv("ASC_VENDOR_NUMBER"), fc.VendorNumber),
	}
	var pemData []byte
	switch {
	case os.Getenv("ASC_PRIVATE_KEY") != "":
		pemData = []byte(strings.ReplaceAll(os.Getenv("ASC_PRIVATE_KEY"), `\n`, "\n"))
	default:
		c.KeyPath = firstNonEmpty(os.Getenv("ASC_PRIVATE_KEY_PATH"), fc.PrivateKeyPath)
		if c.KeyPath == "" {
			c.KeyPath, c.KeyID = findKeyFile(c.KeyID)
		}
		if c.KeyPath == "" {
			return nil, setupError(errNoCredentials)
		}
		data, err := os.ReadFile(expandHome(c.KeyPath))
		if err != nil {
			return nil, setupError(fmt.Errorf("reading private key: %w", err))
		}
		pemData = data
	}
	if c.KeyID == "" && c.KeyPath != "" {
		if m := keyFileRE.FindStringSubmatch(filepath.Base(c.KeyPath)); m != nil {
			c.KeyID = m[1]
		}
	}
	if c.KeyID == "" {
		return nil, setupError(errors.New("API key id unknown: set ASC_KEY_ID or keep the AuthKey_<KEYID>.p8 file name"))
	}
	key, err := parsePrivateKey(pemData)
	if err != nil {
		return nil, setupError(err)
	}
	c.key = key
	return c, nil
}

// findKeyFile looks for AuthKey_<keyID>.p8 in the usual dirs; with no keyID
// it takes the only AuthKey_*.p8 in the config dir.
func findKeyFile(keyID string) (path, id string) {
	dir, _ := configDir()
	if keyID == "" {
		if dir == "" {
			return "", ""
		}
		matches, _ := filepath.Glob(filepath.Join(dir, "AuthKey_*.p8"))
		if len(matches) != 1 {
			return "", ""
		}
		m := keyFileRE.FindStringSubmatch(filepath.Base(matches[0]))
		if m == nil {
			return "", ""
		}
		return matches[0], m[1]
	}
	home, _ := os.UserHomeDir()
	dirs := []string{dir, "private_keys"}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, "private_keys"), filepath.Join(home, ".private_keys"), filepath.Join(home, ".appstoreconnect", "private_keys"))
	}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		p := filepath.Join(d, "AuthKey_"+keyID+".p8")
		if _, err := os.Stat(p); err == nil {
			return p, keyID
		}
	}
	return "", keyID
}

func parsePrivateKey(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("private key is not PEM: expected the .p8 file downloaded from App Store Connect")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing private key: %w", err)
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is %T, want an ECDSA P-256 key (.p8)", k)
	}
	return ec, nil
}

func setupError(err error) error {
	return fmt.Errorf(`%w

Setup:
1. App Store Connect → Users and Access → Integrations → App Store Connect API →
   Team Keys → generate a key (role: Admin or App Manager for writes; Sales or
   Finance for reports). Download AuthKey_<KEYID>.p8 (only once) and copy the
   Issuer ID shown above the key list.
2. Run: %s setup --key-file /path/to/AuthKey_<KEYID>.p8 --issuer-id <ISSUER_ID> [--vendor-number <N>]
   or set ASC_KEY_ID, ASC_ISSUER_ID and ASC_PRIVATE_KEY_PATH on the MCP registration.
Individual keys (Users and Access → your profile) work too: omit the issuer id.`, err, serverName)
}

// ---- JWT ----

// tokenSource signs and caches the bearer token.
type tokenSource struct {
	creds *credentials
	now   func() time.Time

	mu  sync.Mutex
	tok string
	exp time.Time
}

func (t *tokenSource) Token() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if t.tok != "" && t.exp.Sub(now) > jwtRefresh {
		return t.tok, nil
	}
	tok, err := signJWT(t.creds, now)
	if err != nil {
		return "", err
	}
	t.tok, t.exp = tok, now.Add(jwtLifetime)
	return tok, nil
}

func signJWT(c *credentials, now time.Time) (string, error) {
	header := map[string]string{"alg": "ES256", "kid": c.KeyID, "typ": "JWT"}
	claims := map[string]any{
		"iat": now.Unix(),
		"exp": now.Add(jwtLifetime).Unix(),
		"aud": jwtAudience,
	}
	if c.IssuerID != "" {
		claims["iss"] = c.IssuerID
	} else {
		claims["sub"] = "user"
	}
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	p, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(h) + "." + enc.EncodeToString(p)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, c.key, sum[:])
	if err != nil {
		return "", err
	}
	// JWS ES256 signatures are r||s, each left-padded to 32 bytes.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}

// jwtTransport adds the bearer token to every request.
type jwtTransport struct {
	src  *tokenSource
	base http.RoundTripper
}

func (t *jwtTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := t.src.Token()
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+tok)
	return t.base.RoundTrip(r)
}

// httpClient returns an authenticated client and the credentials behind it.
func httpClient() (*http.Client, *credentials, error) {
	c, err := loadCredentials()
	if err != nil {
		return nil, nil, err
	}
	src := &tokenSource{creds: c, now: time.Now}
	return &http.Client{Transport: &jwtTransport{src: src, base: http.DefaultTransport}}, c, nil
}

// ---- setup command ----

// runSetup copies the .p8 into the config dir, writes config.json (both 0600)
// and checks the key against the API.
func runSetup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	keyFile := fs.String("key-file", "", "path to AuthKey_<KEYID>.p8 (required)")
	issuer := fs.String("issuer-id", "", "Issuer ID from App Store Connect → Integrations (omit for an individual key)")
	keyID := fs.String("key-id", "", "key id (default: from the file name AuthKey_<KEYID>.p8)")
	vendor := fs.String("vendor-number", "", "vendor number for sales/finance reports (Payments and Financial Reports)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keyFile == "" {
		fs.Usage()
		return errors.New("--key-file is required")
	}
	data, err := os.ReadFile(expandHome(*keyFile))
	if err != nil {
		return err
	}
	if _, err := parsePrivateKey(data); err != nil {
		return err
	}
	id := *keyID
	if id == "" {
		m := keyFileRE.FindStringSubmatch(filepath.Base(*keyFile))
		if m == nil {
			return errors.New("cannot read the key id from the file name: pass --key-id")
		}
		id = m[1]
	}
	dir, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	dest := filepath.Join(dir, "AuthKey_"+id+".p8")
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return err
	}
	cfg := fileConfig{KeyID: id, IssuerID: *issuer, PrivateKeyPath: dest, VendorNumber: *vendor}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	cp := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Printf("Saved key %s to %s and settings to %s.\n", id, dest, cp)
	if *issuer == "" {
		fmt.Println("No issuer id: treating it as an individual API key.")
	}
	c, err := newASCClient()
	if err != nil {
		return err
	}
	apps, err := c.listApps(ctx, "")
	if err != nil {
		return fmt.Errorf("key saved, but the test call failed: %w", err)
	}
	fmt.Printf("Key works: %d app(s) visible.\n", len(apps))
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return p
}
