package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Namespace's workspace tokens, made with its own CLI (nsc), signed in to
// the workspace previews run in: `stagd compute connect namespace` and
// `stagd compute update <name> --new-token` make one with exactly the
// grants Stagd uses, hand it to Stagd without it being shown or typed, and
// revoke the ones they made before. The grants are the platform's too
// (its namespaceGrants), which its check of a token looks for.

// namespaceGrants are nsc's --grant values.
var namespaceGrants = []string{
	`{"resource_type":"instance","resource_id":"*","actions":["create","get","list","wait","exec","refresh","suspend","resume","destroy"]}`,
	`{"resource_type":"instance/ingress","resource_id":"*","actions":["register","list"]}`,
	`{"resource_type":"ingress","resource_id":"*","actions":["access"]}`,
}

// runNsc runs nsc and answers what it printed; replaced in tests.
var runNsc = func(ctx context.Context, args ...string) ([]byte, error) {
	var stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, "nsc", args...)
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		said := strings.TrimSpace(stderr.String())

		if said == "" {
			said = err.Error()
		}

		return nil, fmt.Errorf("nsc %s: %s", args[0]+" "+args[1], said)
	}

	return out, nil
}

// hasNsc says whether nsc is installed; replaced in tests.
var hasNsc = func() bool {
	_, err := exec.LookPath("nsc")

	return err == nil
}

// tokenPrefix names the tokens the CLI makes for a connection: stagd-<connection>-<when>.
func tokenPrefix(connection string) string {
	return "stagd-" + connection + "-"
}

// newNamespaceToken makes a workspace token for a connection, valid for a year: its value, and its name.
func newNamespaceToken(ctx context.Context, connection string) (token, name string, err error) {
	name = tokenPrefix(connection) + time.Now().UTC().Format("20060102-150405")

	args := []string{"token", "create", "--name", name, "--expires_in", "8760h", "--description", "Stagd's compute connection " + connection}

	for _, g := range namespaceGrants {
		args = append(args, "--grant", g)
	}

	out, err := runNsc(ctx, append(args, "--output", "token")...)
	if err != nil {
		return "", "", err
	}

	token = strings.TrimSpace(string(out))

	if token == "" {
		return "", "", fmt.Errorf("nsc made a token but printed none")
	}

	return token, name, nil
}

// nscToken is a token as `nsc token list -o json` lists it.
type nscToken struct {
	ID    string `json:"token_id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

// revokeOlderTokens revokes the tokens the CLI made before for a connection, all but keep: their names.
func revokeOlderTokens(ctx context.Context, connection, keep string) ([]string, error) {
	out, err := runNsc(ctx, "token", "list", "--output", "json")
	if err != nil {
		return nil, err
	}

	var tokens []nscToken

	if err := json.Unmarshal(out, &tokens); err != nil {
		return nil, fmt.Errorf("reading nsc's list of tokens: %w", err)
	}

	var revoked []string

	for _, t := range tokens {
		if !strings.HasPrefix(t.Name, tokenPrefix(connection)) || t.Name == keep || strings.EqualFold(t.State, "revoked") {
			continue
		}

		if _, err := runNsc(ctx, "token", "revoke", "--token_id", t.ID); err != nil {
			return revoked, err
		}

		revoked = append(revoked, t.Name)
	}

	return revoked, nil
}

// revokeToken revokes a token the CLI just made, by name: when Stagd didn't take it.
func revokeToken(ctx context.Context, name string) {
	out, err := runNsc(ctx, "token", "list", "--output", "json")
	if err != nil {
		return
	}

	var tokens []nscToken

	if json.Unmarshal(out, &tokens) != nil {
		return
	}

	for _, t := range tokens {
		if t.Name == name {
			_, _ = runNsc(ctx, "token", "revoke", "--token_id", t.ID)
		}
	}
}
