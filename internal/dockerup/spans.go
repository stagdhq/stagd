package dockerup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Span is one part of bringing the plan up, for the deploy's trace: written to the state as a line
//
//	span {"key": "build-web", "name": "build", "start": 1795720715945, "end": 1795720719233, ...}
//
// after the times, once it has ended. Stagd forwards them under the deploy's instance span. Its key is unique in a
// run and its parent is another's key, or none for a part of the run itself. Times are ms since the epoch.
//
// Only the preview's names and categories go in one: never a command, a Dockerfile's text, a log line or a link.
type Span struct {
	Key        string         `json:"key"`
	Parent     string         `json:"parent,omitempty"`
	Name       string         `json:"name"`
	Start      int64          `json:"start"`
	End        int64          `json:"end"`
	Attributes map[string]any `json:"attributes,omitempty"`

	// Failed: a category, never a message.
	Error string `json:"error,omitempty"`
}

// span records a span that has ended.
func (s *stage) span(sp Span) {
	line, err := json.Marshal(sp)

	if err != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.spans.WriteString("span ")
	s.spans.Write(line)
	s.spans.WriteByte('\n')
	s.write()
}

// ms is a time as a span's.
func ms(t time.Time) int64 {
	return t.UnixMilli()
}

// failed is a span's error for an err: "failed", or nothing.
func failed(err error) string {
	if err != nil {
		return "failed"
	}

	return ""
}

// lines is a writer that hands each whole line it's written to fn, as well as passing everything on.
type lines struct {
	out io.Writer
	fn  func(line string)

	mu  sync.Mutex
	buf []byte
}

func (l *lines) Write(p []byte) (int, error) {
	l.mu.Lock()
	l.buf = append(l.buf, p...)

	for {
		i := bytes.IndexByte(l.buf, '\n')

		if i < 0 {
			break
		}

		line := string(l.buf[:i])
		l.buf = l.buf[i+1:]
		l.fn(line)
	}

	l.mu.Unlock()

	if l.out == nil {
		return len(p), nil
	}

	return l.out.Write(p)
}

var (
	// A BuildKit step in --progress=plain: "#5 [2/4] RUN npm ci", "#5 [builder 3/7] COPY . .", "#1 [internal] load build definition".
	stepHead = regexp.MustCompile(`^#(\d+) (\[([^\]]*)\]|\S)`)

	// Its end: "#5 DONE 12.3s", "#4 CACHED", "#5 ERROR: …", "#5 CANCELED".
	stepEnd = regexp.MustCompile(`^#(\d+) (DONE [0-9.]+s|CACHED|ERROR|CANCELED)`)

	// A step's place in its stage, from its brackets: "2/4" of "[builder 2/4]".
	stepPlace = regexp.MustCompile(`(\d+/\d+)$`)

	// A step's log line, "#5 0.512 added 312 packages": not its head.
	stepLog = regexp.MustCompile(`^#\d+ \d+\.\d+ `)
)

// steps reads a build's plain progress as it's written, a span for each step under the build's: which step of its
// stage (2/4), or what BuildKit does around them (internal, export), and whether it was cached. Never the step's text.
type steps struct {
	parent  string
	service string
	now     func() time.Time
	span    func(Span)

	open  map[string]*Span
	ended map[string]bool
}

func newSteps(parent, service string, now func() time.Time, span func(Span)) *steps {
	return &steps{parent: parent, service: service, now: now, span: span, open: map[string]*Span{}, ended: map[string]bool{}}
}

func (st *steps) line(line string) {
	if m := stepEnd.FindStringSubmatch(line); m != nil {
		sp := st.open[m[1]]

		if sp == nil {
			return
		}

		delete(st.open, m[1])
		st.ended[m[1]] = true

		sp.End = ms(st.now())
		sp.Attributes["stagd.cached"] = m[2] == "CACHED"

		// BuildKit says how long it took ("DONE 12.3s"): what's printed can come late, in bursts, so that wins over
		// when the lines were read.
		if took, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimPrefix(m[2], "DONE "), "s"), 64); err == nil && strings.HasPrefix(m[2], "DONE ") {
			sp.Start = min(sp.Start, sp.End-int64(took*1000))
		}

		if strings.HasPrefix(m[2], "ERROR") {
			sp.Error = "failed"
		}

		st.span(*sp)

		return
	}

	if stepLog.MatchString(line) {
		return
	}

	m := stepHead.FindStringSubmatch(line)

	// Once: BuildKit names a step again when its output resumes after another's. #0 is the builder, not a step.
	if m == nil || m[1] == "0" || st.open[m[1]] != nil || st.ended[m[1]] {
		return
	}

	step := "export"

	switch {
	case m[3] == "internal":
		step = "internal"

	case m[2][0] == '[':
		if p := stepPlace.FindString(m[3]); p != "" {
			step = p
		}
	}

	now := ms(st.now())

	st.open[m[1]] = &Span{
		Key:        st.parent + " step " + m[1],
		Parent:     st.parent,
		Name:       "step",
		Start:      now,
		Attributes: map[string]any{"stagd.service": st.service, "stagd.step": step},
	}
}

// starts follows Docker's events while the containers start: a span for each service from its container made to
// it being ready, healthy where it has a health check, done where it's a one-shot.
type starts struct {
	plan    *Plan
	project string
	span    func(Span)

	mu      sync.Mutex
	created map[string]int64
	done    map[string]bool
}

func newStarts(p *Plan, project string, span func(Span)) *starts {
	return &starts{plan: p, project: project, span: span, created: map[string]int64{}, done: map[string]bool{}}
}

// dockerEvent is one of `docker events --format '{{json .}}'`.
type dockerEvent struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
	TimeNano int64 `json:"timeNano"`
}

func (st *starts) line(line string) {
	var e dockerEvent

	if json.Unmarshal([]byte(line), &e) != nil || e.Type != "container" || e.Actor.Attributes["com.docker.compose.project"] != st.project {
		return
	}

	service := e.Actor.Attributes["com.docker.compose.service"]
	at := time.Unix(0, e.TimeNano)

	var health, oneShot, known bool

	for _, s := range st.plan.Services {
		if s.Name == service {
			known, health, oneShot = true, s.Health != nil && len(s.Health.Command) > 0, s.OneShot
		}
	}

	if !known {
		return
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	if st.done[service] {
		return
	}

	if st.created[service] == 0 && (e.Action == "create" || e.Action == "start") {
		st.created[service] = ms(at)
	}

	// Ready: started, where there's nothing more to wait for; healthy, where there's a check; exited 0, for a one-shot.
	switch {
	case e.Action == "start" && !health && !oneShot:
		st.end(service, at, "")

	case e.Action == "health_status: healthy" && !oneShot:
		st.end(service, at, "")

	case e.Action == "health_status: unhealthy" && !oneShot:
		st.end(service, at, "unhealthy")

	case e.Action == "die" && oneShot:
		why := ""

		if e.Actor.Attributes["exitCode"] != "0" {
			why = "failed"
		}

		st.end(service, at, why)
	}
}

// end records a service's start; the lock is held.
func (st *starts) end(service string, at time.Time, why string) {
	start := st.created[service]

	if start == 0 {
		return
	}

	st.done[service] = true

	st.span(Span{
		Key:        "start-" + service,
		Parent:     "start",
		Name:       "service_start",
		Start:      start,
		End:        max(ms(at), start),
		Attributes: map[string]any{"stagd.service": service},
		Error:      why,
	})
}

// follow hands Docker's container events since the time given to the starts, until ctx is done: from a moment
// before, so none is missed while it attaches.
func (u *up) follow(ctx context.Context, since time.Time, st *starts) {
	w := &lines{fn: st.line}
	from := fmt.Sprintf("%d.%09d", since.Unix(), since.Nanosecond())

	_ = u.o.Run(ctx, Cmd{
		Name: "docker",
		Args: []string{"events", "--since", from, "--filter", "type=container", "--filter", "label=com.docker.compose.project=" + u.o.Project, "--format", "{{json .}}"},
		Out:  w,
	})
}
