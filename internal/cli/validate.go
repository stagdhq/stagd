package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"

	"github.com/stagdhq/stagd/internal/client/api"
	"github.com/stagdhq/stagd/internal/configyaml"
	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/output"
)

// rootFiles are what the server looks for first; anything else (app.compose
// in stagd.yaml) it asks for by name.
var rootFiles = []string{"stagd.yaml", ".stagd.yaml", "compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// maxRounds bounds the back-and-forth when the server asks for more files.
const maxRounds = 4

func validateCommand() *cli.Command {
	return &cli.Command{
		Name:      "validate",
		Usage:     "check stagd.yaml and the compose file as previews would read them",
		ArgsUsage: "[repository directory]",
		Description: "Reads the files from your checkout (the current directory by default) and has Stagd check them\n" +
			"with the same rules previews use. Exits non-zero when there's something to fix, so CI can run it.\n" +
			"With --explain, also prints stagd.yaml as Stagd reads it: shorthands written out, defaults filled in.",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "explain", Usage: "also print stagd.yaml as Stagd reads it, every default filled in"},
		},
		Action: validate,
	}
}

// readRepoFile reads a root-relative path, refusing anything outside the repository.
func readRepoFile(root, path string) (string, bool, error) {
	clean := filepath.Clean(path)

	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("%s is outside the repository", path)
	}

	data, err := os.ReadFile(filepath.Join(root, clean))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}

	if err != nil {
		return "", false, err
	}

	return string(data), true, nil
}

func validate(ctx context.Context, cmd *cli.Command) error {
	root := cmd.Args().First()
	if root == "" {
		root = "."
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := appFiles(ctx, c, root)
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		if err := output.JSON(w, res); err != nil {
			return err
		}
	} else {
		printValidation(w, res)

		if cmd.Bool("explain") {
			if err := explain(w, res); err != nil {
				return err
			}
		}
	}

	if n := len(res.GetProblems()); n > 0 {
		return cli.Exit("", 1)
	}

	return nil
}

// appFiles sends the checkout's stagd.yaml and compose files to Stagd, and
// those it asks for next, until it answers how previews would read them.
func appFiles(ctx context.Context, c *api.Clients, root string) (*apiv1.ValidateAppResponse, error) {
	req := &apiv1.ValidateAppRequest{Files: map[string]string{}}
	add := func(path string) error {
		text, ok, err := readRepoFile(root, path)
		if err != nil {
			return err
		}

		if ok {
			req.Files[path] = text
		} else {
			req.Absent = append(req.Absent, path)
		}

		return nil
	}

	for _, path := range rootFiles {
		if err := add(path); err != nil {
			return nil, err
		}
	}

	for round := 0; ; round++ {
		r, err := c.Previews.ValidateApp(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, err
		}

		if len(r.Msg.GetRequested()) == 0 {
			return r.Msg, nil
		}

		if round == maxRounds {
			return nil, errors.New("Stagd kept asking for more files; check the paths in stagd.yaml")
		}

		for _, path := range r.Msg.GetRequested() {
			if err := add(path); err != nil {
				return nil, err
			}
		}
	}
}

func printValidation(w interface{ Write([]byte) (int, error) }, res *apiv1.ValidateAppResponse) {
	if files := res.GetFiles(); len(files) > 0 {
		fmt.Fprintf(w, "Read %s\n", strings.Join(files, " and "))
	}

	if problems := res.GetProblems(); len(problems) > 0 {
		fmt.Fprintf(w, "\n%d to fix:\n", len(problems))

		for _, p := range problems {
			fmt.Fprintf(w, "  ✗ %s\n", p)
		}
	} else {
		fmt.Fprintln(w, "\nPreviews would run:")

		for _, c := range res.GetComponents() {
			from := "image " + c.GetImage()
			if c.GetBuildContext() != "" {
				from = "built from " + c.GetBuildContext()
			}

			fmt.Fprintf(w, "  %-12s %s\n", c.GetName(), from)
		}

		fmt.Fprintln(w, "\nWith URLs for:")

		for _, i := range res.GetIngresses() {
			marker := " "
			if i.GetPrimary() {
				marker = "*"
			}

			fmt.Fprintf(w, "  %s %-10s %s:%d\n", marker, i.GetName(), i.GetComponent(), i.GetPort())
		}
	}

	if notes := res.GetNotes(); len(notes) > 0 {
		fmt.Fprintln(w, "\nLeft out on purpose:")

		for _, n := range notes {
			fmt.Fprintf(w, "  - %s\n", n)
		}
	}
}

// explain prints stagd.yaml as previews read it, or why it can't.
func explain(w io.Writer, res *apiv1.ValidateAppResponse) error {
	read := false

	for _, f := range res.GetFiles() {
		if slices.Contains(configFiles, f) {
			read = true
		}
	}

	if !read {
		fmt.Fprintln(w, "\nNo stagd.yaml: previews go by the compose file alone. stagd init writes one to start from.")

		return nil
	}

	if len(res.GetProblems()) > 0 || res.GetConfig() == nil {
		fmt.Fprintln(w, "\nstagd.yaml as Stagd reads it shows once the problems above are fixed.")

		return nil
	}

	out, err := configyaml.Marshal(res.GetConfig())
	if err != nil {
		return err
	}

	fmt.Fprint(w, "\nstagd.yaml as Stagd reads it, every default filled in:\n\n")
	_, err = w.Write(out)

	return err
}
