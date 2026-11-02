package cli

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"
	"google.golang.org/protobuf/types/known/durationpb"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/output"
)

func previewUsageCommand() *cli.Command {
	return &cli.Command{
		Name:      "usage",
		Usage:     "show the CPU and memory a preview's services use",
		ArgsUsage: "<id>",
		Description: "Prints each service's CPU and memory now, its average CPU and peak memory over the window,\n" +
			"its restarts and how long it has run, as the preview's Services tab has them. Samples are taken\n" +
			"once a minute while the preview is live; a sleeping one keeps what was sampled.\n\n" +
			"   stagd preview usage <id> --window 6h",
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "window", Usage: "how far back the averages and peaks reach, 1m to 24h", Value: time.Hour},
		},
		Action: previewUsage,
	}
}

func previewUsage(ctx context.Context, cmd *cli.Command) error {
	id := cmd.Args().Get(0)

	if id == "" {
		return cli.Exit("usage: stagd preview usage <id>", 2)
	}

	window := cmd.Duration("window")

	if window < time.Minute || window > 24*time.Hour {
		return cli.Exit("--window: from 1m to 24h", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Previews.GetServiceUsage(ctx, connect.NewRequest(&apiv1.GetServiceUsageRequest{PreviewId: id, Window: durationpb.New(window)}))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	services := res.Msg.GetServices()

	if len(services) == 0 {
		fmt.Fprintf(cmd.Root().ErrWriter, "No samples in the last %s: the preview hasn't been live, or isn't sampled yet.\n", shortSpan(window))

		return nil
	}

	now := time.Now()
	rows := make([][]string, 0, len(services))

	for _, s := range services {
		avg, peak := overWindow(s)

		rows = append(rows, []string{
			s.GetService(),
			fmt.Sprintf("%.1f%%", s.GetCpuPercent()),
			memoryOf(s.GetMemoryBytes(), s.GetMemoryLimitBytes()),
			fmt.Sprintf("%.1f%%", avg),
			sizeOf(peak),
			fmt.Sprint(s.GetRestarts()),
			running(s, now),
			shortSpan(now.Sub(s.GetSampledAt().AsTime())) + " ago",
		})
	}

	return output.Table(w, []string{"service", "cpu", "memory", "avg cpu", "peak memory", "restarts", "running", "sampled"}, rows)
}

// overWindow is a service's average CPU and peak memory over its points.
func overWindow(s *apiv1.ServiceUsage) (float64, int64) {
	points := s.GetPoints()

	if len(points) == 0 {
		return s.GetCpuPercent(), s.GetMemoryBytes()
	}

	var sum float64
	var peak int64

	for _, p := range points {
		sum += p.GetCpuPercent()
		peak = max(peak, p.GetMemoryBytes())
	}

	return sum / float64(len(points)), peak
}

// memoryOf is memory now and its share of the limit: "366 MiB (2%)".
func memoryOf(used, limit int64) string {
	if limit <= 0 {
		return sizeOf(used)
	}

	return fmt.Sprintf("%s (%.0f%%)", sizeOf(used), float64(used)/float64(limit)*100)
}

// running is how long the service has run since it last started; "—" when unknown.
func running(s *apiv1.ServiceUsage, now time.Time) string {
	if s.GetStartedAt() == nil {
		return "—"
	}

	return shortSpan(now.Sub(s.GetStartedAt().AsTime()))
}

// sizeOf is bytes as people read them: "512 B", "82 MiB", "1.4 GiB".
func sizeOf(b int64) string {
	const unit = 1024

	if b < unit {
		return fmt.Sprintf("%d B", b)
	}

	v := float64(b)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	i := -1

	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}

	if v >= 100 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}

	return fmt.Sprintf("%.1f %s", v, units[i])
}

// shortSpan is a duration in its two largest parts: "45s", "12m", "3h05m", "2d4h".
func shortSpan(d time.Duration) string {
	d = max(d, 0).Round(time.Second)

	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}
