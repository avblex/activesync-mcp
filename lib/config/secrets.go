package config

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/zalando/go-keyring"
)

// SecretResolver fetches the password for an account.
//
// The default resolver uses the OS keyring (via go-keyring) and exec'd
// commands. Tests inject a fake KeyringGetter / Runner to avoid touching
// real keychains and processes.
type SecretResolver struct {
	Keyring KeyringGetter
	Run     Runner
}

// KeyringGetter abstracts keyring.Get for testing.
type KeyringGetter func(service, user string) (string, error)

// Runner abstracts exec for testing. It must run argv[0] with argv[1:] as
// arguments (no shell), capture stdout, and return it.
type Runner func(ctx context.Context, argv []string) (stdout string, err error)

// DefaultResolver returns a resolver that talks to the real OS keyring and
// shells out via os/exec.
func DefaultResolver() *SecretResolver {
	return &SecretResolver{
		Keyring: keyring.Get,
		Run:     defaultRunner,
	}
}

// Resolve returns the password for the account.
func (r *SecretResolver) Resolve(ctx context.Context, a *Account) (string, error) {
	if a == nil {
		return "", errors.New("nil account")
	}
	switch {
	case a.Secret.KeyringService != "":
		pw, err := r.Keyring(a.Secret.KeyringService, a.Secret.KeyringAccount)
		if err != nil {
			if errors.Is(err, keyring.ErrNotFound) {
				return "", fmt.Errorf("keyring entry not set for service=%q account=%q (run: activesync-mcp setup, then edit account %q)",
					a.Secret.KeyringService, a.Secret.KeyringAccount, a.Name)
			}
			return "", fmt.Errorf("keyring lookup: %w", err)
		}
		if pw == "" {
			return "", fmt.Errorf("keyring entry for %q is empty", a.Name)
		}
		return pw, nil
	case len(a.Secret.Command) > 0:
		out, err := r.Run(ctx, a.Secret.Command)
		if err != nil {
			return "", fmt.Errorf("secret command: %w", err)
		}
		// Trim trailing newline only — internal whitespace may be intentional
		// (e.g. tokens with surrounding spaces would already be wrong, but a
		// single trailing \n is the universal convention for CLI output).
		out = strings.TrimRight(out, "\r\n")
		if out == "" {
			return "", errors.New("secret command produced empty output")
		}
		return out, nil
	default:
		return "", errors.New("no secret source configured")
	}
}

func defaultRunner(ctx context.Context, argv []string) (string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return "", fmt.Errorf("%s: exit %d: %s",
				argv[0], ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}
