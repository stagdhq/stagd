package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
	"github.com/stagdhq/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// fakeSeeds is an org with an Azure container, seeds, whose folder has one dump.
type fakeSeeds struct {
	apiv1connect.UnimplementedSeedServiceHandler

	seeds []*apiv1.Seed
}

var nightly = &apiv1.SeedFile{Name: "platform/2026-10-28.dump", SizeBytes: 1_234_000_000, ModifyTime: mailAt}

func (f *fakeSeeds) find(integration, path string) (*apiv1.SeedFile, error) {
	if integration != "seeds" {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}

	if path != "platform/" && path != nightly.GetName() {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	return nightly, nil
}

func (f *fakeSeeds) ListSeeds(context.Context, *connect.Request[apiv1.ListSeedsRequest]) (*connect.Response[apiv1.ListSeedsResponse], error) {
	return connect.NewResponse(&apiv1.ListSeedsResponse{Seeds: f.seeds}), nil
}

func (f *fakeSeeds) CreateSeed(_ context.Context, req *connect.Request[apiv1.CreateSeedRequest]) (*connect.Response[apiv1.CreateSeedResponse], error) {
	file, err := f.find(req.Msg.GetIntegration(), req.Msg.GetPath())
	if err != nil {
		return nil, err
	}

	s := &apiv1.Seed{Name: req.Msg.GetName(), IntegrationName: req.Msg.GetIntegration(), Path: req.Msg.GetPath(), File: file}
	f.seeds = append(f.seeds, s)

	return connect.NewResponse(&apiv1.CreateSeedResponse{Seed: s}), nil
}

func (f *fakeSeeds) FindSeedFile(_ context.Context, req *connect.Request[apiv1.FindSeedFileRequest]) (*connect.Response[apiv1.FindSeedFileResponse], error) {
	file, err := f.find(req.Msg.GetIntegration(), req.Msg.GetPath())
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&apiv1.FindSeedFileResponse{File: file}), nil
}

func (f *fakeSeeds) DeleteSeed(_ context.Context, req *connect.Request[apiv1.DeleteSeedRequest]) (*connect.Response[apiv1.DeleteSeedResponse], error) {
	for i, s := range f.seeds {
		if s.GetName() == req.Msg.GetSeed() {
			f.seeds = append(f.seeds[:i], f.seeds[i+1:]...)

			return connect.NewResponse(&apiv1.DeleteSeedResponse{}), nil
		}
	}

	return nil, connect.NewError(connect.CodeNotFound, nil)
}

func seedsServer(t *testing.T, f *fakeSeeds) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	path, handler := apiv1connect.NewSeedServiceHandler(f)

	mux.Handle(path, handler)

	srv := httptest.NewServer(mux)

	t.Cleanup(srv.Close)

	return srv
}

func TestSeedsAddListFindRemove(t *testing.T) {
	f := &fakeSeeds{}
	srv := seedsServer(t, f)

	out, err := run(t, srv, "--token", "tok-1", "seeds", "list")
	if err != nil || !strings.Contains(out, "No seeds yet") {
		t.Fatalf("empty list: %v\n%s", err, out)
	}

	out, err = run(t, srv, "--token", "tok-1", "seeds", "add", "app", "--from", "seeds", "--path", "platform/")
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"Added app: platform/ in seeds", "It finds platform/2026-10-28.dump, 1.2 GB", "seed: app"} {
		if !strings.Contains(out, want) {
			t.Errorf("add said:\n%s\nwant %q", out, want)
		}
	}

	out, err = run(t, srv, "--token", "tok-1", "seeds", "list")
	if err != nil || !strings.Contains(out, "app") || !strings.Contains(out, "platform/2026-10-28.dump, 1.2 GB") {
		t.Errorf("list: %v\n%s", err, out)
	}

	out, err = run(t, srv, "--token", "tok-1", "seeds", "find", "platform/", "--from", "seeds")
	if err != nil || !strings.HasPrefix(out, "platform/2026-10-28.dump, 1.2 GB") {
		t.Errorf("find: %v\n%s", err, out)
	}

	if _, err := run(t, srv, "--token", "tok-1", "seeds", "remove", "app", "--yes"); err != nil {
		t.Fatal(err)
	}

	if len(f.seeds) != 0 {
		t.Errorf("seeds left: %v", f.seeds)
	}
}

func TestSize(t *testing.T) {
	for n, want := range map[int64]string{512: "512 bytes", 12_000: "12 kB", 1_234_000_000: "1.2 GB", 45_600_000: "46 MB"} {
		if got := size(n); got != want {
			t.Errorf("size(%d) = %q, want %q", n, got, want)
		}
	}
}
