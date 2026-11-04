package cli

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/stagdhq/stagd/internal/client/api"
	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/output"
)

// How often --follow asks for newer lines.
var followEvery = 2 * time.Second

func previewLogsCommand() *cli.Command {
	return &cli.Command{
		Name:      "logs",
		Usage:     "read what a preview's services wrote to stdout and stderr",
		ArgsUsage: "<id>",
		Description: "Prints the latest lines, oldest first, each with its time (UTC), service and level and the\n" +
			"line as the service wrote it, so they can be piped, grepped or pasted:\n\n" +
			"   stagd preview logs <id> --service stagd-postgres --since 10m\n\n" +
			"--follow keeps printing new lines until stopped. --build prints the end of each service's\n" +
			"build output instead: what to read when a build failed.",
		Flags: []cli.Flag{
			&cli.StringSliceFlag{Name: "service", Aliases: []string{"s"}, Usage: "only this service's lines (repeatable)"},
			&cli.StringFlag{Name: "since", Usage: "only lines from this long ago (10m, 2h) or this time (2026-11-02T13:10:00Z) on"},
			&cli.IntFlag{Name: "lines", Aliases: []string{"n"}, Usage: "the latest this many lines at most", Value: 500},
			&cli.BoolFlag{Name: "errors", Usage: "only lines logged as errors"},
			&cli.BoolFlag{Name: "follow", Aliases: []string{"f"}, Usage: "keep printing new lines as they're written"},
			&cli.BoolFlag{Name: "build", Usage: "the build's output, not the running services' logs"},
		},
		Action: previewLogs,
	}
}

func previewLogs(ctx context.Context, cmd *cli.Command) error {
	id := cmd.Args().Get(0)

	if id == "" {
		return cli.Exit("usage: stagd preview logs <id>", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	if cmd.Bool("build") {
		return buildLogs(ctx, cmd, c, id)
	}

	since, err := logsSince(cmd.String("since"), time.Now())
	if err != nil {
		return cli.Exit(err.Error(), 2)
	}

	w := cmd.Root().Writer
	errorsOnly := cmd.Bool("errors")
	limit := cmd.Int("lines")

	// Errors are picked from more lines, so --lines counts what's printed.
	asked := limit

	if errorsOnly {
		asked = max(limit, 2000)
	}

	res, err := c.Previews.ListLogs(ctx, connect.NewRequest(&apiv1.ListLogsRequest{PreviewId: id, Services: cmd.StringSlice("service"), Since: since, Limit: int32(asked)}))
	if err != nil {
		return err
	}

	lines := pickLines(res.Msg.GetLines(), errorsOnly)

	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}

	if cmd.Bool("json") && !cmd.Bool("follow") {
		return output.JSON(w, &apiv1.ListLogsResponse{Lines: lines, Truncated: res.Msg.GetTruncated()})
	}

	if res.Msg.GetTruncated() {
		fmt.Fprintf(cmd.Root().ErrWriter, "Older lines left out: the latest %d (--lines for more, --since for a time).\n", limit)
	}

	if len(lines) == 0 && !cmd.Bool("follow") {
		fmt.Fprintln(cmd.Root().ErrWriter, "No lines.")

		return nil
	}

	if err := printLines(w, lines, cmd.Bool("json"), output.Color(w)); err != nil {
		return err
	}

	if !cmd.Bool("follow") {
		return nil
	}

	return followLogs(ctx, cmd, c, id, res.Msg.GetLines())
}

// followLogs asks for the lines after the last one read, every couple of
// seconds, until stopped. Asking since the last line's time gets the lines
// at that time again: the first time, they were printed or left out by
// --lines; after that, those already printed are known.
func followLogs(ctx context.Context, cmd *cli.Command, c *api.Clients, id string, read []*apiv1.LogLine) error {
	w := cmd.Root().Writer
	since := timestamppb.Now()
	first := true
	var seen map[string]bool

	if len(read) > 0 {
		since = read[len(read)-1].GetTime()
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(followEvery):
		}

		res, err := c.Previews.ListLogs(ctx, connect.NewRequest(&apiv1.ListLogsRequest{PreviewId: id, Services: cmd.StringSlice("service"), Since: since, Limit: 1000}))
		if ctx.Err() != nil {
			return nil
		}

		if err != nil {
			return err
		}

		var fresh []*apiv1.LogLine

		for _, l := range res.Msg.GetLines() {
			again := l.GetTime().AsTime().Equal(since.AsTime()) && (first || seen[lineKey(l)])

			if !again {
				fresh = append(fresh, l)
			}
		}

		first = false

		if lines := res.Msg.GetLines(); len(lines) > 0 {
			since = lines[len(lines)-1].GetTime()
			seen = map[string]bool{}

			for _, l := range lines {
				if l.GetTime().AsTime().Equal(since.AsTime()) {
					seen[lineKey(l)] = true
				}
			}
		}

		if err := printLines(w, pickLines(fresh, cmd.Bool("errors")), cmd.Bool("json"), output.Color(w)); err != nil {
			return err
		}
	}
}

func buildLogs(ctx context.Context, cmd *cli.Command, c *api.Clients, id string) error {
	res, err := c.Previews.ListBuildLogs(ctx, connect.NewRequest(&apiv1.ListBuildLogsRequest{PreviewId: id}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer
	wanted := cmd.StringSlice("service")
	var builds []*apiv1.BuildLog

	for _, b := range res.Msg.GetBuilds() {
		if len(wanted) == 0 || slices.Contains(wanted, b.GetService()) {
			builds = append(builds, b)
		}
	}

	if cmd.Bool("json") {
		return output.JSON(w, &apiv1.ListBuildLogsResponse{Builds: builds})
	}

	if len(builds) == 0 {
		fmt.Fprintln(cmd.Root().ErrWriter, "No build output: nothing was built, or not yet.")

		return nil
	}

	for i, b := range builds {
		if i > 0 {
			fmt.Fprintln(w)
		}

		fmt.Fprintf(w, "==> %s\n", b.GetService())

		if b.GetTruncated() {
			fmt.Fprintln(w, "(the start left out)")
		}

		fmt.Fprint(w, strings.TrimSuffix(b.GetText(), "\n")+"\n")
	}

	return nil
}

// logsSince reads --since: a duration back from now, or a time.
func logsSince(s string, now time.Time) (*timestamppb.Timestamp, error) {
	if s == "" {
		return nil, nil
	}

	if d, err := time.ParseDuration(s); err == nil {
		return timestamppb.New(now.Add(-d)), nil
	}

	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return timestamppb.New(t), nil
	}

	return nil, fmt.Errorf("--since %q: give a duration (10m, 2h) or a time (2026-11-02T13:10:00Z)", s)
}

func pickLines(lines []*apiv1.LogLine, errorsOnly bool) []*apiv1.LogLine {
	if !errorsOnly {
		return lines
	}

	var picked []*apiv1.LogLine

	for _, l := range lines {
		if l.GetLevel() == apiv1.LogLevel_LOG_LEVEL_ERROR {
			picked = append(picked, l)
		}
	}

	return picked
}

// printLines prints each line as text, or with --json as a JSON object on a line of its own.
func printLines(w io.Writer, lines []*apiv1.LogLine, asJSON, color bool) error {
	for _, l := range lines {
		var err error

		if asJSON {
			var b []byte

			if b, err = protojson.Marshal(l); err == nil {
				_, err = fmt.Fprintln(w, string(b))
			}
		} else {
			text := lineText(l)

			if color {
				text = coloredLine(l)
			}

			_, err = fmt.Fprintln(w, text)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

// lineText is a line as the dashboard copies it: its time in UTC, service, level and what it wrote.
func lineText(l *apiv1.LogLine) string {
	parts := []string{l.GetTime().AsTime().UTC().Format("2006-01-02T15:04:05.000Z"), l.GetService()}

	if level := logLevel(l.GetLevel()); level != "" {
		parts = append(parts, level)
	}

	return strings.Join(append(parts, l.GetText()), " ")
}

// In a terminal: the time dimmed, each service in a color of its own (the
// same one every run), the level by how bad it is, and an error's text red.
// The same words as lineText, so what's copied from the terminal is too.
var (
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Red)
	serviceColors = []lipgloss.Style{
		lipgloss.NewStyle().Foreground(lipgloss.Cyan),
		lipgloss.NewStyle().Foreground(lipgloss.Magenta),
		lipgloss.NewStyle().Foreground(lipgloss.Blue),
		lipgloss.NewStyle().Foreground(lipgloss.Green),
		lipgloss.NewStyle().Foreground(lipgloss.BrightCyan),
		lipgloss.NewStyle().Foreground(lipgloss.BrightMagenta),
		lipgloss.NewStyle().Foreground(lipgloss.BrightBlue),
		lipgloss.NewStyle().Foreground(lipgloss.BrightGreen),
	}
	levelStyles = map[apiv1.LogLevel]lipgloss.Style{
		apiv1.LogLevel_LOG_LEVEL_DEBUG: dimStyle,
		apiv1.LogLevel_LOG_LEVEL_INFO:  lipgloss.NewStyle().Foreground(lipgloss.Green),
		apiv1.LogLevel_LOG_LEVEL_WARN:  lipgloss.NewStyle().Foreground(lipgloss.Yellow),
		apiv1.LogLevel_LOG_LEVEL_ERROR: errorStyle.Bold(true),
	}
)

func coloredLine(l *apiv1.LogLine) string {
	parts := []string{
		dimStyle.Render(l.GetTime().AsTime().UTC().Format("2006-01-02T15:04:05.000Z")),
		serviceStyle(l.GetService()).Render(l.GetService()),
	}

	if level := logLevel(l.GetLevel()); level != "" {
		parts = append(parts, levelStyles[l.GetLevel()].Render(level))
	}

	text := l.GetText()

	if l.GetLevel() == apiv1.LogLevel_LOG_LEVEL_ERROR {
		text = errorStyle.Render(text)
	}

	return strings.Join(append(parts, text), " ")
}

// serviceStyle picks a service's color from its name, so it stays the same between runs.
func serviceStyle(service string) lipgloss.Style {
	h := fnv.New32a()
	h.Write([]byte(service))

	return serviceColors[h.Sum32()%uint32(len(serviceColors))]
}

func logLevel(l apiv1.LogLevel) string {
	if l == apiv1.LogLevel_LOG_LEVEL_UNSPECIFIED {
		return ""
	}

	return strings.TrimPrefix(l.String(), "LOG_LEVEL_")
}

func lineKey(l *apiv1.LogLine) string {
	return fmt.Sprintf("%d.%d %s %s", l.GetTime().GetSeconds(), l.GetTime().GetNanos(), l.GetService(), l.GetText())
}
