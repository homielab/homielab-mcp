// Package googleauth implements the OAuth 2.0 installed-app flow (RFC 8252,
// loopback + PKCE) shared by every Google service: credential discovery, a
// 0600 token store under <config dir>/<service>/, a
// background login manager for MCP tools, and an authenticated *http.Client.
package googleauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/homielab/homielab-mcp/internal/appdir"
)

const (
	AuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	TokenURL = "https://oauth2.googleapis.com/token"
)

// Collection lists the Google services. Each one probes its own dir, the
// shared config dir, then the others' dirs for client_secret*.json, so one
// downloaded secret file serves all of them.
var Collection = []string{"adsense", "searchconsole", "googleplay", "admob", "blogger", "chromewebstore"}

// ErrNoToken is wrapped by every "no stored token" error.
var ErrNoToken = errors.New("not authenticated")

const loginTimeout = 5 * time.Minute

// Provider describes one server's OAuth identity.
type Provider struct {
	Name       string // service id and config subdir, e.g. "adsense"
	Title      string // human name, e.g. "AdSense"
	Scope      string // OAuth scope
	EnvPrefix  string // env prefix, e.g. "ADSENSE_MCP" → ADSENSE_MCP_CLIENT_ID
	ToolPrefix string // MCP tool prefix, e.g. "adsense" → adsense_login
	APIName    string // API to enable in Google Cloud Console
	Account    string // which Google account to sign in with, for hints
	BaseDir    string // overrides appdir.Base (tests); empty = real dir

	mu      sync.Mutex
	running bool
	authURL string
	lastErr error
	doneAt  time.Time
}

func (p *Provider) env(suffix string) string { return os.Getenv(p.EnvPrefix + "_" + suffix) }

// Config builds the oauth2.Config from <PREFIX>_CLIENT_ID + _CLIENT_SECRET, or
// a Google Cloud "Desktop app" client-secret JSON file.
func (p *Provider) Config() (*oauth2.Config, error) {
	if id, sec := p.env("CLIENT_ID"), p.env("CLIENT_SECRET"); id != "" && sec != "" {
		return p.newConfig(id, sec), nil
	}
	candidates := p.secretCandidates()
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cs clientSecrets
		if err := json.Unmarshal(data, &cs); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		switch {
		case cs.Installed != nil:
			return p.newConfig(cs.Installed.ClientID, cs.Installed.ClientSecret), nil
		case cs.Web != nil:
			return p.newConfig(cs.Web.ClientID, cs.Web.ClientSecret), nil
		default:
			return nil, fmt.Errorf("%s does not contain an \"installed\" or \"web\" client", path)
		}
	}
	where := "client_secret.json"
	if dir, err := p.ConfigDir(); err == nil {
		where = filepath.Join(dir, "client_secret.json")
	}
	return nil, fmt.Errorf("no OAuth client credentials found.\n"+
		"Set %[1]s_CLIENT_ID and %[1]s_CLIENT_SECRET, or download a "+
		"\"Desktop app\" client secret JSON from Google Cloud Console and place it at %[2]s\n"+
		"(a client_secret*.json in %[3]s, shared by every Google service, also works)", p.EnvPrefix, where, p.base())
}

type clientSecrets struct {
	Installed *clientSecret `json:"installed"`
	Web       *clientSecret `json:"web"`
}

type clientSecret struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func (p *Provider) newConfig(id, secret string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     id,
		ClientSecret: secret,
		Endpoint:     oauth2.Endpoint{AuthURL: AuthURL, TokenURL: TokenURL},
		Scopes:       []string{p.Scope},
	}
}

// secretCandidates lists client secret files in lookup order:
// $<PREFIX>_CLIENT_SECRET_FILE, ./client_secret.json, this service's config
// dir, the shared config dir, then the other Google services' dirs.
func (p *Provider) secretCandidates() []string {
	var paths []string
	if f := p.env("CLIENT_SECRET_FILE"); f != "" {
		paths = append(paths, f)
	}
	paths = append(paths, "client_secret.json")
	dirs := []string{p.Name, ""}
	for _, n := range Collection {
		if n != p.Name {
			dirs = append(dirs, n)
		}
	}
	for _, n := range dirs {
		if dir, err := p.dir(n); err == nil {
			matches, _ := filepath.Glob(filepath.Join(dir, "client_secret*.json"))
			paths = append(paths, matches...)
		}
	}
	return paths
}

// CredentialSources describes the lookup order for usage text.
func (p *Provider) CredentialSources() string {
	var b strings.Builder
	fmt.Fprintf(&b, "  - %[1]s_CLIENT_ID + %[1]s_CLIENT_SECRET environment variables\n", p.EnvPrefix)
	fmt.Fprintf(&b, "  - $%s_CLIENT_SECRET_FILE\n", p.EnvPrefix)
	b.WriteString("  - ./client_secret.json\n")
	fmt.Fprintf(&b, "  - %s/client_secret*.json\n", filepath.Join(p.base(), p.Name))
	fmt.Fprintf(&b, "  - %s/client_secret*.json  (shared by every Google service)\n", p.base())
	b.WriteString("  - the other Google services' config dirs\n")
	return b.String()
}

func (p *Provider) dir(name string) (string, error) {
	base := p.BaseDir
	if base == "" {
		var err error
		if base, err = appdir.Base(); err != nil {
			return "", err
		}
	}
	return filepath.Join(base, name), nil
}

// base returns the shared config dir for messages.
func (p *Provider) base() string {
	if d, err := p.dir(""); err == nil {
		return d
	}
	return "<user config dir>/" + appdir.Name
}

// ConfigDir returns <config dir>/<Name>.
func (p *Provider) ConfigDir() (string, error) { return p.dir(p.Name) }

// TokenPath returns the token file location.
func (p *Provider) TokenPath() (string, error) {
	dir, err := p.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "token.json"), nil
}

// LoadToken reads the stored token; the error wraps ErrNoToken when absent.
func (p *Provider) LoadToken() (*oauth2.Token, error) {
	path, err := p.TokenPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: run the %s_login tool (or `%s login` on the command line) first", ErrNoToken, p.ToolPrefix, p.Name)
		}
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("corrupt token file %s: %w", path, err)
	}
	return &tok, nil
}

// SaveToken writes the token with mode 0600.
func (p *Provider) SaveToken(tok *oauth2.Token) error {
	path, err := p.TokenPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s: token saved to %s\n", p.Name, path)
	return nil
}

// ClearToken deletes the stored token; a missing token is not an error.
func (p *Provider) ClearToken() error {
	path, err := p.TokenPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// TokenStored reports whether a readable token exists.
func (p *Provider) TokenStored() bool {
	_, err := p.LoadToken()
	return err == nil
}

// Login runs the consent flow: it serves a one-shot callback on a random
// 127.0.0.1 port, opens the browser at Google's consent page, and exchanges
// the code for a refresh token which is saved. onAuthURL, when set, is called
// as soon as the consent URL is ready.
func (p *Provider) Login(ctx context.Context, cfg *oauth2.Config, onAuthURL func(string)) (*oauth2.Token, error) {
	verifier := oauth2.GenerateVerifier()
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return nil, err
	}
	state := hex.EncodeToString(stateBytes)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting loopback listener: %w", err)
	}
	conf := *cfg
	conf.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)

	consentURL := conf.AuthCodeURL(state,
		oauth2.S256ChallengeOption(verifier),
		oauth2.AccessTypeOffline,
		// Force the consent screen (guarantees a refresh token even if the
		// user previously granted) and let multi-account users pick the right one.
		oauth2.SetAuthURLParam("prompt", "consent select_account"),
	)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			q := r.URL.Query()
			if e := q.Get("error"); e != "" {
				fmt.Fprintf(w, "<html><body><h3>Authorization failed</h3><p>%s</p></body></html>", html.EscapeString(e))
				errCh <- fmt.Errorf("Google returned %q", e)
				return
			}
			if q.Get("state") != state {
				http.Error(w, "state mismatch", http.StatusBadRequest)
				errCh <- errors.New("OAuth state mismatch")
				return
			}
			code := q.Get("code")
			if code == "" {
				http.Error(w, "missing code", http.StatusBadRequest)
				errCh <- errors.New("callback without code")
				return
			}
			fmt.Fprintf(w, "<html><body><h3>✓ %s MCP authorized.</h3>You can close this window and go back to the app.</body></html>", html.EscapeString(p.Title))
			codeCh <- code
		}),
	}
	go srv.Serve(ln)
	defer srv.Close()

	fmt.Fprintf(os.Stderr, "%s: waiting for authorization in your browser...\n%s\n", p.Name, consentURL)
	if onAuthURL != nil {
		onAuthURL(consentURL)
	}
	if err := openBrowser(consentURL); err != nil {
		fmt.Fprintf(os.Stderr, "%s: could not open a browser automatically (%v); open the URL above manually\n", p.Name, err)
	}

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(loginTimeout):
		return nil, errors.New("timed out waiting for authorization (5 minutes)")
	}

	tok, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	if tok.RefreshToken == "" {
		return nil, errors.New("Google did not return a refresh token; revoke access at https://myaccount.google.com/permissions and log in again")
	}
	if err := p.SaveToken(tok); err != nil {
		return nil, err
	}
	return tok, nil
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// HTTPClient returns an *http.Client that authenticates every request with
// the stored token, refreshing it via Google's token endpoint.
func (p *Provider) HTTPClient(ctx context.Context) (*http.Client, error) {
	cfg, err := p.Config()
	if err != nil {
		return nil, err
	}
	tok, err := p.LoadToken()
	if err != nil {
		return nil, err
	}
	return oauth2.NewClient(ctx, cfg.TokenSource(ctx, tok)), nil
}

// SetupHint appends setup steps to credential/token errors so the model or
// user knows what to do; other errors pass through unchanged.
func (p *Provider) SetupHint(err error) error {
	if !errors.Is(err, ErrNoToken) && !strings.Contains(err.Error(), "no OAuth client credentials") {
		return err
	}
	return fmt.Errorf("%w\n\nSetup summary:\n"+
		"1. In Google Cloud Console: enable the %q and create an OAuth Client ID of type \"Desktop app\".\n"+
		"2. Put the downloaded JSON in %s (shared by every Google service), or set %s_CLIENT_ID / %s_CLIENT_SECRET.\n"+
		"3. Call the %s_login tool and approve access with %s.", err, p.APIName, p.base(), p.EnvPrefix, p.EnvPrefix, p.ToolPrefix, p.Account)
}

// ---- background login ----
//
// MCP tool calls should not block for minutes waiting on a human, so the
// *_login tool starts the consent flow in the background and returns
// immediately; *_login_status reports progress.

// StartLogin launches the consent flow in the background. If a flow is
// already running it returns its consent URL with alreadyRunning=true.
func (p *Provider) StartLogin() (authURL string, alreadyRunning bool, err error) {
	p.mu.Lock()
	if p.running {
		defer p.mu.Unlock()
		return p.authURL, true, nil
	}
	cfg, err := p.Config()
	if err != nil {
		p.mu.Unlock()
		return "", false, err
	}
	p.running = true
	p.authURL = ""
	p.lastErr = nil
	p.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)
		defer cancel()
		_, err := p.Login(ctx, cfg, func(u string) {
			p.mu.Lock()
			p.authURL = u
			p.mu.Unlock()
		})
		p.mu.Lock()
		p.running = false
		p.lastErr = err
		p.doneAt = time.Now()
		p.mu.Unlock()
	}()
	return "", false, nil
}

// Status describes the login flow and stored token.
type Status struct {
	CredsConfigured bool
	FlowRunning     bool
	AuthURL         string
	LastFlowError   string
	LastFlowEnded   string
	TokenStored     bool
	TokenPath       string
}

func (p *Provider) Status() Status {
	var s Status
	_, credsErr := p.Config()
	s.CredsConfigured = credsErr == nil
	if path, err := p.TokenPath(); err == nil {
		s.TokenPath = path
	}
	s.TokenStored = p.TokenStored()

	p.mu.Lock()
	defer p.mu.Unlock()
	s.FlowRunning = p.running
	s.AuthURL = p.authURL
	if p.lastErr != nil {
		s.LastFlowError = p.lastErr.Error()
	}
	if !p.doneAt.IsZero() {
		s.LastFlowEnded = p.doneAt.Format(time.RFC3339)
	}
	return s
}

// RenderStatus turns Status into a compact human-readable block. readyNote is
// appended to the "Ready" line (may be empty).
func (p *Provider) RenderStatus(s Status, readyNote string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s MCP login status:\n", p.Title)
	if s.CredsConfigured {
		b.WriteString("- OAuth client credentials: configured\n")
	} else {
		b.WriteString("- OAuth client credentials: NOT configured — see setup steps in tool error messages\n")
	}
	if s.TokenStored {
		fmt.Fprintf(&b, "- Stored token: present (%s)\n", s.TokenPath)
	} else {
		b.WriteString("- Stored token: none\n")
	}
	switch {
	case s.FlowRunning:
		b.WriteString("- Login flow: RUNNING — waiting for you to approve in the browser.\n")
		if s.AuthURL != "" {
			b.WriteString("  Consent URL (if no browser opened): " + s.AuthURL + "\n")
		}
	case s.LastFlowError != "":
		fmt.Fprintf(&b, "- Login flow: FAILED at %s — %s\n", s.LastFlowEnded, s.LastFlowError)
	case s.LastFlowEnded != "":
		fmt.Fprintf(&b, "- Login flow: finished at %s\n", s.LastFlowEnded)
	default:
		b.WriteString("- Login flow: not started\n")
	}
	switch {
	case s.TokenStored && s.CredsConfigured && !s.FlowRunning:
		fmt.Fprintf(&b, "Ready: call any %s_* data tool now%s.", p.ToolPrefix, readyNote)
	case s.CredsConfigured && !s.FlowRunning && !s.TokenStored:
		fmt.Fprintf(&b, "Next: call %s_login and approve in the browser.", p.ToolPrefix)
	}
	b.WriteString("\n")
	return b.String()
}
