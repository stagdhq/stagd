// Package output prints results as human tables or, with --json, as JSON
// for scripts.
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"charm.land/lipgloss/v2"
	"golang.org/x/term"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// headerStyle dims a table's header in a terminal; nothing else is styled.
var headerStyle = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)

// isTerminal reports whether w is a terminal (not a pipe, file or test buffer).
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)

	return ok && term.IsTerminal(int(f.Fd()))
}

// TerminalWidth is w's width in columns, or 0 when w isn't a terminal.
func TerminalWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return 0
	}

	// Some terminals (and pseudo-terminals) don't report a size.
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil || width <= 0 {
		return 80
	}

	return width
}

// Table prints rows under a header as aligned columns, upper-case header
// first, for people and for grep and awk alike; in a terminal the header is
// dimmed, nothing more.
func Table(w io.Writer, header []string, rows [][]string) error {
	var buf bytes.Buffer

	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.ToUpper(strings.Join(header, "\t")))

	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}

	if err := tw.Flush(); err != nil {
		return err
	}

	out := buf.String()

	// Dimmed after aligning: escape codes would throw the widths off.
	if isTerminal(w) {
		head, rest, _ := strings.Cut(out, "\n")
		out = headerStyle.Render(head) + "\n" + rest
	}

	_, err := io.WriteString(w, out)

	return err
}

// JSON prints a message as protobuf JSON (the same field names the API uses
// on the wire), indented the same way every time so scripts can diff it.
// protojson's own output has deliberately unstable whitespace.
func JSON(w io.Writer, m proto.Message) error {
	b, err := protojson.Marshal(m)
	if err != nil {
		return err
	}

	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		return err
	}

	out.WriteByte('\n')
	_, err = out.WriteTo(w)

	return err
}

// JSONValue prints any value as indented JSON.
func JSONValue(w io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}

	_, err = fmt.Fprintln(w, string(b))

	return err
}
