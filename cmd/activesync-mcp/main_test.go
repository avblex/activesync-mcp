package main

import (
	"os"
	"strings"
	"testing"

	"activesync-mcp/lib/config"
)

func TestDeviceSeedForStateSlot(t *testing.T) {
	first := deviceSeedForStateSlot(1)
	second := deviceSeedForStateSlot(2)
	if first == second {
		t.Fatalf("state slots produced the same device seed %q", first)
	}
	if again := deviceSeedForStateSlot(1); again != first {
		t.Fatalf("slot seed is not stable: %q then %q", first, again)
	}
}

func TestScopeConfiguredDeviceIDsToStateSlot(t *testing.T) {
	original := "0123456789abcdef0123456789abcdef"
	first := &config.Config{Accounts: []config.Account{{Name: "work", DeviceID: original}}}
	scopeConfiguredDeviceIDs(first, 1)
	if first.Accounts[0].DeviceID != original {
		t.Fatalf("slot 1 device ID changed: %q", first.Accounts[0].DeviceID)
	}

	second := &config.Config{Accounts: []config.Account{{Name: "work", DeviceID: original}}}
	scopeConfiguredDeviceIDs(second, 2)
	got := second.Accounts[0].DeviceID
	if got == original || len(got) != 32 {
		t.Fatalf("slot 2 device ID = %q; want a distinct 32-hex ID", got)
	}
	again := &config.Config{Accounts: []config.Account{{Name: "work", DeviceID: original}}}
	scopeConfiguredDeviceIDs(again, 2)
	if again.Accounts[0].DeviceID != got {
		t.Fatalf("slot 2 device ID is not stable: %q then %q", got, again.Accounts[0].DeviceID)
	}
}

func TestSplitSubcommand(t *testing.T) {
	cases := []struct {
		argv     []string
		wantSub  string
		wantRest []string
	}{
		{nil, "", nil},
		{[]string{}, "", []string{}},
		{[]string{"--config", "x"}, "", []string{"--config", "x"}},
		{[]string{"serve"}, "serve", []string{}},
		{[]string{"keyring", "set", "--account", "a"}, "keyring", []string{"set", "--account", "a"}},
	}
	for i, tc := range cases {
		sub, rest := splitSubcommand(tc.argv)
		if sub != tc.wantSub {
			t.Errorf("case %d: sub = %q, want %q", i, sub, tc.wantSub)
		}
		if len(rest) != len(tc.wantRest) {
			t.Errorf("case %d: rest = %v, want %v", i, rest, tc.wantRest)
			continue
		}
		for j := range rest {
			if rest[j] != tc.wantRest[j] {
				t.Errorf("case %d: rest[%d] = %q, want %q", i, j, rest[j], tc.wantRest[j])
			}
		}
	}
}

func TestRun_helpExits0(t *testing.T) {
	out, errF, read := tempStdio(t)
	code := run([]string{"help"}, out, errF)
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	stdout, _ := read()
	if !strings.Contains(stdout, "MCP server") {
		t.Errorf("stdout missing usage: %q", stdout)
	}
}

func TestRun_unknownSubcommand(t *testing.T) {
	out, errF, read := tempStdio(t)
	code := run([]string{"frobnicate"}, out, errF)
	if code != exitUsageErr {
		t.Fatalf("exit %d", code)
	}
	_, e := read()
	if !strings.Contains(e, "unknown subcommand") {
		t.Errorf("stderr: %q", e)
	}
}

func TestLoadConfigOrHint_notExistPrintsBootstrap(t *testing.T) {
	_, errF, read := tempStdio(t)
	_, err := loadConfigOrHint("/no/such/path/config.toml", errF)
	if err == nil {
		t.Fatal("want error")
	}
	_, e := read()
	for _, want := range []string{
		"no config file at /no/such/path/config.toml",
		"activesync-mcp setup",
	} {
		if !strings.Contains(e, want) {
			t.Errorf("stderr missing %q\n--- stderr ---\n%s", want, e)
		}
	}
}

func TestLoadConfigOrHint_otherErrorPrintsVerbatim(t *testing.T) {
	// Write a syntactically invalid config: hits the decode-error
	// branch (not fs.ErrNotExist) so the helper falls through to the
	// generic "activesync-mcp: ..." print.
	dir := t.TempDir()
	bad := dir + "/config.toml"
	if err := os.WriteFile(bad, []byte("this is not [valid toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errF, read := tempStdio(t)
	_, err := loadConfigOrHint(bad, errF)
	if err == nil {
		t.Fatal("want error")
	}
	_, e := read()
	if strings.Contains(e, "activesync-mcp setup") {
		t.Errorf("decode error should NOT point at the setup TUI: %q", e)
	}
	if !strings.Contains(e, "decode config") {
		t.Errorf("stderr should surface the underlying error: %q", e)
	}
}

func TestRun_serveBadConfig(t *testing.T) {
	out, errF, read := tempStdio(t)
	code := run([]string{"serve", "--config", "/no/such/path/config.toml"}, out, errF)
	if code != exitConfig {
		t.Fatalf("exit %d", code)
	}
	_, e := read()
	// File-not-found is the canonical first-run case, so the helper
	// points the user at the interactive setup TUI instead of a
	// bare 'open config' error.
	if !strings.Contains(e, "no config file") || !strings.Contains(e, "activesync-mcp setup") {
		t.Errorf("stderr: %q", e)
	}
}
