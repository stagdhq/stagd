package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"

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
			"with the same rules previews use. Exits non-zero when there's something to fix, so CI can run it.",
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
			return err
		}
	}

	var res *apiv1.ValidateAppResponse

	for round := 0; ; round++ {
		r, err := c.Previews.ValidateApp(ctx, connect.NewRequest(req))
		if err != nil {
			return err
		}

		res = r.Msg

		if len(res.GetRequested()) == 0 {
			break
		}

		if round == maxRounds {
			return errors.New("Stagd kept asking for more files; check the paths in stagd.yaml")
		}

		for _, path := range res.GetRequested() {
			if err := add(path); err != nil {
				return err
			}
		}
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		if err := output.JSON(w, res); err != nil {
			return err
		}
	} else {
		printValidation(w, res)
	}

	if n := len(res.GetProblems()); n > 0 {
		return cli.Exit("", 1)
	}

	return nil
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
