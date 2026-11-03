package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/output"
)

func previewTestersCommand() *cli.Command {
	return &cli.Command{
		Name:      "testers",
		Usage:     "the users stagd.yaml names for signing in to a preview, and whether signing in works",
		ArgsUsage: "<id> [tester...]",
		Description: "Lists the preview's testers (stagd.yaml's testers) and how each signs in. With --check,\n" +
			"Stagd signs in to the live preview as each one, or those named, the way sign_in says, and\n" +
			"prints how each step went; it exits non-zero when one doesn't work. --screenshots keeps the\n" +
			"page where a sign-in stopped, as <tester>.jpg in that directory:\n\n" +
			"   stagd preview testers <id> --check\n" +
			"   stagd preview testers <id> --check admin --screenshots .",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "check", Usage: "sign in as each tester, or those named, and say how it went"},
			&cli.StringFlag{Name: "screenshots", Usage: "with --check: a directory for the page where a sign-in stopped"},
		},
		Action: previewTesters,
	}
}

func previewTesters(ctx context.Context, cmd *cli.Command) error {
	id := cmd.Args().Get(0)
	names := cmd.Args().Slice()

	if id == "" {
		return cli.Exit("usage: stagd preview testers <id> [--check [tester...]]", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if !cmd.Bool("check") {
		res, err := c.Previews.ListTesters(ctx, connect.NewRequest(&apiv1.ListTestersRequest{Id: id}))
		if err != nil {
			return err
		}

		if cmd.Bool("json") {
			return output.JSON(w, res.Msg)
		}

		if len(res.Msg.GetTesters()) == 0 {
			_, err = fmt.Fprintln(w, "This preview's stagd.yaml names no testers: add testers and sign_in, and deploy again.")

			return err
		}

		rows := make([][]string, 0, len(res.Msg.GetTesters()))

		for _, t := range res.Msg.GetTesters() {
			who := t.GetEmail()
			if who == "" {
				who = t.GetUsername()
			}

			rows = append(rows, []string{t.GetName(), who, t.GetSignIn(), t.GetHow(), t.GetNote()})
		}

		return output.Table(w, []string{"tester", "who", "sign-in", "how", "note"}, rows)
	}

	res, err := c.Previews.CheckTesters(ctx, connect.NewRequest(&apiv1.CheckTestersRequest{Id: id, Names: names[1:]}))
	if err != nil {
		return err
	}

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	failed := 0

	for _, check := range res.Msg.GetChecks() {
		mark := "✓"
		if !check.GetOk() {
			mark = "✗"
			failed++
		}

		fmt.Fprintf(w, "%s %s\n", mark, check.GetName())

		for _, s := range check.GetSteps() {
			step := "✓"
			if !s.GetOk() {
				step = "✗"
			}

			line := fmt.Sprintf("    %s %s  %s", step, s.GetText(), s.GetDuration().AsDuration().Round(10_000_000))
			if s.GetError() != "" {
				line += "  " + s.GetError()
			}

			fmt.Fprintln(w, line)
		}

		if !check.GetOk() && len(check.GetSteps()) == 0 && check.GetError() != "" {
			fmt.Fprintf(w, "    %s\n", check.GetError())
		}

		if dir := cmd.String("screenshots"); dir != "" && len(check.GetScreenshot()) > 0 {
			path := filepath.Join(dir, check.GetName()+".jpg")

			if err := os.WriteFile(path, check.GetScreenshot(), 0o644); err != nil {
				return err
			}

			fmt.Fprintf(w, "    the page where it stopped: %s\n", path)
		}
	}

	if failed > 0 {
		return cli.Exit(fmt.Sprintf("%d of %d sign-ins didn't work", failed, len(res.Msg.GetChecks())), 1)
	}

	return nil
}
