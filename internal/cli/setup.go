package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/output"
)

// setupCommand prints the org's way to its first live preview: the same steps as Home in the dashboard and the MCP
// server's onboarding_status, as the server works them out (stagdhq/platform#11).
func setupCommand() *cli.Command {
	return &cli.Command{
		Name:  "setup",
		Usage: "the steps to your org's first live preview, and the next one",
		Description: "The same steps as Home in the dashboard: connect GitHub, choose repositories, connect compute,\n" +
			"set the secrets the app needs, and a first preview. Admin steps say who the admins are.\n\n" +
			"With a coding agent, `stagd mcp install` adds Stagd's tools to it; its setup prompt walks through the rest.",
		Action: setupAction,
	}
}

func setupAction(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Orgs.GetOnboarding(ctx, connect.NewRequest(&apiv1.GetOnboardingRequest{}))
	if err != nil {
		return err
	}

	o := res.Msg.GetOnboarding()
	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, o)
	}

	printOnboarding(w, o)

	return nil
}

// printOnboarding writes the steps as a checklist: ✓ done, ● the next one with its detail and link, ○ the rest.
func printOnboarding(w io.Writer, o *apiv1.Onboarding) {
	if o.GetCompleteTime() != nil {
		fmt.Fprintf(w, "Set up: the first preview went live on %s.\n\n", o.GetCompleteTime().AsTime().Local().Format("2 January 2006"))
	} else {
		fmt.Fprintf(w, "Your first preview: %d of %d done\n\n", o.GetDone(), o.GetRequired())
	}

	width := 0

	for _, s := range o.GetSteps() {
		width = max(width, len(s.GetTitle()))
	}

	admins := make([]string, 0, len(o.GetAdmins()))

	for _, a := range o.GetAdmins() {
		admins = append(admins, firstNonEmpty(a.GetName(), a.GetEmail()))
	}

	optionalShown := false

	for _, s := range o.GetSteps() {
		if s.GetOptional() && !optionalShown {
			fmt.Fprintln(w, "\nOptional")

			optionalShown = true
		}

		mark := "○"

		switch {
		case s.GetDone():
			mark = "✓"
		case s.GetId() == o.GetNext():
			mark = "●"
		}

		line := fmt.Sprintf("%s %-*s", mark, width, s.GetTitle())

		// Done steps say what was set up; the next one says what to do, and where.
		if s.GetDone() && s.GetDetail() != "" {
			line += "  " + s.GetDetail()
		}

		fmt.Fprintln(w, strings.TrimRight(line, " "))

		if s.GetDone() || (s.GetId() != o.GetNext() && !s.GetOptional()) {
			continue
		}

		indent := strings.Repeat(" ", 2)

		if s.GetDetail() != "" {
			fmt.Fprintln(w, indent+s.GetDetail())
		}

		if s.GetAdminOnly() && len(admins) > 0 {
			fmt.Fprintf(w, "%sAn admin's step: %s.\n", indent, strings.Join(admins, ", "))
		}

		if s.GetUrl() != "" {
			fmt.Fprintln(w, indent+s.GetUrl())
		}
	}
}

// mcpInstallCommand adds Stagd's MCP server to the coding agents found here: Claude Code (through its own CLI) and
// Cursor (its mcp.json), leaving their other servers as they are.
func mcpInstallCommand() *cli.Command {
	return &cli.Command{
		Name:  "install",
		Usage: "add Stagd's tools to Claude Code and Cursor, for every project",
		Description: "Claude Code: runs `claude mcp add --scope user stagd -- stagd mcp`.\n" +
			"Cursor: adds stagd to ~/.cursor/mcp.json, keeping its other servers.\n" +
			"Then ask your agent to set up Stagd, or in Claude Code run /mcp__stagd__setup.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "client", Usage: "claude, cursor or all (the ones found here)", Value: "all"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			client := strings.ToLower(cmd.String("client"))
			if client != "all" && client != "claude" && client != "cursor" {
				return cli.Exit("--client: claude, cursor or all", 2)
			}

			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}

			w := cmd.Root().Writer
			command := stagdCommand()
			added := 0

			if client == "all" || client == "claude" {
				switch ok, err := installClaude(ctx, command); {
				case err != nil:
					fmt.Fprintf(w, "Claude Code: %v\n", err)
				case ok:
					fmt.Fprintln(w, "Claude Code: added stagd, for every project.")

					added++
				case client == "claude":
					fmt.Fprintln(w, "Claude Code: the claude command isn't here; install Claude Code first.")
				}
			}

			if client == "all" || client == "cursor" {
				dir := filepath.Join(home, ".cursor")

				if _, statErr := os.Stat(dir); statErr == nil || client == "cursor" {
					if err := installCursor(filepath.Join(dir, "mcp.json"), command); err != nil {
						fmt.Fprintf(w, "Cursor: %v\n", err)
					} else {
						fmt.Fprintf(w, "Cursor: added stagd to %s.\n", filepath.Join(dir, "mcp.json"))

						added++
					}
				}
			}

			if added == 0 {
				fmt.Fprintln(w, "No coding agent found to add it to. Elsewhere, start it as: stagd mcp")

				return nil
			}

			fmt.Fprintln(w, "\nNext: ask your agent to set up Stagd for this repository (in Claude Code: /mcp__stagd__setup).")

			return nil
		},
	}
}

// stagdCommand is how an agent should start this CLI: "stagd" when it's on the PATH, else this binary's own path.
func stagdCommand() string {
	if path, err := exec.LookPath("stagd"); err == nil && path != "" {
		return "stagd"
	}

	if self, err := os.Executable(); err == nil {
		return self
	}

	return "stagd"
}

// runClaude runs Claude Code's CLI; tests replace it.
var runClaude = func(ctx context.Context, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, errNoClaude
	}

	return exec.CommandContext(ctx, "claude", args...).CombinedOutput()
}

var errNoClaude = errors.New("claude isn't installed")

// installClaude adds stagd for every project; false when Claude Code isn't installed. One already there is fine.
func installClaude(ctx context.Context, command string) (bool, error) {
	out, err := runClaude(ctx, "mcp", "add", "--scope", "user", "stagd", "--", command, "mcp")

	switch {
	case errors.Is(err, errNoClaude):
		return false, nil
	case err != nil && strings.Contains(string(out), "already exists"):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("claude mcp add failed: %s", strings.TrimSpace(string(out)))
	}

	return true, nil
}

// installCursor adds stagd to an mcp.json, made if it isn't there, keeping everything else in it.
func installCursor(path, command string) error {
	config := map[string]any{}

	raw, err := os.ReadFile(path)

	switch {
	case err == nil && len(strings.TrimSpace(string(raw))) > 0:
		if err := json.Unmarshal(raw, &config); err != nil {
			return fmt.Errorf("%s isn't JSON, so it's left as it is: %w", path, err)
		}
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return err
	}

	servers, _ := config["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}

	servers["stagd"] = map[string]any{"command": command, "args": []string{"mcp"}}
	config["mcpServers"] = servers

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	return os.WriteFile(path, append(out, '\n'), 0o644)
}
