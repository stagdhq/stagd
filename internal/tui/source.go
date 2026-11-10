// Package tui is stagd ui: previews in a terminal, laid out like k9s. A
// table of the org's previews, a preview described, and its logs followed;
// waking, destroying and opening one in the browser. What it shows comes
// from a Source: Stagd's API, or made-up previews (--demo).
package tui

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
)

// Source is where the previews come from.
type Source interface {
	Previews(ctx context.Context) ([]*apiv1.Preview, error)
	Preview(ctx context.Context, id string) (*apiv1.Preview, error)

	// Logs are a preview's lines from since on; all of them at since too,
	// so the caller leaves out the ones it has.
	Logs(ctx context.Context, id string, since *timestamppb.Timestamp) ([]*apiv1.LogLine, error)

	Wake(ctx context.Context, id string) error
	Destroy(ctx context.Context, id string) error
}

// Context is what the header says about where the previews come from.
type Context struct {
	Org     string
	User    string
	Server  string
	Version string
}
