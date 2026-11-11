package config

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecode_minimalKeyringAccount(t *testing.T) {
	src := `
[[account]]
name       = "work"
server_url = "https://mail.example.com/Microsoft-Server-ActiveSync"
username   = "henry"
secret     = { keyring_service = "activesync-mcp", keyring_account = "work" }
`
	c, err := Decode(strings.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(c.Accounts) != 1 {
		t.Fatalf("want 1 account, got %d", len(c.Accounts))
	}
	a := c.Accounts[0]
	if a.Name != "work" {
		t.Errorf("name: %q", a.Name)
	}
	if a.DefaultAccess != AccessRO {
		t.Errorf("default_access default: %q, want %q", a.DefaultAccess, AccessRO)
	}
	if a.DeviceType != "MCP" {
		t.Errorf("device_type default: %q", a.DeviceType)
	}
	if a.ASVersion != "14.1" {
		t.Errorf("as_version default: %q", a.ASVersion)
	}
	if c.LogLevel != "info" {
		t.Errorf("log_level default: %q", c.LogLevel)
	}
}

func TestDecode_perClassAccess(t *testing.T) {
	src := `
[[account]]
name       = "work"
server_url = "https://x"
username   = "u"
secret     = { command = ["echo", "hi"] }
default_access = "ro"

[account.access]
calendar = "rw"
tasks    = "rw"
`
	c, err := Decode(strings.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	a := c.Accounts[0]
	if a.Access["calendar"] != AccessRW {
		t.Errorf("calendar access: %q", a.Access["calendar"])
	}
	if a.Access["tasks"] != AccessRW {
		t.Errorf("tasks access: %q", a.Access["tasks"])
	}
}

func TestDecode_unknownPasswordKeyIgnored(t *testing.T) {
	// The schema has no password field; an unknown TOML key is ignored
	// rather than rejected. (We rely on absence-from-schema as the
	// no-cleartext-secrets enforcement.)
	src := `
[[account]]
name       = "work"
server_url = "https://x"
username   = "u"
password   = "should-be-ignored"
secret     = { command = ["echo", "hi"] }
`
	if _, err := Decode(strings.NewReader(src)); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestDecode_validationErrors(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			name:    "no accounts",
			src:     ``,
			wantSub: "no [[account]]",
		},
		{
			name: "missing name",
			src: `
[[account]]
server_url = "https://x"
username   = "u"
secret     = { command = ["x"] }`,
			wantSub: "name is required",
		},
		{
			name: "duplicate name",
			src: `
[[account]]
name = "a"
server_url = "https://x"
username   = "u"
secret     = { command = ["x"] }
[[account]]
name = "a"
server_url = "https://x"
username   = "u"
secret     = { command = ["x"] }`,
			wantSub: "duplicate name",
		},
		{
			name: "missing username",
			src: `
[[account]]
name       = "a"
server_url = "https://x"
secret     = { command = ["x"] }`,
			wantSub: "username is required",
		},
		{
			name: "no secret source",
			src: `
[[account]]
name       = "a"
server_url = "https://x"
username   = "u"`,
			wantSub: "must set either",
		},
		{
			name: "secret keyring partial",
			src: `
[[account]]
name       = "a"
server_url = "https://x"
username   = "u"
secret     = { keyring_service = "svc" }`,
			wantSub: "keyring requires both",
		},
		{
			name: "secret both sources",
			src: `
[[account]]
name       = "a"
server_url = "https://x"
username   = "u"
secret     = { keyring_service = "s", keyring_account = "a", command = ["x"] }`,
			wantSub: "mutually exclusive",
		},
		{
			name: "bad default_access",
			src: `
[[account]]
name           = "a"
server_url     = "https://x"
username       = "u"
secret         = { command = ["x"] }
default_access = "yes"`,
			wantSub: "default_access must be",
		},
		{
			name: "unknown access class",
			src: `
[[account]]
name       = "a"
server_url = "https://x"
username   = "u"
secret     = { command = ["x"] }
[account.access]
mail = "rw"`,
			wantSub: "unknown class",
		},
		{
			name: "bad access value",
			src: `
[[account]]
name       = "a"
server_url = "https://x"
username   = "u"
secret     = { command = ["x"] }
[account.access]
email = "write"`,
			wantSub: "must be",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(strings.NewReader(tc.src))
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestFindAccount(t *testing.T) {
	c := &Config{
		Accounts: []Account{
			{Name: "a"},
			{Name: "b"},
		},
	}
	if c.FindAccount("a") == nil {
		t.Errorf("a not found")
	}
	if c.FindAccount("b") == nil {
		t.Errorf("b not found")
	}
	if c.FindAccount("c") != nil {
		t.Errorf("c should not be found")
	}
}

func TestExpandHome(t *testing.T) {
	if got := expandHome("/abs/path"); got != "/abs/path" {
		t.Errorf("absolute path mangled: %q", got)
	}
	got := expandHome("~/foo")
	if strings.HasPrefix(got, "~") {
		t.Errorf("home not expanded: %q", got)
	}
}

func TestLoad_filesystemRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `
[[account]]
name       = "work"
server_url = "https://x"
username   = "henry"
secret     = { command = ["echo", "p"] }
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Accounts) != 1 || c.Accounts[0].Name != "work" {
		t.Errorf("accounts = %+v", c.Accounts)
	}
}

func TestLoad_fileNotFound(t *testing.T) {
	_, err := Load("/no/such/path/config.toml")
	if err == nil || !strings.Contains(err.Error(), "open config") {
		t.Errorf("err = %v, want 'open config' wrap", err)
	}
}

func TestDecode_invalidTOML(t *testing.T) {
	_, err := Decode(strings.NewReader("not [valid toml"))
	if err == nil || !strings.Contains(err.Error(), "decode config") {
		t.Errorf("err = %v, want 'decode config' wrap", err)
	}
}

// errReader returns a non-EOF error on the first Read.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestDecode_readError(t *testing.T) {
	_, err := Decode(errReader{})
	if err == nil || !strings.Contains(err.Error(), "read config") {
		t.Errorf("err = %v, want 'read config' wrap", err)
	}
}

func TestDefaultPath_xdgConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
	got := DefaultPath()
	want := filepath.Join("/xdg/config", "activesync-mcp", "config.toml")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDefaultPath_homeFallback(t *testing.T) {
	// Unset XDG_CONFIG_HOME so DefaultPath falls through to home/.config.
	t.Setenv("XDG_CONFIG_HOME", "")
	got := DefaultPath()
	// We can't predict the home dir reliably (CI runs as different
	// users) but we know it ends in .config/activesync-mcp/config.toml.
	if !strings.HasSuffix(got, filepath.Join(".config", "activesync-mcp", "config.toml")) {
		t.Errorf("got %q; want a path ending in .config/activesync-mcp/config.toml", got)
	}
}

func TestDefaultStateDir_xdgStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	want := filepath.Join("/xdg/state", "activesync-mcp")
	if got := defaultStateDir(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDefaultStateDir_homeFallbackIsStable(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	got := defaultStateDir()
	wantSuffix := filepath.Join(".local", "state", "activesync-mcp")
	if !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("got %q; want a path ending in %q", got, wantSuffix)
	}
	if strings.Contains(filepath.Base(got), "pid-") {
		t.Fatalf("state directory must be stable across processes: %q", got)
	}
}

func TestSecretValidate_emptyCommandName(t *testing.T) {
	src := `
[[account]]
name       = "a"
server_url = "https://x"
username   = "u"
secret     = { command = ["   "] }
`
	_, err := Decode(strings.NewReader(src))
	if err == nil || !strings.Contains(err.Error(), "command[0] is empty") {
		t.Errorf("err = %v", err)
	}
}

func TestSecretValidate_invalidAuthScheme(t *testing.T) {
	src := `
[[account]]
name       = "a"
server_url = "https://x"
username   = "u"
secret     = { command = ["x"], auth_scheme = "weird" }
`
	_, err := Decode(strings.NewReader(src))
	if err == nil || !strings.Contains(err.Error(), "auth_scheme must be one of") {
		t.Errorf("err = %v", err)
	}
}

func TestSecretValidate_negotiateSkipsSecretSourceCheck(t *testing.T) {
	// auth_scheme=negotiate intentionally allows omitting both
	// keyring_service+keyring_account and command — credentials come
	// from the user's Kerberos ccache or keytab.
	src := `
[[account]]
name       = "a"
server_url = "https://x"
username   = "u"
secret     = { auth_scheme = "negotiate" }
`
	c, err := Decode(strings.NewReader(src))
	if err != nil {
		t.Fatalf("err = %v (negotiate should permit no secret source)", err)
	}
	if c.Accounts[0].Secret.AuthScheme != "negotiate" {
		t.Errorf("AuthScheme = %q", c.Accounts[0].Secret.AuthScheme)
	}
}

func TestDecode_serverURLOptional(t *testing.T) {
	// server_url is optional — empty value triggers serve-time
	// autodiscover via the Manager. The validator must accept the
	// account; defaults are still applied.
	src := `
[[account]]
name       = "work"
username   = "henry@example.com"
secret     = { command = ["echo", "p"] }
discovery_required = true
`
	c, err := Decode(strings.NewReader(src))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if c.Accounts[0].ServerURL != "" {
		t.Errorf("ServerURL = %q, want empty", c.Accounts[0].ServerURL)
	}
	if !c.Accounts[0].DiscoveryRequired {
		t.Error("DiscoveryRequired should round-trip from TOML")
	}
}

func TestSecretValidate_authSchemeBearer(t *testing.T) {
	src := `
[[account]]
name       = "a"
server_url = "https://x"
username   = "u"
secret     = { command = ["x"], auth_scheme = "bearer" }
`
	if _, err := Decode(strings.NewReader(src)); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestExpandHome_homeEnvUnreadable(t *testing.T) {
	// On a system where UserHomeDir errors, expandHome returns the
	// input unchanged. We can't easily simulate that here, but we can
	// at least verify expandHome returns something that doesn't start
	// with "~" when the home lookup succeeds.
	got := expandHome("~")
	if got == "~" {
		t.Errorf("expandHome(~) returned %q; expected resolution to home dir", got)
	}
}
