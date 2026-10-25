package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"
	"google.golang.org/protobuf/types/known/durationpb"

	"stagd.dev/stagd/internal/client/api"
	apiv1 "stagd.dev/stagd/internal/gen/stagd/api/v1"
	"stagd.dev/stagd/internal/output"
)

func previewCommand() *cli.Command {
	idArg := "<id>"
	noWait := &cli.BoolFlag{Name: "no-wait", Usage: "return at once instead of following the preview until it's live"}

	return &cli.Command{
		Name:  "preview",
		Usage: "start, follow and tear down previews",
		Commands: []*cli.Command{
			{
				Name:  "create",
				Usage: "start a preview of a pull request or branch (or update it to a new commit)",
				Description: "Follows the preview until it's live or has failed, and exits non-zero on failure,\n" +
					"so CI jobs and agents can wait on it.",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "repo", Usage: "the repository, owner/name", Required: true},
					&cli.IntFlag{Name: "pr", Usage: "a pull request number"},
					&cli.StringFlag{Name: "branch", Usage: "a branch"},
					&cli.StringFlag{Name: "sha", Usage: "pin a commit"},
					noWait,
				},
				Action: previewCreate,
			},
			{
				Name:  "list",
				Usage: "list your org's previews",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "repo", Usage: "only this repository"},
					&cli.BoolFlag{Name: "all", Usage: "include torn-down previews"},
				},
				Action: previewList,
			},
			{Name: "get", Usage: "show one preview", ArgsUsage: idArg, Action: previewGet},
			{Name: "watch", Usage: "follow a preview until it's live, asleep, failed or torn down", ArgsUsage: idArg, Action: previewWatch},
			{Name: "wake", Usage: "start a sleeping preview again", ArgsUsage: idArg, Flags: []cli.Flag{noWait}, Action: previewWake},
			{Name: "destroy", Usage: "tear a preview down now", ArgsUsage: idArg, Action: previewDestroy},
			previewMailCommand(),
			{
				Name:  "policy",
				Usage: "who may open previews, and how their URLs look",
				Description: "Without flags, shows the policy. --repo picks one repository (its own override, or the org's default\n" +
					"it follows); without --repo it's the org's default. Access applies to existing previews on their\n" +
					"next deploy; URL style only to new previews, so links already shared keep working. A repository's\n" +
					"stagd.yaml can set its own sleep and expiry (lifecycle: { sleep_after: 2h, expire_after: 36h }), which win.",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "repo", Usage: "a repository, owner/name; the org's default when left out"},
					&cli.StringFlag{Name: "access", Usage: "org (members signed in to Stagd) or public (anyone with the link)"},
					&cli.StringSliceFlag{Name: "public-ingress", Usage: "an ingress anyone can reach even when access is org (repeatable)"},
					&cli.BoolFlag{Name: "unguessable-urls", Usage: "add a random token to new previews' URLs (--unguessable-urls=false to stop)"},
					&cli.StringFlag{Name: "sleep-after", Usage: "how long a preview nobody opens stays awake, 30m to 24h, e.g. 2h; default for Stagd's (45m)"},
					&cli.StringFlag{Name: "expire-after", Usage: "how long a preview lives without a new commit, 1h to 48h, e.g. 36h; default for Stagd's (24h)"},
					&cli.BoolFlag{Name: "reset", Usage: "drop the repository's override; it follows the org's default again"},
				},
				Action: previewPolicy,
			},
		},
	}
}

func accessLabel(a apiv1.PreviewAccess) string {
	return label(a.String(), "PREVIEW_ACCESS_")
}

func previewPolicy(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	repo := cmd.String("repo")
	current, err := c.Previews.GetPreviewPolicy(ctx, connect.NewRequest(&apiv1.GetPreviewPolicyRequest{Repository: repo}))
	if err != nil {
		return err
	}

	res := &apiv1.UpdatePreviewPolicyResponse{Policy: current.Msg.GetPolicy(), Inherited: current.Msg.GetInherited()}
	changing := cmd.IsSet("access") || cmd.IsSet("public-ingress") || cmd.IsSet("unguessable-urls") || cmd.IsSet("sleep-after") || cmd.IsSet("expire-after") || cmd.Bool("reset")

	if changing {
		// Start from what applies now and change only what was asked.
		policy := current.Msg.GetPolicy()
		policy.Repository = repo

		if cmd.IsSet("access") {
			switch cmd.String("access") {
			case "org":
				policy.Access = apiv1.PreviewAccess_PREVIEW_ACCESS_ORG
			case "public":
				policy.Access = apiv1.PreviewAccess_PREVIEW_ACCESS_PUBLIC
			default:
				return cli.Exit("--access: org or public", 2)
			}
		}

		if cmd.IsSet("public-ingress") {
			policy.PublicIngresses = cmd.StringSlice("public-ingress")
		}

		if cmd.IsSet("unguessable-urls") {
			policy.UnguessableUrls = cmd.Bool("unguessable-urls")
		}

		if cmd.IsSet("sleep-after") {
			d, err := policyDuration(cmd.String("sleep-after"))
			if err != nil {
				return cli.Exit("--sleep-after: "+err.Error(), 2)
			}

			policy.SleepAfter = d
		}

		if cmd.IsSet("expire-after") {
			d, err := policyDuration(cmd.String("expire-after"))
			if err != nil {
				return cli.Exit("--expire-after: "+err.Error(), 2)
			}

			policy.ExpireAfter = d
		}

		updated, err := c.Previews.UpdatePreviewPolicy(ctx, connect.NewRequest(&apiv1.UpdatePreviewPolicyRequest{Policy: policy, Reset_: cmd.Bool("reset")}))
		if err != nil {
			return err
		}

		res = updated.Msg
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res)
	}

	p := res.GetPolicy()
	scope := "org default"

	if repo != "" {
		scope = repo
		if res.GetInherited() {
			scope += " (follows the org default)"
		}
	}

	public := strings.Join(p.GetPublicIngresses(), ", ")
	if public == "" {
		public = "none"
	}

	fmt.Fprintf(w, "%s\n", scope)
	fmt.Fprintf(w, "access:           %s\n", accessLabel(p.GetAccess()))
	fmt.Fprintf(w, "public ingresses: %s\n", public)
	fmt.Fprintf(w, "unguessable URLs: %s\n", yesNo(p.GetUnguessableUrls()))
	fmt.Fprintf(w, "sleeps after:     %s\n", policySpan(p.GetSleepAfter(), "45m"))
	fmt.Fprintf(w, "expires after:    %s\n", policySpan(p.GetExpireAfter(), "24h"))

	return nil
}

// policyDuration reads 2h, 90m or 7d; "default" (nil) leaves it to Stagd.
func policyDuration(s string) (*durationpb.Duration, error) {
	s = strings.TrimSpace(s)

	if s == "default" {
		return nil, nil
	}

	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("a duration like 2h, 90m or 7d, or default; not %q", s)
		}

		return durationpb.New(time.Duration(n) * 24 * time.Hour), nil
	}

	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return nil, fmt.Errorf("a duration like 2h, 90m or 7d, or default; not %q", s)
	}

	return durationpb.New(d), nil
}

// policySpan shows a policy's duration, or Stagd's default when it has none.
func policySpan(d *durationpb.Duration, fallback string) string {
	if d == nil {
		return fallback + " (Stagd's default)"
	}

	v := d.AsDuration()

	if v >= 24*time.Hour && v%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", v/(24*time.Hour))
	}

	out := strings.TrimSuffix(v.String(), "0s")

	if strings.HasSuffix(out, "h0m") {
		out = strings.TrimSuffix(out, "0m")
	}

	return out
}

func statusLabel(s apiv1.PreviewStatus) string {
	return label(s.String(), "PREVIEW_STATUS_")
}

// what names what a preview runs: "PR #42" or "branch main", with the commit.
func what(p *apiv1.Preview) string {
	s := "branch " + p.GetBranch()
	if n := p.GetChange().GetNumber(); n > 0 {
		s = fmt.Sprintf("PR #%d", n)
	}

	if sha := p.GetSha(); sha != "" {
		s += " @ " + sha[:min(7, len(sha))]
	}

	return s
}

func previewCreate(ctx context.Context, cmd *cli.Command) error {
	pr, branch := cmd.Int("pr"), strings.TrimSpace(cmd.String("branch"))

	if (pr > 0) == (branch != "") {
		return cli.Exit("say what to preview: --pr <number> or --branch <name>", 2)
	}

	source := &apiv1.PreviewSource{Sha: cmd.String("sha")}
	if pr > 0 {
		source.Kind = &apiv1.PreviewSource_ChangeNumber{ChangeNumber: int64(pr)}
	} else {
		source.Kind = &apiv1.PreviewSource_Branch{Branch: branch}
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Previews.CreatePreview(ctx, connect.NewRequest(&apiv1.CreatePreviewRequest{Repository: cmd.String("repo"), Source: source}))
	if err != nil {
		return err
	}

	return afterStart(ctx, cmd, c, res.Msg.GetPreview())
}

// afterStart prints the preview, then follows it unless --no-wait.
func afterStart(ctx context.Context, cmd *cli.Command, c *api.Clients, p *apiv1.Preview) error {
	w := cmd.Root().Writer

	if cmd.Bool("no-wait") {
		if cmd.Bool("json") {
			return output.JSON(w, p)
		}

		fmt.Fprintf(w, "Preview %s of %s: %s\n", p.GetId(), what(p), statusLabel(p.GetStatus()))

		return nil
	}

	if !cmd.Bool("json") {
		fmt.Fprintf(w, "Preview %s of %s %s\n", p.GetId(), p.GetRepository().GetFullName(), what(p))
	}

	return follow(ctx, cmd, c, p.GetId())
}

// follow streams a preview's progress and fails when it does.
func follow(ctx context.Context, cmd *cli.Command, c *api.Clients, id string) error {
	stream, err := c.Previews.WatchPreview(ctx, connect.NewRequest(&apiv1.WatchPreviewRequest{Id: id}))
	if err != nil {
		return err
	}
	defer stream.Close()

	w := cmd.Root().Writer
	var last *apiv1.Preview

	for stream.Receive() {
		msg := stream.Msg()
		last = msg.GetPreview()

		if cmd.Bool("json") {
			continue
		}

		// Component messages already start with the component's name.
		fmt.Fprintf(w, "%s  %-12s %s\n", time.Now().Format("15:04:05"), statusLabel(last.GetStatus()), msg.GetMessage())
	}

	if err := stream.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}

	if last == nil {
		return errors.New("the preview didn't report its state")
	}

	if cmd.Bool("json") {
		if err := output.JSON(w, last); err != nil {
			return err
		}
	}

	switch last.GetStatus() {
	case apiv1.PreviewStatus_PREVIEW_STATUS_LIVE:
		if !cmd.Bool("json") {
			printIngresses(w, last)
		}

		return nil
	case apiv1.PreviewStatus_PREVIEW_STATUS_FAILED:
		return fmt.Errorf("the preview failed: %s", last.GetError())
	case apiv1.PreviewStatus_PREVIEW_STATUS_SLEEPING, apiv1.PreviewStatus_PREVIEW_STATUS_DESTROYED:
		return nil
	default:
		return fmt.Errorf("stopped following while it was %s; run `stagd preview watch %s`", statusLabel(last.GetStatus()), last.GetId())
	}
}

func previewList(ctx context.Context, cmd *cli.Command) error {
	c, err := clients(cmd)
	if err != nil {
		return err
	}

	req := &apiv1.ListPreviewsRequest{Repository: cmd.String("repo")}
	if cmd.Bool("all") {
		for s := apiv1.PreviewStatus_PREVIEW_STATUS_QUEUED; s <= apiv1.PreviewStatus_PREVIEW_STATUS_DESTROYED; s++ {
			req.Statuses = append(req.Statuses, s)
		}
	}

	res, err := c.Previews.ListPreviews(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}

	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, res.Msg)
	}

	rows := make([][]string, 0, len(res.Msg.GetPreviews()))

	for _, p := range res.Msg.GetPreviews() {
		rows = append(rows, []string{p.GetRepository().GetFullName(), what(p), statusLabel(p.GetStatus()), p.GetUrl(), p.GetId()})
	}

	return output.Table(w, []string{"repository", "of", "status", "url", "id"}, rows)
}

func previewGet(ctx context.Context, cmd *cli.Command) error {
	id := cmd.Args().First()
	if id == "" {
		return cli.Exit("usage: stagd preview get <id>", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Previews.GetPreview(ctx, connect.NewRequest(&apiv1.GetPreviewRequest{Id: id}))
	if err != nil {
		return err
	}

	p := res.Msg.GetPreview()
	w := cmd.Root().Writer

	if cmd.Bool("json") {
		return output.JSON(w, p)
	}

	fmt.Fprintf(w, "%s %s\n", p.GetRepository().GetFullName(), what(p))
	fmt.Fprintf(w, "status:  %s\n", statusLabel(p.GetStatus()))

	if e := p.GetError(); e != "" {
		fmt.Fprintf(w, "error:   %s\n", e)
	}

	fmt.Fprintf(w, "expires: %s\n", p.GetExpireTime().AsTime().Local().Format("2006-01-02 15:04"))

	if len(p.GetIngresses()) == 0 {
		fmt.Fprintf(w, "url:     %s\n", p.GetUrl())
	} else {
		fmt.Fprintln(w)
		printIngresses(w, p)
	}

	if len(p.GetComponents()) > 0 {
		fmt.Fprintln(w)
		rows := make([][]string, 0, len(p.GetComponents()))

		for _, c := range p.GetComponents() {
			rows = append(rows, []string{c.GetName(), label(c.GetPhase().String(), "COMPONENT_PHASE_"), c.GetMessage()})
		}

		return output.Table(w, []string{"component", "phase", ""}, rows)
	}

	return nil
}

// printIngresses lists a preview's URLs, the primary one first and marked.
func printIngresses(w io.Writer, p *apiv1.Preview) {
	if len(p.GetIngresses()) == 0 {
		fmt.Fprintf(w, "Live at %s\n", p.GetUrl())

		return
	}

	fmt.Fprintln(w, "Live at:")

	for _, i := range p.GetIngresses() {
		marker := " "
		if i.GetPrimary() {
			marker = "*"
		}

		who := "anyone with the link"
		if i.GetProtected() {
			who = "org members"
		}

		if i.GetBuiltin() == "mail" {
			who += ", mail inbox: stagd preview mail " + p.GetId()
		}

		fmt.Fprintf(w, "  %s %-10s %s  (%s)\n", marker, i.GetName(), i.GetUrl(), who)
	}
}

func previewWatch(ctx context.Context, cmd *cli.Command) error {
	id := cmd.Args().First()
	if id == "" {
		return cli.Exit("usage: stagd preview watch <id>", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	return follow(ctx, cmd, c, id)
}

func previewWake(ctx context.Context, cmd *cli.Command) error {
	id := cmd.Args().First()
	if id == "" {
		return cli.Exit("usage: stagd preview wake <id>", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	res, err := c.Previews.WakePreview(ctx, connect.NewRequest(&apiv1.WakePreviewRequest{Id: id}))
	if err != nil {
		return err
	}

	return afterStart(ctx, cmd, c, res.Msg.GetPreview())
}

func previewDestroy(ctx context.Context, cmd *cli.Command) error {
	id := cmd.Args().First()
	if id == "" {
		return cli.Exit("usage: stagd preview destroy <id>", 2)
	}

	c, err := clients(cmd)
	if err != nil {
		return err
	}

	if _, err := c.Previews.DestroyPreview(ctx, connect.NewRequest(&apiv1.DestroyPreviewRequest{Id: id})); err != nil {
		return err
	}

	_, err = fmt.Fprintln(cmd.Root().Writer, "Tearing it down.")

	return err
}
