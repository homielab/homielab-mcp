package googleauth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func testProvider(t *testing.T) *Provider {
	t.Setenv("TEST_MCP_CLIENT_SECRET_FILE", "")
	t.Setenv("TEST_MCP_CLIENT_ID", "")
	t.Setenv("TEST_MCP_CLIENT_SECRET", "")
	t.Chdir(t.TempDir()) // no stray ./client_secret.json
	return &Provider{
		Name:       "mcp-adsense",
		Title:      "Test",
		Scope:      "scope-x",
		EnvPrefix:  "TEST_MCP",
		ToolPrefix: "test",
		APIName:    "Test API",
		Account:    "the test account",
		BaseDir:    t.TempDir(),
	}
}

func TestClientSecretsParse(t *testing.T) {
	raw := `{"installed":{"client_id":"abc.apps.googleusercontent.com","client_secret":"GOCSPX-secret","redirect_uris":["http://localhost"]}}`
	var cs clientSecrets
	if err := json.Unmarshal([]byte(raw), &cs); err != nil {
		t.Fatal(err)
	}
	if cs.Installed == nil || cs.Installed.ClientID != "abc.apps.googleusercontent.com" || cs.Installed.ClientSecret != "GOCSPX-secret" {
		t.Errorf("parsed %+v", cs)
	}

	cs = clientSecrets{}
	if err := json.Unmarshal([]byte(`{"web":{"client_id":"w","client_secret":"s"}}`), &cs); err != nil {
		t.Fatal(err)
	}
	if cs.Web == nil || cs.Web.ClientID != "w" {
		t.Errorf("parsed %+v", cs)
	}
}

func TestTokenRoundtrip(t *testing.T) {
	p := testProvider(t)

	if _, err := p.LoadToken(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("empty store should yield ErrNoToken, got %v", err)
	}
	tok := &oauth2.Token{AccessToken: "at", RefreshToken: "rt", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}
	if err := p.SaveToken(tok); err != nil {
		t.Fatal(err)
	}
	path, _ := p.TokenPath()
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	got, err := p.LoadToken()
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshToken != "rt" || got.AccessToken != "at" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if err := p.ClearToken(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.LoadToken(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("after clear should yield ErrNoToken, got %v", err)
	}
	if err := p.ClearToken(); err != nil {
		t.Errorf("clearing a missing token should succeed, got %v", err)
	}
}

func TestConfigFromEnv(t *testing.T) {
	p := testProvider(t)
	t.Setenv("TEST_MCP_CLIENT_ID", "env-id")
	t.Setenv("TEST_MCP_CLIENT_SECRET", "env-secret")
	cfg, err := p.Config()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientID != "env-id" || cfg.ClientSecret != "env-secret" {
		t.Errorf("cfg %+v", cfg)
	}
	if len(cfg.Scopes) != 1 || cfg.Scopes[0] != "scope-x" {
		t.Errorf("scopes = %v", cfg.Scopes)
	}
	if cfg.Endpoint.AuthURL != AuthURL || cfg.Endpoint.TokenURL != TokenURL {
		t.Errorf("endpoints wrong: %+v", cfg.Endpoint)
	}
}

// A secret saved for another server in the collection is found.
func TestConfigFromSharedSecret(t *testing.T) {
	p := testProvider(t)
	if _, err := p.Config(); err == nil {
		t.Fatal("no credentials anywhere should fail")
	}
	dir := filepath.Join(p.BaseDir, "mcp-androidpublisher")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := `{"installed":{"client_id":"shared-id","client_secret":"shared-secret"}}`
	if err := os.WriteFile(filepath.Join(dir, "client_secret_x.json"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := p.Config()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientID != "shared-id" {
		t.Errorf("ClientID = %q, want shared-id", cfg.ClientID)
	}
}

func TestSetupHint(t *testing.T) {
	p := testProvider(t)
	_, err := p.LoadToken()
	wrapped := p.SetupHint(err)
	if !strings.Contains(wrapped.Error(), "Setup summary") || !strings.Contains(wrapped.Error(), "test_login") {
		t.Errorf("auth error should gain a setup hint, got %v", wrapped)
	}
	if !errors.Is(wrapped, ErrNoToken) {
		t.Error("hint must keep the wrapped error")
	}
	_, err = p.Config()
	if !strings.Contains(p.SetupHint(err).Error(), "Setup summary") {
		t.Errorf("missing credentials should gain a setup hint, got %v", err)
	}
	plain := errors.New("some API error")
	if got := p.SetupHint(plain); got != plain {
		t.Errorf("non-auth errors must pass through unchanged, got %v", got)
	}
}

func TestStatus(t *testing.T) {
	p := testProvider(t)
	s := p.Status()
	if s.CredsConfigured || s.TokenStored || s.FlowRunning {
		t.Errorf("fresh provider status = %+v", s)
	}
	out := p.RenderStatus(s, "")
	if !strings.Contains(out, "NOT configured") || !strings.Contains(out, "not started") {
		t.Errorf("unexpected status text:\n%s", out)
	}
}

func TestRenderStatus(t *testing.T) {
	p := testProvider(t)
	out := p.RenderStatus(Status{CredsConfigured: true, TokenStored: true, TokenPath: "/tmp/token.json"}, " (needs X)")
	if !strings.Contains(out, "present (/tmp/token.json)") || !strings.Contains(out, "Ready: call any test_* data tool now (needs X).") {
		t.Errorf("rendering wrong:\n%s", out)
	}
	out = p.RenderStatus(Status{FlowRunning: true, AuthURL: "https://example/consent"}, "")
	if !strings.Contains(out, "RUNNING") || !strings.Contains(out, "https://example/consent") {
		t.Errorf("running flow rendering wrong:\n%s", out)
	}
	out = p.RenderStatus(Status{CredsConfigured: true}, "")
	if !strings.Contains(out, "Next: call test_login") {
		t.Errorf("next-step hint missing:\n%s", out)
	}
}
