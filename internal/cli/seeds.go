package cli

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/output"
)

func seedsCommand() *cli.Command {
	fromFlag := &cli.StringFlag{Name: "from", Usage: "the storage integration it's kept in (see stagd integrations list)"}
	pathFlag := &cli.StringFlag{Name: "path", Usage: "a file in it; a pattern such as dumps/*.dump for the newest it matches; or a folder ending in / for its newest file"}

	return &cli.Command{
		Name:  "seeds",
		Usage: "the data previews' databases start with: database dumps kept in your Azure or S3 storage",
		Description: "A seed is a dump in one of the org's storage integrations, named for stagd.yaml:\n\n" +
			"   services:\n" +
			"     postgres: { seed: app-anonymized }\n\n" +
			"A pattern (dumps/*.dump, * and ? not crossing a /) is the newest file it matches at each deploy,\n" +
			"so a nightly dump is picked up as it lands; a folder (a path ending in /) is its newest file,\n" +
			"leaving out files without an extension such as a latest pointer. Stagd streams the file to the preview's database through a short-lived link; the\n" +
			"storage's credentials never reach the preview.",
		Commands: []*cli.Command{
			{
				Name:   "list",
				Usage:  "list the org's seeds and the file each finds",
				Action: seedList,
			},
			{
				Name:      "add",
				Usage:     "name a dump in a storage integration as a seed; Stagd looks for the file first",
				ArgsUsage: "<name>",
				Flags:     []cli.Flag{fromFlag, pathFlag},
				Action:    seedAdd,
			},
			{
				Name:      "change",
				Usage:     "point a seed at another path or integration",
				ArgsUsage: "<name>",
				Flags:     []cli.Flag{fromFlag, pathFlag},
				Action:    seedChange,
			},
			{
				Name:      "find",
				Usage:     "look up the file a path finds, without saving anything",
				ArgsUsage: "<path>",
				Flags:     []cli.Flag{fromFlag},
				Action:    seedFind,
			},
			{
				Name:      "remove",
				Usage:     "forget a seed; its file stays in the storage",
				ArgsUsage: "<name>",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "don't ask for confirmation"},
				},
				Action: seedRemove,
			},
		},
	}
}

func seedList(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Seeds.ListSeeds(ctx, connect.NewRequest(&apiv1.ListSeedsRequest{}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	seeds := res.Msg.GetSeeds()

	if len(seeds) == 0 {
		fmt.Fprintln(w, "No seeds yet: add one with stagd seeds add <name> --from <integration> --path <path>.")

		return nil
	}

	rows := make([][]string, 0, len(seeds))

	for _, s := range seeds {
		file := s.GetProblem()
		if f := s.GetFile(); f.GetName() != "" {
			file = fileLine(f)
		}

		rows = append(rows, []string{s.GetName(), s.GetIntegrationName(), s.GetPath(), file})
	}

	return output.Table(w, []string{"name", "from", "path", "file"}, rows)
}

func seedAdd(ctx context.Context, cmd *cli.Command) error {
	name := cmd.Args().First()
	if name == "" || cmd.String("from") == "" || cmd.String("path") == "" {
		return cli.Exit("usage: stagd seeds add <name> --from <integration> --path <file, or folder/>", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Seeds.CreateSeed(ctx, connect.NewRequest(&apiv1.CreateSeedRequest{Name: name, Integration: cmd.String("from"), Path: cmd.String("path")}))
	if err != nil {
		return err
	}

	return printSeed(cmd, "Added", res.Msg.GetSeed(), res.Msg)
}

func seedChange(ctx context.Context, cmd *cli.Command) error {
	name := cmd.Args().First()
	if name == "" || (cmd.String("from") == "" && cmd.String("path") == "") {
		return cli.Exit("usage: stagd seeds change <name> [--from <integration>] [--path <file, or folder/>]", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Seeds.UpdateSeed(ctx, connect.NewRequest(&apiv1.UpdateSeedRequest{Seed: name, Integration: cmd.String("from"), Path: cmd.String("path")}))
	if err != nil {
		return err
	}

	return printSeed(cmd, "Changed", res.Msg.GetSeed(), res.Msg)
}

// printSeed says what a seed now is and the file it finds, or the response as JSON.
func printSeed(cmd *cli.Command, did string, s *apiv1.Seed, msg proto.Message) error {
	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, msg)
	}

	fmt.Fprintf(w, "%s %s: %s in %s\n", did, s.GetName(), s.GetPath(), s.GetIntegrationName())

	if f := s.GetFile(); f.GetName() != "" {
		fmt.Fprintf(w, "It finds %s\n", fileLine(f))
	}

	_, err := fmt.Fprintf(w, "Use it in stagd.yaml: seed: %s\n", s.GetName())

	return err
}

func seedFind(ctx context.Context, cmd *cli.Command) error {
	path := cmd.Args().First()
	if path == "" || cmd.String("from") == "" {
		return cli.Exit("usage: stagd seeds find <file, or folder/> --from <integration>", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Seeds.FindSeedFile(ctx, connect.NewRequest(&apiv1.FindSeedFileRequest{Integration: cmd.String("from"), Path: path}))
	if err != nil {
		return err
	}

	if cmd.Bool("json") {
		return output.JSON(cmd.Root().Writer, res.Msg)
	}

	_, err = fmt.Fprintln(cmd.Root().Writer, fileLine(res.Msg.GetFile()))

	return err
}

func seedRemove(ctx context.Context, cmd *cli.Command) error {
	name := cmd.Args().First()
	if name == "" {
		return cli.Exit("usage: stagd seeds remove <name>", 2)
	}

	if !cmd.Bool("yes") {
		if !interactive() {
			return cli.Exit("pass --yes to remove without a prompt", 2)
		}

		if ok, err := confirm(cmd, fmt.Sprintf("Remove the seed %s?", name), "Previews whose stagd.yaml names it stop deploying; its file stays in the storage."); err != nil || !ok {
			return err
		}
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	if _, err := c.Seeds.DeleteSeed(ctx, connect.NewRequest(&apiv1.DeleteSeedRequest{Seed: name})); err != nil {
		return err
	}

	_, err = fmt.Fprintf(cmd.Root().Writer, "Removed %s\n", name)

	return err
}

// fileLine is a seed's file on one line: "platform/2026-10-28.dump, 1.2 GB, 2026-10-28 03:00".
func fileLine(f *apiv1.SeedFile) string {
	line := f.GetName() + ", " + size(f.GetSizeBytes())

	if t := f.GetModifyTime(); t != nil {
		line += ", " + t.AsTime().Local().Format(time.DateTime[:16])
	}

	return line
}

// size is a number of bytes in the largest unit that keeps it above 1, in tenths below 10.
func size(n int64) string {
	units := []string{"bytes", "kB", "MB", "GB", "TB"}
	v := float64(n)
	i := 0

	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}

	if i == 0 {
		return fmt.Sprintf("%d bytes", n)
	}

	if v < 10 {
		return fmt.Sprintf("%.1f %s", v, units[i])
	}

	return fmt.Sprintf("%.0f %s", v, units[i])
}
