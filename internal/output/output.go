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
	"charm.land/lipgloss/v2/table"
	"golang.org/x/term"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var (
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.BrightBlack).PaddingRight(3)
	cellStyle   = lipgloss.NewStyle().PaddingRight(3)
	// The first column names the row.
	firstStyle = cellStyle.Bold(true)
	ruleStyle  = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
)

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

// Table prints rows under a header. In a terminal it's a styled table; piped,
// it's plain aligned columns with an upper-case header, for grep and awk.
func Table(w io.Writer, header []string, rows [][]string) error {
	if isTerminal(w) {
		return styledTable(w, header, rows)
	}

	return plainTable(w, header, rows)
}

func styledTable(w io.Writer, header []string, rows [][]string) error {
	if len(rows) == 0 {
		_, err := lipgloss.Fprintln(w, ruleStyle.Render("(none)"))

		return err
	}

	titles := make([]string, len(header))

	for i, h := range header {
		if h != "" {
			titles[i] = strings.ToUpper(h[:1]) + h[1:]
		}
	}

	t := table.New().
		Headers(titles...).
		Rows(rows...).
		Border(lipgloss.NormalBorder()).
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		BorderColumn(false).
		BorderHeader(true).
		BorderStyle(ruleStyle).
		StyleFunc(func(row, col int) lipgloss.Style {
			switch {
			case row == table.HeaderRow:
				return headerStyle
			case col == 0:
				return firstStyle
			default:
				return cellStyle
			}
		})

	_, err := lipgloss.Fprintln(w, t)

	return err
}

func plainTable(w io.Writer, header []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.ToUpper(strings.Join(header, "\t")))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	return tw.Flush()
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
