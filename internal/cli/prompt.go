package cli

import (
	"bufio"
	"fmt"
	"strings"

	"charm.land/huh/v2"
	"github.com/urfave/cli/v3"
)

// Prompts stay plain: forms in huh's base theme, without colour, and
// yes-or-no questions as one line.

// newForm is a huh form in the plain theme.
func newForm(groups ...*huh.Group) *huh.Form {
	return huh.NewForm(groups...).WithTheme(huh.ThemeFunc(huh.ThemeBase))
}

// confirm asks a yes-or-no question on stderr, what it means above it:
// "Delete x? [y/N]". Anything but y or yes is no.
func confirm(cmd *cli.Command, question, detail string) (bool, error) {
	w := cmd.Root().ErrWriter

	if detail != "" {
		fmt.Fprintln(w, detail)
	}

	fmt.Fprintf(w, "%s [y/N] ", question)

	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(w)

		return false, nil
	}

	answer := strings.ToLower(strings.TrimSpace(line))

	return answer == "y" || answer == "yes", nil
}
