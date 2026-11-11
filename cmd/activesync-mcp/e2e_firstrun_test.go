// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

//go:build e2e

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zalando/go-keyring"

	"activesync-mcp/lib/server"
)

// TestE2E_FirstRunSequence orchestrates the full setup journey end to
// end: write a config that references a keyring entry → store the
// password in the OS keyring (as `setup` would) → run `doctor` → spawn `serve` and call
// accounts_list → SIGTERM the serve process. Catches integration
// failures across the keyring → secret-resolution → manager → MCP →
// signal-handling chain that no narrower test would surface.
func TestE2E_FirstRunSequence(t *testing.T) {
	bin := e2eBinary(t)
	stateDir := t.TempDir()
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.toml")

	// Skip if keyring isn't reachable (headless CI without a Secret
	// Service provider) — falls under Tier 2 keyring tests anyway.
	const svc = "activesync-mcp-e2e"
	acct := fmt.Sprintf("firstrun-%d", time.Now().UnixNano())
	if err := keyring.Set(svc, acct, "probe"); err != nil {
		t.Skipf("OS keyring unreachable: %v", err)
	}
	_ = keyring.Delete(svc, acct)

	body := fmt.Sprintf(`state_dir = %q
[[account]]
name             = "test"
server_url       = %q
username         = "integration"
as_version       = "14.0"
secret           = { keyring_service = %q, keyring_account = %q }
default_access   = "rw"
`, escapeTOMLE2E(stateDir), e2eServerURL(), svc, acct)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. Store the testenv password in the OS keyring the way the setup
	// TUI would (the `keyring` subcommand was replaced by setup).
	if err := keyring.Set(svc, acct, "integration"); err != nil {
		t.Fatalf("keyring set: %v", err)
	}
	t.Cleanup(func() { _ = keyring.Delete(svc, acct) })

	// 2. doctor against the live testenv → exit 0.
	doctorCmd := exec.Command(bin, "doctor", "--config", cfgPath)
	var docOut, docErr bytes.Buffer
	doctorCmd.Stdout = &docOut
	doctorCmd.Stderr = &docErr
	if err := doctorCmd.Run(); err != nil {
		t.Fatalf("doctor: %v\nstdout: %s\nstderr: %s", err, docOut.String(), docErr.String())
	}
	if !strings.Contains(docOut.String(), "OK") {
		t.Errorf("doctor stdout missing OK:\n%s", docOut.String())
	}

	// 3. spawn serve, connect MCP client, call accounts_list.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	serveCmd := exec.Command(bin, "serve", "--config", cfgPath)
	serveCmd.Stderr = testWriter{t}
	transport := &mcp.CommandTransport{Command: serveCmd}
	c := mcp.NewClient(&mcp.Implementation{Name: "firstrun", Version: "0"}, nil)
	cs, err := c.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "accounts_list",
		Arguments: server.AccountsListInput{},
	})
	if err != nil {
		t.Fatalf("accounts_list: %v", err)
	}
	if res.IsError {
		t.Fatalf("accounts_list IsError: %s", formatContent(res.Content))
	}

	// 4. clean shutdown via session Close (sends SIGTERM via CommandTransport).
	if err := cs.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
