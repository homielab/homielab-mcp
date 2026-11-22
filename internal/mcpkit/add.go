package mcpkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const addUsage = `Usage: mcp-homielab add [--client <client>] [--scope <scope>] <service>... | all

Registers services with an MCP client. Each one runs as "<this binary> <service>".
  --client  claude-code (default), claude-desktop, cursor, or json (print the config)
  --scope   claude-code scope: user (default), project or local
`

// runAdd registers services with an MCP client.
func runAdd(args []string, services []Service) error {
	client, scope := "claude-code", "user"
	var ids []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, val, hasVal := strings.Cut(a, "=")
		switch name {
		case "-h", "--help":
			fmt.Print(addUsage)
			return nil
		case "-c", "--client", "--scope":
			if !hasVal {
				if i+1 >= len(args) {
					return fmt.Errorf("%s needs a value\n\n%s", name, addUsage)
				}
				i++
				val = args[i]
			}
			if name == "--scope" {
				scope = val
			} else {
				client = val
			}
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("unknown flag %q\n\n%s", a, addUsage)
			}
			ids = append(ids, a)
		}
	}
	ids, err := resolveIDs(ids, services)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	switch client {
	case "claude-code", "claude":
		return addClaudeCode(exe, scope, ids)
	case "json":
		out, _ := json.MarshalIndent(map[string]any{"mcpServers": serverEntries(exe, ids)}, "", "  ")
		fmt.Println(string(out))
		return nil
	case "claude-desktop", "cursor":
		path, err := clientConfigPath(client)
		if err != nil {
			return err
		}
		if err := upsertServers(path, serverEntries(exe, ids)); err != nil {
			return err
		}
		fmt.Printf("Added %s to %s. Restart %s to load them.\n", strings.Join(ids, ", "), path, client)
		return nil
	}
	return fmt.Errorf("unknown client %q\n\n%s", client, addUsage)
}

// resolveIDs expands "all" and rejects unknown service ids.
func resolveIDs(ids []string, services []Service) ([]string, error) {
	var known []string
	for _, svc := range services {
		known = append(known, svc.New().id())
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("name the services to add, or all: %s\n\n%s", strings.Join(known, " "), addUsage)
	}
	if len(ids) == 1 && ids[0] == "all" {
		return known, nil
	}
	for _, id := range ids {
		found := false
		for _, k := range known {
			found = found || k == id
		}
		if !found {
			return nil, fmt.Errorf("unknown service %q; choose from: %s", id, strings.Join(known, " "))
		}
	}
	return ids, nil
}

func serverEntries(exe string, ids []string) map[string]any {
	m := map[string]any{}
	for _, id := range ids {
		m[id] = map[string]any{"command": exe, "args": []string{id}}
	}
	return m
}

func addClaudeCode(exe, scope string, ids []string) error {
	if _, err := exec.LookPath("claude"); err != nil {
		return errors.New(`"claude" (Claude Code) is not on PATH; use --client claude-desktop, cursor or json`)
	}
	var failed []string
	for _, id := range ids {
		cmd := exec.Command("claude", "mcp", "add", "--scope", scope, id, "--", exe, id)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			failed = append(failed, id)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("claude mcp add failed for %s (already added? remove it with: claude mcp remove --scope %s <name>)", strings.Join(failed, ", "), scope)
	}
	return nil
}

// clientConfigPath returns the JSON config file of a desktop MCP client.
func clientConfigPath(client string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if client == "cursor" {
		return filepath.Join(home, ".cursor", "mcp.json"), nil
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"), nil
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "Claude", "claude_desktop_config.json"), nil
	default:
		return filepath.Join(home, ".config", "Claude", "claude_desktop_config.json"), nil
	}
}

// upsertServers sets mcpServers entries in a JSON config file, keeping every
// other key. A file that is not valid JSON is left untouched.
func upsertServers(path string, entries map[string]any) error {
	cfg := map[string]any{}
	mode := os.FileMode(0o600)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if fi, err := os.Stat(path); err == nil {
			mode = fi.Mode().Perm()
		}
		if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, &cfg); err != nil {
				return fmt.Errorf("%s is not valid JSON, not changing it: %w", path, err)
			}
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	for k, v := range entries {
		servers[k] = v
	}
	cfg["mcpServers"] = servers
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
