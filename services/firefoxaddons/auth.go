package firefoxaddons

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/homielab/homielab-mcp/internal/appdir"
)

// AMO authenticates every request with a short-lived HS256 JWT signed with
// the API secret (header "Authorization: JWT <token>"). Each request gets a
// fresh token with a unique jti, so there is nothing to cache or refresh.

// jwtLifetime stays well under AMO's 5-minute maximum.
const jwtLifetime = 60 * time.Second

const defaultAPIBase = "https://addons.mozilla.org/api/v5"

const credentialSources = `  - AMO_JWT_ISSUER + AMO_JWT_SECRET, then WEB_EXT_API_KEY + WEB_EXT_API_SECRET (web-ext's names)
  - <user config dir>/mcp-homielab/firefoxaddons/credentials.json, written by "mcp-homielab firefoxaddons setup"
  - AMO_API_BASE (or apiBase in credentials.json) points at another server,
    e.g. https://addons.allizom.org/api/v5 (stage); default ` + defaultAPIBase + `
`

// configBase overrides appdir.Base in tests.
var configBase string

func configDir() (string, error) {
	base := configBase
	if base == "" {
		var err error
		if base, err = appdir.Base(); err != nil {
			return "", err
		}
	}
	return filepath.Join(base, serviceID), nil
}

// credentials is credentials.json and the resolved key.
type credentials struct {
	Issuer  string `json:"issuer"`
	Secret  string `json:"secret"`
	APIBase string `json:"apiBase,omitempty"`
}

var errNoCredentials = errors.New("AMO API key not configured")

func setupError(err error) error {
	return fmt.Errorf(`%w

Setup:
1. Sign in at https://addons.mozilla.org/developers/addon/api/key/ and generate
   credentials: a JWT issuer (user:12345:67) and a JWT secret (shown once).
2. Run: %s setup --issuer user:12345:67   (then paste the secret)
   or set AMO_JWT_ISSUER and AMO_JWT_SECRET on the MCP registration.`, err, appdir.Name+" "+serviceID)
}

func loadCredentials() (*credentials, error) {
	var fc credentials
	dir, err := configDir()
	if err == nil {
		p := filepath.Join(dir, "credentials.json")
		if data, err := os.ReadFile(p); err == nil {
			if err := json.Unmarshal(data, &fc); err != nil {
				return nil, fmt.Errorf("parsing %s: %w", p, err)
			}
		}
	}
	c := &credentials{APIBase: firstNonEmpty(os.Getenv("AMO_API_BASE"), fc.APIBase, defaultAPIBase)}
	switch {
	case os.Getenv("AMO_JWT_ISSUER") != "" || os.Getenv("AMO_JWT_SECRET") != "":
		c.Issuer, c.Secret = os.Getenv("AMO_JWT_ISSUER"), os.Getenv("AMO_JWT_SECRET")
	case os.Getenv("WEB_EXT_API_KEY") != "" || os.Getenv("WEB_EXT_API_SECRET") != "":
		c.Issuer, c.Secret = os.Getenv("WEB_EXT_API_KEY"), os.Getenv("WEB_EXT_API_SECRET")
	default:
		c.Issuer, c.Secret = fc.Issuer, fc.Secret
	}
	c.Issuer, c.Secret = strings.TrimSpace(c.Issuer), strings.TrimSpace(c.Secret)
	if c.Issuer == "" || c.Secret == "" {
		if c.Issuer != "" || c.Secret != "" {
			return nil, setupError(errors.New("AMO API key incomplete: both the JWT issuer and the JWT secret are needed"))
		}
		return nil, setupError(errNoCredentials)
	}
	if err := checkAPIBase(c.APIBase); err != nil {
		return nil, err
	}
	c.APIBase = strings.TrimSuffix(c.APIBase, "/")
	return c, nil
}

func checkAPIBase(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("API base %q must be a URL such as %s", s, defaultAPIBase)
	}
	return nil
}

func signJWT(c *credentials, now time.Time) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	h, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	p, err := json.Marshal(map[string]any{
		"iss": c.Issuer,
		"jti": hex.EncodeToString(nonce),
		"iat": now.Unix(),
		"exp": now.Add(jwtLifetime).Unix(),
	})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(h) + "." + enc.EncodeToString(p)
	mac := hmac.New(sha256.New, []byte(c.Secret))
	mac.Write([]byte(signing))
	return signing + "." + enc.EncodeToString(mac.Sum(nil)), nil
}

// jwtTransport signs a fresh token for every request (and every retry).
type jwtTransport struct {
	creds *credentials
	base  http.RoundTripper
}

func (t *jwtTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := signJWT(t.creds, time.Now())
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "JWT "+tok)
	r.Header.Set("Accept", "application/json")
	return t.base.RoundTrip(r)
}

// ---- setup command ----

// runSetup stores the issuer and secret in credentials.json (0600) and
// checks them against the profile endpoint.
func runSetup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	issuer := fs.String("issuer", "", "JWT issuer from https://addons.mozilla.org/developers/addon/api/key/ (prompted when omitted)")
	apiBase := fs.String("api-base", "", "API base URL, e.g. https://addons.allizom.org/api/v5 for stage (default "+defaultAPIBase+")")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *apiBase != "" {
		if err := checkAPIBase(*apiBase); err != nil {
			return err
		}
	}
	in := bufio.NewReader(os.Stdin)
	readLine := func(prompt string) (string, error) {
		fmt.Fprintln(os.Stderr, prompt)
		line, err := in.ReadString('\n')
		if s := strings.TrimSpace(line); s != "" {
			return s, nil
		}
		if err != nil {
			return "", err
		}
		return "", errors.New("empty input")
	}
	iss := strings.TrimSpace(*issuer)
	if iss == "" {
		var err error
		if iss, err = readLine("JWT issuer (user:12345:67):"); err != nil {
			return err
		}
	}
	secret, err := readLine("JWT secret, then press Enter:")
	if err != nil {
		return err
	}
	dir, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(credentials{Issuer: iss, Secret: secret, APIBase: *apiBase}, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(p, append(data, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Println("Saved credentials to", p)
	c, err := newAMOClient()
	if err != nil {
		return err
	}
	prof, err := c.profile(ctx)
	if err != nil {
		return fmt.Errorf("credentials saved, but the test call failed: %w", err)
	}
	fmt.Printf("Key works: signed in as %s (user id %d) on %s.\n", prof.Username, prof.ID, c.base)
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
