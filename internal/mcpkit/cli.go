package mcpkit

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"strings"

	"github.com/homielab/homielab-mcp/internal/appdir"
)

// Service is one entry of the mcp-homielab service list.
type Service struct {
	Title string // short label for the service list, e.g. "Google AdSense (read-only)"
	New   func() Server
}

// Main dispatches os.Args across services:
//
//	mcp-homielab <service> [command]   serve, login, setup, --help, ...
//	mcp-homielab add [flags] <service>... | all
//	mcp-homielab version
func Main(version string, services []Service) {
	version = resolveVersion(version)
	log.SetFlags(log.LstdFlags)
	log.SetPrefix(appdir.Name + ": ")

	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Print(mainUsage(version, services))
		return
	}
	ctx := context.Background()
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(mainUsage(version, services))
		return
	case "-v", "--version", "version":
		fmt.Println(appdir.Name, version)
		return
	case "add":
		if err := runAdd(args[1:], services); err != nil {
			fmt.Fprintln(os.Stderr, "add:", err)
			os.Exit(1)
		}
		return
	}
	for _, svc := range services {
		s := svc.New()
		if s.id() != args[0] {
			continue
		}
		log.SetPrefix(appdir.Name + " " + s.id() + ": ")
		if err := s.run(ctx, version, args[1:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	fmt.Fprintf(os.Stderr, "unknown service or command %q\n\n%s", args[0], mainUsage(version, services))
	os.Exit(2)
}

// resolveVersion falls back to the module version for "go install …@vX".
func resolveVersion(v string) string {
	if v != "" && v != "dev" {
		return v
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func mainUsage(version string, services []Service) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s — MCP servers for app, extension and web publishers\n\n", appdir.Name, version)
	b.WriteString("Usage:\n")
	fmt.Fprintf(&b, "  %s <service>                 run that service's MCP server on stdio\n", appdir.Name)
	fmt.Fprintf(&b, "  %s <service> login|setup     sign in / store an API key (see <service> --help)\n", appdir.Name)
	fmt.Fprintf(&b, "  %s <service> --help          commands and credential lookup of one service\n", appdir.Name)
	fmt.Fprintf(&b, "  %s add <service>... | all    register services with Claude Code\n", appdir.Name)
	fmt.Fprintf(&b, "  %s add --client <c> ...      claude-code, claude-desktop, cursor, or json (print)\n", appdir.Name)
	fmt.Fprintf(&b, "  %s version\n\nServices:\n", appdir.Name)
	for _, svc := range services {
		s := svc.New()
		var cmds []string
		for _, c := range s.commands() {
			cmds = append(cmds, c.Name)
		}
		fmt.Fprintf(&b, "  %-16s %-40s %s\n", s.id(), svc.Title, strings.Join(cmds, ", "))
	}
	return b.String()
}
