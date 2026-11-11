package config

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestResolve_keyring(t *testing.T) {
	r := &SecretResolver{
		Keyring: func(service, user string) (string, error) {
			if service != "activesync-mcp" || user != "work" {
				t.Errorf("unexpected lookup: service=%q user=%q", service, user)
			}
			return "supersecret", nil
		},
	}
	a := &Account{
		Name:   "work",
		Secret: SecretRef{KeyringService: "activesync-mcp", KeyringAccount: "work"},
	}
	pw, err := r.Resolve(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if pw != "supersecret" {
		t.Errorf("pw = %q", pw)
	}
}

func TestResolve_keyringNotFound(t *testing.T) {
	r := &SecretResolver{
		Keyring: func(string, string) (string, error) { return "", keyring.ErrNotFound },
	}
	a := &Account{
		Name:   "work",
		Secret: SecretRef{KeyringService: "s", KeyringAccount: "u"},
	}
	_, err := r.Resolve(context.Background(), a)
	if err == nil || !strings.Contains(err.Error(), "activesync-mcp setup") {
		t.Errorf("want hint to run the setup TUI, got %v", err)
	}
}

func TestResolve_keyringEmpty(t *testing.T) {
	r := &SecretResolver{
		Keyring: func(string, string) (string, error) { return "", nil },
	}
	a := &Account{
		Name:   "work",
		Secret: SecretRef{KeyringService: "s", KeyringAccount: "u"},
	}
	_, err := r.Resolve(context.Background(), a)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("want empty error, got %v", err)
	}
}

func TestResolve_command(t *testing.T) {
	r := &SecretResolver{
		Run: func(_ context.Context, argv []string) (string, error) {
			if argv[0] != "pass" || argv[1] != "show" {
				t.Errorf("argv = %v", argv)
			}
			return "secret-value\n", nil
		},
	}
	a := &Account{Secret: SecretRef{Command: []string{"pass", "show", "mail/work"}}}
	pw, err := r.Resolve(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if pw != "secret-value" {
		t.Errorf("pw = %q (trailing newline not trimmed?)", pw)
	}
}

func TestResolve_commandError(t *testing.T) {
	r := &SecretResolver{
		Run: func(context.Context, []string) (string, error) {
			return "", errors.New("boom")
		},
	}
	a := &Account{Secret: SecretRef{Command: []string{"x"}}}
	_, err := r.Resolve(context.Background(), a)
	if err == nil || !strings.Contains(err.Error(), "secret command") {
		t.Errorf("err = %v", err)
	}
}

func TestResolve_commandEmptyOutput(t *testing.T) {
	r := &SecretResolver{
		Run: func(context.Context, []string) (string, error) { return "\n", nil },
	}
	a := &Account{Secret: SecretRef{Command: []string{"x"}}}
	_, err := r.Resolve(context.Background(), a)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("err = %v", err)
	}
}

func TestResolve_nilAccount(t *testing.T) {
	r := DefaultResolver()
	if _, err := r.Resolve(context.Background(), nil); err == nil {
		t.Error("want error for nil account")
	}
}

func TestResolve_noSource(t *testing.T) {
	r := &SecretResolver{}
	a := &Account{}
	_, err := r.Resolve(context.Background(), a)
	if err == nil || !strings.Contains(err.Error(), "no secret source") {
		t.Errorf("err = %v", err)
	}
}

func TestDefaultRunner_realProcess(t *testing.T) {
	out, err := defaultRunner(context.Background(), []string{"printf", "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello" {
		t.Errorf("out = %q", out)
	}
}

func TestDefaultRunner_nonzeroExit(t *testing.T) {
	_, err := defaultRunner(context.Background(), []string{"sh", "-c", "echo bad >&2; exit 7"})
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "exit 7") {
		t.Errorf("err missing exit code: %v", err)
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("err missing stderr: %v", err)
	}
}

func TestResolve_keyringOtherError(t *testing.T) {
	// Any error other than ErrNotFound is wrapped under "keyring lookup:"
	// and surfaced as-is rather than the actionable "run keyring set" hint.
	r := &SecretResolver{
		Keyring: func(string, string) (string, error) {
			return "", errors.New("dbus disconnected")
		},
	}
	a := &Account{
		Name:   "work",
		Secret: SecretRef{KeyringService: "s", KeyringAccount: "u"},
	}
	_, err := r.Resolve(context.Background(), a)
	if err == nil || !strings.Contains(err.Error(), "keyring lookup") {
		t.Errorf("err = %v, want one wrapped under 'keyring lookup'", err)
	}
	if strings.Contains(err.Error(), "keyring set") {
		t.Errorf("non-NotFound errors should not suggest `keyring set`: %v", err)
	}
}

func TestDefaultRunner_argv0NotFound(t *testing.T) {
	_, err := defaultRunner(context.Background(),
		[]string{"/no/such/binary-9c61c0c3"})
	if err == nil {
		t.Fatal("want error for missing argv[0]")
	}
}
