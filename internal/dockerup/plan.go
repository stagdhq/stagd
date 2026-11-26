// Package dockerup brings a preview up on a machine that runs Docker, from
// a plan: what the preview runs, whatever compute it runs on. Stagd puts the
// plan on the instance (Namespace, a Docker host) and runs `stagd docker up`
// there; the same plan, fetched with `stagd preview plan`, runs on your own
// Docker.
//
// The plan is stagd.plan.v1.Plan (proto/stagd/plan/v1), as protojson: it
// says nothing of Docker. How it runs here (the compose project, whether
// ingresses are published, where built images are kept) is Options.
//
// It writes where it stands to <dir>/state, as Stagd reads it:
//
//	starting
//	progress <what it's doing>
//	running
//	failed <why, in words a person can act on>
//
// The lines after the first are when each part of the work began and ended
// so far, for the deploy's trace:
//
//	<ms since the epoch> begin|end <part>, e.g. build-web or start
package dockerup

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/encoding/protojson"

	planv1 "github.com/stagdhq/stagd/internal/gen/stagd/plan/v1"
)

// Version is the plan format this CLI reads.
const Version = 1

// Plan is what a preview runs.
type Plan = planv1.Plan

// Load reads a plan file and checks it.
func Load(path string) (*Plan, error) {
	data, err := os.ReadFile(path)

	if err != nil {
		return nil, err
	}

	return Parse(data)
}

// Parse reads a plan and checks it.
func Parse(data []byte) (*Plan, error) {
	var p Plan

	if err := protojson.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("the plan isn't a plan this stagd can read (update stagd?): %w", err)
	}

	if p.Version != Version {
		return nil, fmt.Errorf("the plan is version %d; this stagd reads version %d: update stagd", p.Version, Version)
	}

	if err := protovalidate.Validate(&p); err != nil {
		return nil, fmt.Errorf("the plan: %w", err)
	}

	if err := check(&p); err != nil {
		return nil, fmt.Errorf("the plan: %w", err)
	}

	return &p, nil
}

// check refuses what the fields' own rules can't: names given twice, and names that aren't there.
func check(p *Plan) error {
	names := map[string]bool{}

	for _, s := range p.Services {
		if names[s.Name] {
			return fmt.Errorf("two services named %s", s.Name)
		}

		names[s.Name] = true

		if _, built := p.Builds[s.Name]; !built && s.Image == "" {
			return fmt.Errorf("service %s: no image, and nothing builds it", s.Name)
		}
	}

	for name := range p.Builds {
		if !names[name] {
			return fmt.Errorf("a build for %s, which isn't a service", name)
		}
	}

	for _, in := range p.Ingress {
		if !names[in.Service] {
			return fmt.Errorf("ingress %s: no service %s", in.Name, in.Service)
		}
	}

	return nil
}

// built is the services built, in order.
func built(p *Plan) []string {
	out := make([]string, 0, len(p.Builds))

	for name := range p.Builds {
		out = append(out, name)
	}

	sort.Strings(out)

	return out
}

// pull is an image pulled once, for the first service using it, waiting as long as the longest wait.
type pull struct {
	service string
	image   string
	wait    int
}

// pulls is every image that isn't built, once each, in the order of the services using them.
func pulls(p *Plan) []*pull {
	var out []*pull

	byImage := map[string]*pull{}

	for _, s := range p.Services {
		if _, built := p.Builds[s.Name]; built || s.Image == "" {
			continue
		}

		if seen := byImage[s.Image]; seen != nil {
			seen.wait = max(seen.wait, int(s.WaitSeconds))

			continue
		}

		byImage[s.Image] = &pull{service: s.Name, image: s.Image, wait: int(s.WaitSeconds)}
		out = append(out, byImage[s.Image])
	}

	return out
}

// inside is a repository path under root, or an error when it would climb out.
func inside(root, path string) (string, error) {
	if strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("build path %q must stay inside the repository", path)
	}

	var out []string

	for _, segment := range strings.Split(path, "/") {
		switch segment {
		case "", ".":
			continue

		case "..":
			if len(out) == 0 {
				return "", fmt.Errorf("build path %q must stay inside the repository", path)
			}

			out = out[:len(out)-1]

		default:
			out = append(out, segment)
		}
	}

	if len(out) == 0 {
		return root, nil
	}

	return root + "/" + strings.Join(out, "/"), nil
}
