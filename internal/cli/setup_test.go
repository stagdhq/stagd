package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// fakeOnboarding answers GetOnboarding for an org halfway: GitHub and repositories done, compute next, admin-only.
type fakeOnboarding struct {
	apiv1connect.UnimplementedOrgServiceHandler

	complete bool
}

func (f *fakeOnboarding) GetOnboarding(context.Context, *connect.Request[apiv1.GetOnboardingRequest]) (*connect.Response[apiv1.GetOnboardingResponse], error) {
	o := &apiv1.Onboarding{
		Done:     2,
		Required: 5,
		Next:     "compute",
		Admins:   []*apiv1.User{{Name: "Ada Lovelace", Email: "ada@acme.example"}},
		Steps: []*apiv1.OnboardingStep{
			{Id: "github", Title: "Connect GitHub", Done: true, AdminOnly: true, Detail: "acme-inc on GitHub"},
			{Id: "repositories", Title: "Choose repositories", Done: true, AdminOnly: true, Detail: "acme/webapp"},
			{Id: "compute", Title: "Connect compute", AdminOnly: true, Detail: "Where previews run.", Url: "https://app.stagd.dev/settings/compute"},
			{Id: "first_preview", Title: "First preview"},
			{Id: "invite", Title: "Invite the team", Optional: true, AdminOnly: true, Detail: "Bring in the people who review.", Url: "https://app.stagd.dev/settings/members"},
		},
	}

	if f.complete {
		o.CompleteTime = timestamppb.Now()
	}

	return connect.NewResponse(&apiv1.GetOnboardingResponse{Onboarding: o}), nil
}

func TestSetupPrintsTheStepsAndTheNextOne(t *testing.T) {
	mux := http.NewServeMux()

	path, handler := apiv1connect.NewOrgServiceHandler(&fakeOnboarding{})
	mux.Handle(path, handler)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	out, err := run(t, srv, "--token", "tok-1", "setup")
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"2 of 5 done",
		"✓ Connect GitHub",
		"acme-inc on GitHub",
		"● Connect compute",
		"An admin's step: Ada Lovelace.",
		"https://app.stagd.dev/settings/compute",
		"○ First preview",
		"Optional",
		"○ Invite the team",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}

	// Steps that aren't next don't spell themselves out.
	if strings.Count(out, "https://") != 2 {
		t.Errorf("only the next step's and optional steps' links:\n%s", out)
	}
}

func TestInstallCursorKeepsOtherServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursor", "mcp.json")

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(`{"mcpServers":{"other":{"command":"other"}},"theme":"dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := installCursor(path, "stagd"); err != nil {
		t.Fatal(err)
	}

	var got map[string]any

	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	servers := got["mcpServers"].(map[string]any)

	if servers["other"] == nil || got["theme"] != "dark" {
		t.Errorf("lost what was there: %s", raw)
	}

	if s := servers["stagd"].(map[string]any); s["command"] != "stagd" || s["args"].([]any)[0] != "mcp" {
		t.Errorf("stagd: %v", s)
	}

	// Not JSON: left alone.
	_ = os.WriteFile(path, []byte("{nope"), 0o644)

	if err := installCursor(path, "stagd"); err == nil {
		t.Error("broken mcp.json: want an error")
	}
}

func TestInstallClaude(t *testing.T) {
	saved := runClaude
	defer func() { runClaude = saved }()

	var asked []string

	runClaude = func(_ context.Context, args ...string) ([]byte, error) {
		asked = args

		return nil, nil
	}

	if ok, err := installClaude(context.Background(), "stagd"); !ok || err != nil {
		t.Fatalf("ok %v, err %v", ok, err)
	}

	if strings.Join(asked, " ") != "mcp add --scope user stagd -- stagd mcp" {
		t.Errorf("ran claude %v", asked)
	}

	runClaude = func(context.Context, ...string) ([]byte, error) {
		return []byte("MCP server stagd already exists in user config"), errors.New("exit 1")
	}

	if ok, err := installClaude(context.Background(), "stagd"); !ok || err != nil {
		t.Errorf("already there: ok %v, err %v", ok, err)
	}

	runClaude = func(context.Context, ...string) ([]byte, error) { return nil, errNoClaude }

	if ok, err := installClaude(context.Background(), "stagd"); ok || err != nil {
		t.Errorf("not installed: ok %v, err %v", ok, err)
	}
}
