package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/output"
)

func previewTryCommand() *cli.Command {
	return &cli.Command{
		Name:      "try",
		Usage:     "have the org's model do one of What to test's things to try on the live preview",
		ArgsUsage: "<id> <n>",
		Description: "Runs the guide's nth thing to try (from 1, as stagd preview get lists them, and the QA tab)\n" +
			"in a browser on the live preview, signed in as a tester from stagd.yaml: the first, or --as.\n" +
			"What to test's model in Settings → AI does it, one action at a time, and says whether the\n" +
			"app did what the try says. It exits non-zero when the try failed or it couldn't tell.\n\n" +
			"   stagd preview try <id> 2 --as member --screenshot try-2.jpg",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "as", Usage: "the tester to sign in as, by name"},
			&cli.StringFlag{Name: "screenshot", Usage: "a file for the page where it ended (JPEG)"},
		},
		Action: previewTry,
	}
}

func previewTry(ctx context.Context, cmd *cli.Command) error {
	id := cmd.Args().Get(0)
	n, err := strconv.Atoi(cmd.Args().Get(1))

	if id == "" || err != nil || n < 1 {
		return cli.Exit("usage: stagd preview try <id> <n> [--as tester]", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Previews.RunTry(ctx, connect.NewRequest(&apiv1.RunTryRequest{Id: id, Try: int32(n), Tester: cmd.String("as")}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer
	run := res.Msg.GetRun()

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	who := "signed out"
	if run.GetTester() != "" {
		who = "as " + run.GetTester()
	}

	fmt.Fprintf(w, "%d. %s\n   %s\n\n", run.GetTry(), run.GetText(), who)

	for i, s := range run.GetSteps() {
		line := fmt.Sprintf("   %2d. %s", i+1, s.GetAction())
		if s.GetError() != "" {
			line += "  ✗ " + s.GetError()
		}

		fmt.Fprintln(w, line)
	}

	mark := map[apiv1.TryVerdict]string{apiv1.TryVerdict_TRY_VERDICT_PASSED: "✓ Passed", apiv1.TryVerdict_TRY_VERDICT_FAILED: "✗ Failed"}[run.GetVerdict()]
	if mark == "" {
		mark = "? Couldn't tell"
	}

	fmt.Fprintf(w, "\n%s: %s\n", mark, run.GetReason())
	fmt.Fprintf(w, "   %s, %s, %d tokens in, %d out\n", run.GetModelName(), run.GetDuration().AsDuration().Round(100_000_000), run.GetInputTokens(), run.GetOutputTokens())

	if path := cmd.String("screenshot"); path != "" && len(run.GetScreenshot()) > 0 {
		if err := os.WriteFile(path, run.GetScreenshot(), 0o644); err != nil {
			return err
		}

		fmt.Fprintf(w, "   the page where it ended: %s\n", path)
	}

	if run.GetVerdict() != apiv1.TryVerdict_TRY_VERDICT_PASSED {
		return cli.Exit("", 1)
	}

	return nil
}
