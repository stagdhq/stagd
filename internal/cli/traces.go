package cli

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"
	"google.golang.org/protobuf/types/known/durationpb"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/output"
)

func previewTracesCommand() *cli.Command {
	return &cli.Command{
		Name:      "traces",
		Usage:     "list a preview's traces, or show one as a waterfall",
		ArgsUsage: "<id> [trace id]",
		Description: "Reads the traces the preview's services sent to its own OpenTelemetry endpoint\n" +
			"(`services: { otel: {} }` in stagd.yaml), newest first, from the last 15 minutes:\n\n" +
			"   stagd preview traces <id> --errors --service web\n\n" +
			"With a trace id, its spans as a waterfall: each under the span that called it, with\n" +
			"when it started and how long it took. --attributes adds each span's attributes and events.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "service", Aliases: []string{"s"}, Usage: "only traces with spans from this service"},
			&cli.StringFlag{Name: "since", Usage: "traces from this long ago (10m, 2h) or this time (2026-11-02T13:10:00Z) on; 15m when not given"},
			&cli.BoolFlag{Name: "errors", Usage: "only traces where a span failed, or the request answered 5xx"},
			&cli.DurationFlag{Name: "slower-than", Usage: "only traces that took at least this long (500ms, 2s)"},
			&cli.IntFlag{Name: "limit", Aliases: []string{"n"}, Usage: "the newest this many at most (up to 500)", Value: 50},
			&cli.BoolFlag{Name: "attributes", Aliases: []string{"a"}, Usage: "with a trace id: each span's attributes and events too"},
		},
		Action: previewTraces,
	}
}

var traceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func previewTraces(ctx context.Context, cmd *cli.Command) error {
	id := cmd.Args().Get(0)

	if id == "" {
		return cli.Exit("usage: stagd preview traces <id> [trace id]", 2)
	}

	if traceID := cmd.Args().Get(1); traceID != "" {
		if !traceIDPattern.MatchString(traceID) {
			return cli.Exit(fmt.Sprintf("%q isn't a trace id: 32 hex digits, as `stagd preview traces %s` lists them", traceID, id), 2)
		}

		return showTrace(ctx, cmd, id, traceID)
	}

	limit := cmd.Int("limit")

	if limit < 1 || limit > 500 {
		return cli.Exit("--limit is 1 to 500", 2)
	}

	since, err := logsSince(cmd.String("since"), time.Now())
	if err != nil {
		return cli.Exit(err.Error(), 2)
	}

	req := &apiv1.ListTracesRequest{PreviewId: id, Service: cmd.String("service"), Since: since, ErrorsOnly: cmd.Bool("errors"), Limit: int32(limit)}

	if d := cmd.Duration("slower-than"); d > 0 {
		req.MinDuration = durationpb.New(d)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Previews.ListTraces(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	traces := res.Msg.GetTraces()

	if len(traces) == 0 {
		fmt.Fprintln(cmd.Root().ErrWriter, noTraces(cmd, res.Msg.GetServices()))

		return nil
	}

	color := output.Color(w)
	rows := make([][]string, 0, len(traces))

	for _, t := range traces {
		mark := ""

		if t.GetError() {
			mark = "error"
		}

		rows = append(rows, []string{
			t.GetStartTime().AsTime().Local().Format("15:04:05"),
			traceName(t),
			t.GetService(),
			shortDuration(t.GetDuration().AsDuration()),
			fmt.Sprint(t.GetSpanCount()),
			mark,
			t.GetTraceId(),
		})
	}

	if err := output.Table(w, []string{"started", "trace", "service", "took", "spans", "", "trace id"}, rows); err != nil {
		return err
	}

	if len(traces) == limit {
		fmt.Fprintf(cmd.Root().ErrWriter, "\nThe newest %d (--limit for more, --since for longer ago).\n", limit)
	}

	if !color {
		return nil
	}

	_, err = fmt.Fprintln(cmd.Root().ErrWriter, dimStyle.Render(fmt.Sprintf("\nOne as a waterfall: stagd preview traces %s <trace id>", id)))

	return err
}

// noTraces says why the list is empty, and what to try.
func noTraces(cmd *cli.Command, services []string) string {
	filtered := cmd.IsSet("service") || cmd.Bool("errors") || cmd.IsSet("slower-than")
	window := "in the last 15 minutes"

	if s := cmd.String("since"); s != "" {
		window = "since " + s

		if _, err := time.ParseDuration(s); err == nil {
			window = "in the last " + s
		}
	}

	switch {
	case filtered && len(services) > 0:
		return fmt.Sprintf("No traces like that %s. Services sending traces: %s.", window, strings.Join(services, ", "))
	case filtered:
		return fmt.Sprintf("No traces like that %s.", window)
	default:
		return fmt.Sprintf("No traces %s. The app sends them when stagd.yaml has `services: { otel: {} }` and it's been used (--since for longer ago).", window)
	}
}

// traceName is a trace as people know it: the request when the root span was
// one ("GET /api/orders 500"), its span's name otherwise.
// Long routes (a hash in the path) are cut, keeping the status code.
func traceName(t *apiv1.TraceSummary) string {
	name := t.GetName()

	if m := t.GetHttpMethod(); m != "" && !strings.HasPrefix(name, m) {
		name = m + " " + name
	}

	name = cut(name, 56)

	if code := t.GetHttpStatusCode(); code != 0 {
		name += fmt.Sprintf(" %d", code)
	}

	return name
}

func showTrace(ctx context.Context, cmd *cli.Command, id, traceID string) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Previews.GetTrace(ctx, connect.NewRequest(&apiv1.GetTraceRequest{PreviewId: id, TraceId: traceID}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	return printWaterfall(w, res.Msg.GetSpans(), waterfallOptions{
		attributes: cmd.Bool("attributes"),
		color:      output.Color(w),
		width:      output.TerminalWidth(w),
	})
}

type waterfallOptions struct {
	attributes bool
	color      bool
	// The terminal's width; 0 when it isn't one.
	width int
}

// A span placed in the waterfall: how deep it is under the root.
type placedSpan struct {
	span  *apiv1.Span
	depth int
}

// placeSpans orders spans as a tree: each after the span that called it,
// siblings by when they started. A span whose caller isn't in the trace (a
// root, or one called from outside) starts a tree of its own.
func placeSpans(spans []*apiv1.Span) []placedSpan {
	byID := map[string]bool{}

	for _, s := range spans {
		byID[s.GetSpanId()] = true
	}

	children := map[string][]*apiv1.Span{}
	var roots []*apiv1.Span

	for _, s := range spans {
		if p := s.GetParentSpanId(); p != "" && byID[p] && p != s.GetSpanId() {
			children[p] = append(children[p], s)
		} else {
			roots = append(roots, s)
		}
	}

	byStart := func(a, b *apiv1.Span) int { return a.GetStartTime().AsTime().Compare(b.GetStartTime().AsTime()) }
	slices.SortStableFunc(roots, byStart)

	var placed []placedSpan
	seen := map[string]bool{}

	var walk func(s *apiv1.Span, depth int)
	walk = func(s *apiv1.Span, depth int) {
		if seen[s.GetSpanId()] {
			return
		}

		seen[s.GetSpanId()] = true
		placed = append(placed, placedSpan{span: s, depth: depth})

		kids := children[s.GetSpanId()]
		slices.SortStableFunc(kids, byStart)

		for _, k := range kids {
			walk(k, depth+1)
		}
	}

	for _, r := range roots {
		walk(r, 0)
	}

	return placed
}

var (
	barStyle      = lipgloss.NewStyle().Foreground(lipgloss.Cyan)
	errorBarStyle = lipgloss.NewStyle().Foreground(lipgloss.Red)
)

// printWaterfall prints each span on a line: its name indented under its
// caller, service, when it started after the trace did, how long it took, and
// in a terminal a bar showing where in the trace it ran.
func printWaterfall(w io.Writer, spans []*apiv1.Span, o waterfallOptions) error {
	if len(spans) == 0 {
		_, err := fmt.Fprintln(w, "No spans in that trace.")

		return err
	}

	placed := placeSpans(spans)
	start, end := traceBounds(spans)
	total := end.Sub(start)

	names := make([]string, len(placed))
	nameWidth, serviceWidth := 0, 0

	for i, p := range placed {
		names[i] = strings.Repeat("  ", p.depth) + p.span.GetName()
		nameWidth = max(nameWidth, len([]rune(names[i])))
		serviceWidth = max(serviceWidth, len(p.span.GetService()))
	}

	nameWidth = min(nameWidth, 60)

	// The bar gets what's left of the terminal, between 10 and 40 columns.
	barWidth := 0

	if o.width > 0 {
		barWidth = min(max(o.width-nameWidth-serviceWidth-26, 10), 40)
	}

	fmt.Fprintf(w, "%s  %d spans  %s\n\n", start.Local().Format("2006-01-02 15:04:05.000"), len(spans), shortDuration(total))

	for i, p := range placed {
		s := p.span
		at := s.GetStartTime().AsTime().Sub(start)
		took := s.GetDuration().AsDuration()

		name := cut(names[i], nameWidth)
		line := fmt.Sprintf("%-*s  %-*s  +%-8s %8s", nameWidth, name, serviceWidth, s.GetService(), shortDuration(at), shortDuration(took))

		if s.GetError() {
			line += "  error"
		}

		if o.color {
			line = colorSpanLine(line, s, barWidth, at, took, total)
		} else if barWidth > 0 {
			line += "  " + bar(barWidth, at, took, total)
		}

		fmt.Fprintln(w, line)

		if msg := s.GetStatusMessage(); msg != "" && s.GetError() {
			fmt.Fprintf(w, "%s  %s\n", strings.Repeat(" ", min(p.depth*2+2, nameWidth)), maybe(o.color, errorStyle, msg))
		}

		if o.attributes {
			printSpanDetails(w, s, p.depth, start, o.color)
		}
	}

	return nil
}

func colorSpanLine(line string, s *apiv1.Span, barWidth int, at, took, total time.Duration) string {
	style := barStyle

	if s.GetError() {
		style = errorBarStyle
		line = errorStyle.Render(line)
	}

	if barWidth > 0 {
		line += "  " + style.Render(bar(barWidth, at, took, total))
	}

	return line
}

// bar is where a span ran in the trace, in width columns: spaces before it,
// then blocks for as long as it took, at least one so short spans show.
func bar(width int, at, took, total time.Duration) string {
	if total <= 0 {
		return strings.Repeat("█", 1)
	}

	from := int(float64(at) / float64(total) * float64(width))
	length := max(int(float64(took)/float64(total)*float64(width)+0.5), 1)
	from = min(max(from, 0), width-1)
	length = min(length, width-from)

	return strings.Repeat(" ", from) + strings.Repeat("█", length) + strings.Repeat(" ", width-from-length)
}

func printSpanDetails(w io.Writer, s *apiv1.Span, depth int, start time.Time, color bool) {
	indent := strings.Repeat(" ", depth*2+4)

	for _, a := range s.GetAttributes() {
		fmt.Fprintf(w, "%s%s %s\n", indent, maybe(color, dimStyle, a.GetKey()+":"), a.GetValue())
	}

	for _, e := range s.GetEvents() {
		fmt.Fprintf(w, "%s%s %s\n", indent, maybe(color, dimStyle, "event +"+shortDuration(e.GetTime().AsTime().Sub(start))), e.GetName())

		for _, a := range e.GetAttributes() {
			fmt.Fprintf(w, "%s  %s %s\n", indent, maybe(color, dimStyle, a.GetKey()+":"), a.GetValue())
		}
	}
}

// traceBounds is when the first span started and the last one ended.
func traceBounds(spans []*apiv1.Span) (time.Time, time.Time) {
	start := spans[0].GetStartTime().AsTime()
	end := start

	for _, s := range spans {
		from := s.GetStartTime().AsTime()
		to := from.Add(s.GetDuration().AsDuration())

		if from.Before(start) {
			start = from
		}

		if to.After(end) {
			end = to
		}
	}

	return start, end
}

// shortDuration is a duration as a person reads it: 840µs, 12ms, 1.4s, 2m5s.
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < 10*time.Millisecond:
		return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}

func cut(s string, width int) string {
	r := []rune(s)

	if len(r) <= width {
		return s
	}

	return string(r[:width-1]) + "…"
}

func maybe(color bool, style lipgloss.Style, s string) string {
	if !color {
		return s
	}

	return style.Render(s)
}
