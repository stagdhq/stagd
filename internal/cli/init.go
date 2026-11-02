package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"
)

func initCommand() *cli.Command {
	return &cli.Command{
		Name:      "init",
		Usage:     "write a stagd.yaml to start from, made from the checkout's compose file",
		ArgsUsage: "[dir]",
		Description: "Reads the compose file at the checkout's root (compose.yaml, docker-compose.yaml) as previews\n" +
			"would, and writes a stagd.yaml beside it: the URLs previews get, and the settings most\n" +
			"repositories want next, commented out. A repository's page in the dashboard offers the same.\n" +
			"It leaves a stagd.yaml that's there alone; stagd validate checks that one.",
		Action: initConfig,
	}
}

func initConfig(ctx context.Context, cmd *cli.Command) error {
	root := cmd.Args().First()
	if root == "" {
		root = "."
	}

	for _, name := range configFiles {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			return cli.Exit(fmt.Sprintf("%s is there already: stagd validate checks it", name), 1)
		}
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := appFiles(ctx, c, root)
	if err != nil {
		return err
	}

	if res.GetStarter() == "" {
		return cli.Exit("Stagd offered no stagd.yaml to start from: is this Stagd up to date?", 1)
	}

	path := filepath.Join(root, "stagd.yaml")

	if err := os.WriteFile(path, []byte(res.GetStarter()), 0o644); err != nil {
		return err
	}

	w := cmd.Root().Writer

	fmt.Fprintf(w, "Wrote %s.\n", path)

	if problems := res.GetProblems(); len(problems) > 0 {
		fmt.Fprintln(w, "\nBefore previews start:")

		for _, p := range problems {
			fmt.Fprintf(w, "  - %s\n", p)
		}
	}

	fmt.Fprintln(w, "\nLook it over, then stagd validate checks it as previews would read it.")

	return nil
}

// configFiles are where Stagd looks for stagd.yaml, as rootFiles has them.
var configFiles = []string{"stagd.yaml", ".stagd.yaml"}
