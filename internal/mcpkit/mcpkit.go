// Package mcpkit holds the MCP plumbing shared by every server: the
// command-line entry point (serve / login / logout), the *_login tools, and
// tool result helpers.
package mcpkit

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/homielab/homielab-mcp/internal/googleauth"
)

// Server describes one MCP server binary.
type Server struct {
	// Auth is the Google OAuth provider; it adds the login and logout
	// commands. Nil for servers with their own credentials.
	Auth         *googleauth.Provider
	Name         string // binary name; empty = Auth.Name
	Version      string
	Summary      string // one line for --help, e.g. "MCP server for the Google AdSense Management API (read-only)"
	Instructions string // MCP server instructions sent to the client
	// Credentials describes the credential lookup for --help when Auth is nil.
	Credentials string
	// Commands are extra CLI subcommands.
	Commands []Command
	Register func(*mcp.Server)
}

// Command is a CLI subcommand; Run gets the arguments after its name.
type Command struct {
	Name  string
	Usage string // one line for --help, e.g. "store an API key"
	Run   func(ctx context.Context, args []string) error
}

// Main dispatches os.Args: no argument, "serve" or "run" serves MCP on stdio;
// with Auth, "login" and "logout" manage the OAuth token; Commands add more.
func Main(s Server) {
	name := s.Name
	if name == "" {
		name = s.Auth.Name
	}
	s.Name = name
	log.SetPrefix(name + ": ")
	log.SetFlags(log.LstdFlags)

	cmds := s.Commands
	credentials := s.Credentials
	if s.Auth != nil {
		cmds = append([]Command{
			{Name: "login", Usage: "authorize with Google in the default browser", Run: func(ctx context.Context, _ []string) error {
				return login(ctx, s.Auth)
			}},
			{Name: "logout", Usage: "delete the stored OAuth token", Run: func(ctx context.Context, _ []string) error {
				if err := s.Auth.ClearToken(); err != nil {
					return err
				}
				fmt.Println("Token deleted.")
				return nil
			}},
		}, cmds...)
		credentials = s.Auth.CredentialSources()
	}

	var lines strings.Builder
	fmt.Fprintf(&lines, "  %-*s run the MCP server on stdio (default)\n", len(name)+10, name)
	for _, c := range cmds {
		fmt.Fprintf(&lines, "  %-*s %s\n", len(name)+10, name+" "+c.Name, c.Usage)
	}
	usage := fmt.Sprintf("%s %s — %s\n\nUsage:\n%s\nCredential sources (tried in order):\n%s", name, s.Version, s.Summary, lines.String(), credentials)

	ctx := context.Background()
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "", "serve", "run":
		if err := s.serve(ctx); err != nil {
			log.Fatalf("server exited: %v", err)
		}
		return
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	}
	for _, c := range cmds {
		if c.Name == cmd {
			if err := c.Run(ctx, os.Args[2:]); err != nil {
				log.Fatalf("%s: %v", cmd, err)
			}
			return
		}
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
	os.Exit(2)
}

func (s Server) serve(ctx context.Context) error {
	server := mcp.NewServer(&mcp.Implementation{Name: s.Name, Version: s.Version}, &mcp.ServerOptions{
		Instructions: s.Instructions,
		SetCacheable: func(ctx context.Context, req mcp.Request, c *mcp.Cacheable) {
			// Static capability/list results: safe to cache for 5 minutes
			// per the 2026-07-28 caching utility.
			c.TTLMs = 300000
			c.CacheScope = "public"
		},
	})
	s.Register(server)
	return server.Run(ctx, &mcp.StdioTransport{})
}

func login(ctx context.Context, a *googleauth.Provider) error {
	cfg, err := a.Config()
	if err != nil {
		return err
	}
	tok, err := a.Login(ctx, cfg, nil)
	if err != nil {
		return err
	}
	fmt.Printf("Authorized successfully (token expires %s).\n", tok.Expiry.Format("2006-01-02 15:04 MST"))
	return nil
}

// EmptyArgs is the input type of tools without parameters.
type EmptyArgs struct{}

// TextResult returns a plain-text tool result.
func TextResult(s string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}, nil, nil
}

// ErrResult returns err as a tool error, with setup steps for auth errors.
func ErrResult(a *googleauth.Provider, err error) (*mcp.CallToolResult, any, error) {
	return ErrorResult(a.SetupHint(err))
}

// ErrorResult returns err as a tool error.
func ErrorResult(err error) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
		IsError: true,
	}, nil, nil
}

// LoginTools configures the <prefix>_login, _login_status and _logout tools.
type LoginTools struct {
	// Description of the login tool.
	Description string
	// ReadyNote is appended to the status "Ready: call any <prefix>_* data
	// tool now" line, e.g. " (needs a packageName)".
	ReadyNote string
	// Authorized, when set and a token is stored, confirms the token works.
	// It returns a message and true to skip a new consent flow.
	Authorized func(ctx context.Context) (string, bool)
}

// AddLoginTools registers the login, login status and logout tools.
func AddLoginTools(s *mcp.Server, a *googleauth.Provider, lt LoginTools) {
	p := a.ToolPrefix

	mcp.AddTool(s, &mcp.Tool{
		Name:        p + "_login",
		Title:       a.Title + " Login",
		Description: lt.Description,
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ EmptyArgs) (*mcp.CallToolResult, any, error) {
		if a.TokenStored() && lt.Authorized != nil {
			if msg, ok := lt.Authorized(ctx); ok {
				return TextResult(msg)
			}
		}
		url, running, err := a.StartLogin()
		if err != nil {
			return ErrResult(a, err)
		}
		if running {
			// url may still be empty for a flow that just started (the consent
			// URL is generated asynchronously).
			msg := "A login flow is already waiting for your approval in the browser."
			if url != "" {
				msg += " If no browser opened, visit:\n" + url
			}
			msg += "\nThen call " + p + "_login_status to confirm."
			return TextResult(msg)
		}
		msg := "Started the authorization flow — your browser should open at Google's consent page. " +
			"Sign in with " + a.Account + " and approve the request, then call " + p + "_login_status to confirm."
		if url != "" {
			msg += "\nIf the browser did not open, visit:\n" + url
		}
		return TextResult(msg)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        p + "_login_status",
		Title:       a.Title + " Login Status",
		Description: "Check whether the " + a.Title + " OAuth flow is running/finished and whether a stored token exists. Use right after " + p + "_login.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ EmptyArgs) (*mcp.CallToolResult, any, error) {
		return TextResult(a.RenderStatus(a.Status(), lt.ReadyNote))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        p + "_logout",
		Title:       a.Title + " Logout",
		Description: "Delete the locally stored " + a.Title + " OAuth token.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ EmptyArgs) (*mcp.CallToolResult, any, error) {
		if err := a.ClearToken(); err != nil {
			return ErrResult(a, err)
		}
		return TextResult("Token deleted. Re-run " + p + "_login to authorize again.")
	})
}
