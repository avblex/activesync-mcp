// Command activesync-mcp is the MCP server entry point.
//
// Subcommands:
//
//	serve   (default) run the stdio MCP server
//	setup   interactive TUI to add/edit/delete accounts and credentials
//	doctor  validate config and probe each account's server
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"activesync-mcp/lib/config"
	"activesync-mcp/lib/server"
	"activesync-mcp/lib/store"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// exit codes
const (
	exitOK       = 0
	exitConfig   = 2
	exitRuntime  = 3
	exitUsageErr = 64
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. It parses argv (without the program name),
// dispatches to a subcommand, and returns an exit code.
func run(argv []string, stdout, stderr *os.File) int {
	configPath := config.DefaultPath()

	// Subcommand dispatch is positional: the first non-flag arg picks the
	// subcommand. A bare invocation runs `serve`.
	sub, rest := splitSubcommand(argv)
	switch sub {
	case "", "serve":
		return runServe(rest, &configPath, stderr)
	case "setup":
		return runSetup(rest, &configPath, stdout, stderr)
	case "doctor":
		return runDoctor(rest, &configPath, stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "activesync-mcp: unknown subcommand %q\n\n", sub)
		printUsage(stderr)
		return exitUsageErr
	}
}

// splitSubcommand pulls a leading positional arg out of argv; the rest is
// passed to the subcommand for further flag parsing.
func splitSubcommand(argv []string) (sub string, rest []string) {
	if len(argv) == 0 || (len(argv[0]) > 0 && argv[0][0] == '-') {
		return "", argv
	}
	return argv[0], argv[1:]
}

func runServe(argv []string, configPath *string, stderr *os.File) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(configPath, "config", *configPath, "path to config.toml")
	if err := fs.Parse(argv); err != nil {
		return exitUsageErr
	}

	// Install the signal trap as early as possible — before any I/O —
	// so a Ctrl+C during startup translates into ctx-cancel rather
	// than the default kill-on-SIGINT runtime behaviour. Slow
	// machines or busy networks can take seconds to load config / open
	// bbolt / build the manager, and a user impatient enough to hit
	// Ctrl+C during that window deserves a clean exit, not a SIGINT
	// trap that hasn't been wired yet.
	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := loadConfigOrHint(*configPath, stderr)
	if err != nil {
		return exitConfig
	}

	db, err := store.OpenPool(filepath.Join(cfg.StateDir, "state.db"), 4)
	if err != nil {
		fmt.Fprintf(stderr, "activesync-mcp: %v\n", err)
		return exitConfig
	}
	defer db.Close()
	scopeConfiguredDeviceIDs(cfg, db.Slot())

	mgr, err := server.NewDefaultManager(cfg, db, deviceSeedForStateSlot(db.Slot()))
	if err != nil {
		fmt.Fprintf(stderr, "activesync-mcp: %v\n", err)
		return exitConfig
	}
	// Wire the persistent folder cache so *_list_folders tools
	// return the cumulative hierarchy instead of FolderSync deltas.
	mgr.SetFolderCache(folderCacheAdapter{db: db})
	// Pre-warm any accounts marked discovery_required=true. If
	// autodiscover is going to fail for one of them, fail the whole
	// serve here rather than silently degrading at first tool call.
	if err := mgr.WarmRequired(ctx); err != nil {
		fmt.Fprintf(stderr, "activesync-mcp: %v\n", err)
		return exitRuntime
	}
	srv := server.Build(cfg, mgr)

	push := server.NewPushController(cfg, mgr, srv)
	defer push.Close()
	push.Start(ctx)

	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(stderr, "activesync-mcp: %v\n", err)
		return exitRuntime
	}
	return exitOK
}

// folderCacheAdapter wraps *store.DB so it satisfies
// server.FolderCacheProvider. The wrap is necessary because
// (*store.DB).FolderCache returns the concrete *store.FolderCache
// while the manager's interface wants server.FolderCache; without
// the adapter Go's invariance on return types refuses the assignment.
type folderCacheAdapter struct{ db *store.DB }

func (a folderCacheAdapter) FolderCache(account string) server.FolderCache {
	return a.db.FolderCache(account)
}

// hostnameSeed returns a per-installation seed string for device-id
// derivation: hostname combined with the user's home directory. Stable
// across process restarts on the same machine; unique enough across
// installations that two laptops won't collide.
func hostnameSeed() string {
	host, _ := os.Hostname()
	home, _ := os.UserHomeDir()
	return host + "|" + home
}

func deviceSeedForStateSlot(slot int) string {
	return fmt.Sprintf("%s|state-slot-%d", hostnameSeed(), slot)
}

// scopeConfiguredDeviceIDs keeps manually configured device IDs distinct when
// Claude starts concurrent servers. Slot one retains the configured identity;
// later slots get a deterministic 32-hex identity tied to that stable slot.
func scopeConfiguredDeviceIDs(cfg *config.Config, slot int) {
	if slot <= 1 {
		return
	}
	for i := range cfg.Accounts {
		if cfg.Accounts[i].DeviceID == "" {
			continue
		}
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|state-slot-%d", cfg.Accounts[i].DeviceID, slot)))
		cfg.Accounts[i].DeviceID = hex.EncodeToString(sum[:16])
	}
}

// loadConfigOrHint wraps config.Load: on a "file not found" error it
// prints a copy-pasteable bootstrap walkthrough on stderr (so a user
// invoking `serve`/`doctor` for the first time isn't left
// with a bare "no such file" message). On any other error it prints
// the error verbatim. In either case the returned err is non-nil and
// the caller should return exitConfig.
func loadConfigOrHint(path string, stderr *os.File) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err == nil {
		return cfg, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(stderr, `activesync-mcp: no config file at %s

To create one, run:
  activesync-mcp setup

The setup TUI walks you through adding accounts, stores credentials
in the OS keyring, and tests the connection before saving.
`, path)
		return nil, err
	}
	fmt.Fprintf(stderr, "activesync-mcp: %v\n", err)
	return nil, err
}

func printUsage(w *os.File) {
	fmt.Fprintf(w, `activesync-mcp — MCP server exposing email, calendar, contacts, tasks, and notes via ActiveSync

Usage:
  activesync-mcp [serve] [--config PATH]
  activesync-mcp setup [--config PATH]
  activesync-mcp doctor [--config PATH]

Default config path: %s

The 'serve' subcommand starts the MCP stdio server (this is the default).
The 'setup' subcommand opens an interactive TUI for adding, editing, and
deleting accounts. It manages the OS keyring entries for each account
and offers a connection test before saving. Use it for first-run config
or any later credential change.
The 'doctor' subcommand validates the config and runs an HTTP OPTIONS
request against each account's server (no Provision, no state mutation).
`, config.DefaultPath())
}
