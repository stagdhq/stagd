package tui

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
)

// Demo is made-up previews that change as you watch: one builds, logs flow.
// For trying the UI without an org.
type Demo struct {
	mu       sync.Mutex
	start    time.Time
	previews []*apiv1.Preview
	logs     map[string][]*apiv1.LogLine
}

func NewDemo() *Demo {
	now := time.Now()
	ts := func(ago time.Duration) *timestamppb.Timestamp { return timestamppb.New(now.Add(-ago)) }
	ns := &apiv1.PreviewCompute{Id: "c1", Name: "namespace-eu", Provider: apiv1.ComputeProvider_COMPUTE_PROVIDER_NAMESPACE}
	k8s := &apiv1.PreviewCompute{Id: "c2", Name: "office-k3s", Provider: apiv1.ComputeProvider_COMPUTE_PROVIDER_KUBERNETES}
	web := &apiv1.Repository{FullName: "acme/web"}
	apiRepo := &apiv1.Repository{FullName: "acme/api"}

	preview := func(id string, repo *apiv1.Repository, n int64, title, branch string, s apiv1.PreviewStatus, c *apiv1.PreviewCompute, ago time.Duration) *apiv1.Preview {
		p := &apiv1.Preview{
			Id: id, Repository: repo, Branch: branch, Sha: fmt.Sprintf("%07x9e2b31", rand.IntN(0xfffffff)), Status: s, Compute: c,
			CreateTime: ts(ago), ExpireTime: ts(-72 * time.Hour), StartedBy: "ada", Trigger: apiv1.PreviewTrigger_PREVIEW_TRIGGER_WEBHOOK,
			Seeds: []*apiv1.PreviewSeed{{Name: "app-anonymized", Database: "postgres"}},
		}

		if n > 0 {
			p.Change = &apiv1.ChangeRequest{Number: n, Title: title, Author: "ada", Url: fmt.Sprintf("https://github.com/%s/pull/%d", repo.GetFullName(), n)}
		} else {
			p.Trigger = apiv1.PreviewTrigger_PREVIEW_TRIGGER_API
			p.StartedBy = "claude (MCP)"
		}

		host := fmt.Sprintf("%s-%d", repo.GetFullName()[5:], n)

		if n == 0 {
			host = "web-agent-search"
		}

		if s == apiv1.PreviewStatus_PREVIEW_STATUS_LIVE || s == apiv1.PreviewStatus_PREVIEW_STATUS_SLEEPING {
			p.Url = "https://site--" + host + ".stagd.run"
			p.Ingresses = []*apiv1.PreviewIngress{
				{Name: "site", Url: p.Url, Primary: true, Protected: true},
				{Name: "api", Url: "https://api--" + host + ".stagd.run", Protected: true},
				{Name: "mail", Url: "https://mail--" + host + ".stagd.run", Builtin: "mail", Protected: true},
			}
		}

		phase := apiv1.ComponentPhase_COMPONENT_PHASE_RUNNING

		if s != apiv1.PreviewStatus_PREVIEW_STATUS_LIVE {
			phase = apiv1.ComponentPhase_COMPONENT_PHASE_PENDING
		}

		p.Components = []*apiv1.PreviewComponent{
			{Name: "web", Phase: phase},
			{Name: "api", Phase: phase},
			{Name: "worker", Phase: phase},
			{Name: "postgres", Phase: phase, Message: "restored app-anonymized (41 MB)"},
		}

		return p
	}

	failed := preview("p3", apiRepo, 477, "Bump Postgres to 17", "postgres-17", apiv1.PreviewStatus_PREVIEW_STATUS_FAILED, k8s, 26*time.Minute)
	failed.Error = "migrate exited 1"
	failed.Components[3].Phase = apiv1.ComponentPhase_COMPONENT_PHASE_FAILED
	failed.Components[3].Message = `extension "pg_trgm" is not available`
	failed.Failure = &apiv1.PreviewFailure{
		Cause:      `Postgres 17's image has no contrib extensions, and the migration creates pg_trgm.`,
		Fix:        "Add pg_trgm under services.postgres.extensions in stagd.yaml.",
		Confidence: apiv1.PreviewFailureConfidence_PREVIEW_FAILURE_CONFIDENCE_HIGH,
		Evidence: []*apiv1.PreviewFailureEvidence{
			{Source: "migrate", Text: `ERROR:  extension "pg_trgm" is not available`},
			{Source: "migrate", Text: `DETAIL:  Could not open extension control file "/usr/share/postgresql/17/extension/pg_trgm.control"`},
		},
	}

	building := preview("p2", web, 479, "Move invoices to the new API", "invoices-api", apiv1.PreviewStatus_PREVIEW_STATUS_BUILDING, ns, 40*time.Second)
	building.Waiting = "building web (3 of 5 layers)"

	d := &Demo{
		start: now,
		previews: []*apiv1.Preview{
			preview("p1", web, 482, "Checkout: apply discount codes", "discount-codes", apiv1.PreviewStatus_PREVIEW_STATUS_LIVE, ns, 2*time.Hour+14*time.Minute),
			building,
			failed,
			preview("p4", web, 471, "Dark mode for settings", "dark-settings", apiv1.PreviewStatus_PREVIEW_STATUS_SLEEPING, ns, 30*time.Hour),
			preview("p5", web, 0, "", "feature/agent-search", apiv1.PreviewStatus_PREVIEW_STATUS_LIVE, ns, 12*time.Minute),
			preview("p6", apiRepo, 468, "Rate limits per API key", "rate-limits", apiv1.PreviewStatus_PREVIEW_STATUS_LIVE, k8s, 5*24*time.Hour),
			preview("p7", web, 463, "Onboarding checklist", "onboarding", apiv1.PreviewStatus_PREVIEW_STATUS_SLEEPING, ns, 9*24*time.Hour),
		},
		logs: map[string][]*apiv1.LogLine{},
	}

	return d
}

func (d *Demo) Previews(context.Context) ([]*apiv1.Preview, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// The one building goes live after half a minute.
	if p := d.find("p2"); p != nil && p.GetStatus() == apiv1.PreviewStatus_PREVIEW_STATUS_BUILDING && time.Since(d.start) > 30*time.Second {
		p.Status = apiv1.PreviewStatus_PREVIEW_STATUS_LIVE
		p.Waiting = ""
		p.Url = "https://site--web-479.stagd.run"
		p.Ingresses = []*apiv1.PreviewIngress{{Name: "site", Url: p.Url, Primary: true, Protected: true}}

		for _, c := range p.Components {
			c.Phase = apiv1.ComponentPhase_COMPONENT_PHASE_RUNNING
		}
	}

	out := make([]*apiv1.Preview, 0, len(d.previews))

	for _, p := range d.previews {
		if p.GetStatus() != apiv1.PreviewStatus_PREVIEW_STATUS_DESTROYED {
			out = append(out, proto.Clone(p).(*apiv1.Preview))
		}
	}

	return out, nil
}

func (d *Demo) find(id string) *apiv1.Preview {
	for _, p := range d.previews {
		if p.GetId() == id {
			return p
		}
	}

	return nil
}

func (d *Demo) Preview(_ context.Context, id string) (*apiv1.Preview, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if p := d.find(id); p != nil {
		return proto.Clone(p).(*apiv1.Preview), nil
	}

	return nil, fmt.Errorf("no preview %s", id)
}

var demoLines = []struct {
	service string
	level   apiv1.LogLevel
	text    string
}{
	{"api", apiv1.LogLevel_LOG_LEVEL_INFO, "GET /v1/cart 200 18ms"},
	{"web", apiv1.LogLevel_LOG_LEVEL_UNSPECIFIED, "▸ compiled /checkout in 210ms"},
	{"api", apiv1.LogLevel_LOG_LEVEL_WARN, `POST /v1/discounts/apply 422 {"code":"SUMMER"}`},
	{"worker", apiv1.LogLevel_LOG_LEVEL_INFO, "job=send-receipt order=8812 queued"},
	{"api", apiv1.LogLevel_LOG_LEVEL_INFO, "POST /v1/discounts/apply 200 31ms"},
	{"worker", apiv1.LogLevel_LOG_LEVEL_INFO, "job=send-receipt order=8812 sent to ada@example.com (caught by Stagd)"},
	{"api", apiv1.LogLevel_LOG_LEVEL_DEBUG, "pool: 4 open, 1 idle"},
	{"api", apiv1.LogLevel_LOG_LEVEL_ERROR, "GET /v1/orders/9001 500 relation \"order_notes\" does not exist"},
	{"web", apiv1.LogLevel_LOG_LEVEL_UNSPECIFIED, "GET /checkout 200 in 44ms"},
}

func (d *Demo) Logs(_ context.Context, id string, since *timestamppb.Timestamp) ([]*apiv1.LogLine, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	lines := d.logs[id]

	// A screenful to start with, then a few more each time.
	n := 2 + rand.IntN(4)
	at := time.Now().Add(-2 * time.Second)

	if len(lines) == 0 {
		n = 40
		at = time.Now().Add(-3 * time.Minute)
	}

	step := time.Since(at) / time.Duration(n)

	for i := range n {
		l := demoLines[rand.IntN(len(demoLines))]
		lines = append(lines, &apiv1.LogLine{Time: timestamppb.New(at.Add(time.Duration(i) * step)), Service: l.service, Level: l.level, Text: l.text})
	}

	d.logs[id] = lines

	var out []*apiv1.LogLine

	for _, l := range lines {
		if since == nil || !l.GetTime().AsTime().Before(since.AsTime()) {
			out = append(out, l)
		}
	}

	return out, nil
}

func (d *Demo) Wake(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if p := d.find(id); p != nil {
		p.Status = apiv1.PreviewStatus_PREVIEW_STATUS_LIVE
	}

	return nil
}

func (d *Demo) Destroy(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if p := d.find(id); p != nil {
		p.Status = apiv1.PreviewStatus_PREVIEW_STATUS_DESTROYED
	}

	return nil
}
