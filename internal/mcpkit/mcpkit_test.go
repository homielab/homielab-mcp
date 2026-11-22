package mcpkit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/googleauth"
)

func TestLoginTools(t *testing.T) {
	t.Setenv("TEST_MCP_CLIENT_ID", "")
	t.Setenv("TEST_MCP_CLIENT_SECRET", "")
	t.Setenv("TEST_MCP_CLIENT_SECRET_FILE", "")
	t.Chdir(t.TempDir())
	a := &googleauth.Provider{Name: "test", Title: "Test", EnvPrefix: "TEST_MCP", ToolPrefix: "test", BaseDir: t.TempDir()}

	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	AddLoginTools(server, a, LoginTools{Description: "login"})
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	call := func(name string) (string, bool) {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return res.Content[0].(*mcp.TextContent).Text, res.IsError
	}

	if out, isErr := call("test_login_status"); isErr || !strings.Contains(out, "Test MCP login status") {
		t.Errorf("test_login_status = %q (error %v)", out, isErr)
	}
	// No credentials anywhere: login fails with setup steps instead of opening a browser.
	if out, isErr := call("test_login"); !isErr || !strings.Contains(out, "Setup summary") {
		t.Errorf("test_login = %q (error %v)", out, isErr)
	}
	if out, isErr := call("test_logout"); isErr || !strings.Contains(out, "Token deleted") {
		t.Errorf("test_logout = %q (error %v)", out, isErr)
	}
}

// add keeps the rest of a client's config file and rejects invalid JSON.
func TestUpsertServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	orig := `{"theme":"dark","mcpServers":{"other":{"command":"x"},"adsense":{"command":"old"}}}`
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := upsertServers(path, serverEntries("/bin/mcp-homielab", []string{"adsense", "gemini"})); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Theme      string `json:"theme"`
		McpServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Theme != "dark" || got.McpServers["other"].Command != "x" {
		t.Errorf("other keys lost: %s", data)
	}
	if s := got.McpServers["adsense"]; s.Command != "/bin/mcp-homielab" || len(s.Args) != 1 || s.Args[0] != "adsense" {
		t.Errorf("adsense = %+v", s)
	}
	if _, ok := got.McpServers["gemini"]; !ok {
		t.Error("gemini not added")
	}

	if err := os.WriteFile(path, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := upsertServers(path, serverEntries("/x", []string{"adsense"})); err == nil {
		t.Error("invalid JSON should fail")
	}
	if data, _ := os.ReadFile(path); string(data) != "{broken" {
		t.Errorf("invalid file was changed: %s", data)
	}
}
