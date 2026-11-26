package dockerup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// StartWait is how long, in seconds, the containers get to be running and healthy, one-shots done.
const StartWait = 900

// VolumeHelperImage packs and unpacks volumes: curl and tar. The server's volumeHelperImage.
const VolumeHelperImage = "curlimages/curl@sha256:935d9100e9ba842cdb060de42472c7ca90cfe9a7c96e4dacb55e79e560b3ff40"

// Cmd is one command: docker, or tar to unpack the source.
type Cmd struct {
	Name string
	Args []string

	// Added to this process's environment.
	Env   []string
	Stdin io.Reader

	// Gets what it prints, stdout and stderr; nothing when nil.
	Out io.Writer
}

// Runner runs a command to its end.
type Runner func(ctx context.Context, c Cmd) error

// Exec runs a command on this machine.
func Exec(ctx context.Context, c Cmd) error {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.Stdin = c.Stdin
	cmd.Stdout, cmd.Stderr = c.Out, c.Out
	cmd.WaitDelay = 5 * time.Second

	return cmd.Run()
}

// Kept is a registry Stagd keeps built images in, for compute whose instances don't outlive a sleep: a build kept
// for the commit is pulled back rather than built, and what's built is pushed there once the preview runs.
type Kept struct {
	Host     string
	Username string
	Password string

	// Each build's image there, by service.
	Images map[string]string

	// Build again rather than pull back, keeping what's built.
	Rebuild bool
}

// Options are how a plan runs here.
type Options struct {
	// Where the state, the logs, the compose file and the source go.
	Dir string

	// The compose project.
	Project string

	// A Docker host other previews share: nothing published, each service an alias on the project's network.
	Shared bool

	// The source in a directory here, rather than fetched from the plan's.
	Source string

	Kept *Kept

	Getenv func(string) string
	Run    Runner
	HTTP   *http.Client

	// How many builds at once; at least 2.
	CPUs int

	// How long between tries of a pull that waits for its image.
	PullRetry time.Duration
	Now       func() time.Time

	// Told each new state line as it's written: starting, progress …, running or failed ….
	OnState func(line string)
}

func (o Options) withDefaults() Options {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}

	if o.Run == nil {
		o.Run = Exec
	}

	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 10 * time.Minute}
	}

	if o.CPUs == 0 {
		o.CPUs = runtime.NumCPU()
	}

	o.CPUs = max(o.CPUs, 2)

	if o.PullRetry == 0 {
		o.PullRetry = 10 * time.Second
	}

	if o.Now == nil {
		o.Now = time.Now
	}

	return o
}

// failure is what went wrong, in words a person can act on: the state says it.
type failure struct{ msg string }

func (f failure) Error() string { return f.msg }

func fail(format string, args ...any) error {
	return failure{msg: fmt.Sprintf(format, args...)}
}

// stage is the state file, rewritten whole each time: the state, then every time marked so far.
type stage struct {
	path    string
	now     func() time.Time
	onState func(string)

	mu    sync.Mutex
	line  string
	marks strings.Builder
}

func (s *stage) mark(what, part string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fmt.Fprintf(&s.marks, "%d %s %s\n", s.now().UnixMilli(), what, part)
	s.write()
}

func (s *stage) state(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// One line: the first is what's read as the state.
	line := strings.ReplaceAll(fmt.Sprintf(format, args...), "\n", " ")
	changed := line != s.line

	s.line = line
	s.write()

	if changed && s.onState != nil {
		s.onState(line)
	}
}

func (s *stage) write() {
	next := s.path + ".next"

	if err := os.WriteFile(next, []byte(s.line+"\n"+s.marks.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "writing the state:", err)

		return
	}

	if err := os.Rename(next, s.path); err != nil {
		fmt.Fprintln(os.Stderr, "writing the state:", err)
	}
}

// up is one run of a plan.
type up struct {
	plan *Plan
	o    Options
	s    *stage

	// DOCKER_CONFIG for pulls and builds, when there are registries to log in to.
	auth []string

	// The kept images' registry, logged in to.
	keptConfig string
	keptOK     bool

	mu   sync.Mutex
	keep map[string]bool
}

// Up brings the plan up: fetches the source, builds and pulls side by side, puts kept volumes back and starts the
// containers, saying where it stands in <dir>/state. Stopped (ctx), it leaves the state as it was: whatever stopped
// it says what's next.
func Up(ctx context.Context, p *Plan, o Options) error {
	o = o.withDefaults()

	dir, err := filepath.Abs(o.Dir)

	if err != nil {
		return err
	}

	o.Dir = dir

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	s := &stage{path: filepath.Join(dir, "state"), now: o.Now, onState: o.OnState}
	u := &up{plan: p, o: o, s: s, keep: map[string]bool{}}

	s.mark("begin", "script")
	s.state("starting")

	err = u.run(ctx)

	if ctx.Err() != nil {
		return ctx.Err()
	}

	if err != nil {
		var f failure

		if !errors.As(err, &f) {
			f = failure{msg: "Something went wrong bringing it up: " + err.Error()}
		}

		s.mark("end", "script")
		s.state("failed %s", f.msg)

		return f
	}

	s.mark("end", "script")
	s.state("running")

	u.keepBuilt(ctx)

	return nil
}

func (u *up) path(name string) string {
	return filepath.Join(u.o.Dir, name)
}

// docker runs docker, what it prints going to the log named (in the stage's directory), when one is.
func (u *up) docker(ctx context.Context, log string, env []string, stdin io.Reader, args ...string) error {
	var out io.Writer

	if log != "" {
		f, err := os.Create(u.path(log))

		if err != nil {
			return err
		}

		defer f.Close()

		out = f
	}

	return u.o.Run(ctx, Cmd{Name: "docker", Args: args, Env: env, Stdin: stdin, Out: out})
}

// tail is a log's last 8 lines as one, at most 600 characters.
func (u *up) tail(log string) string {
	data, _ := os.ReadFile(u.path(log))
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")

	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}

	text := strings.Join(lines, " ")

	if utf8.RuneCountInString(text) > 600 {
		text = string([]rune(text)[:600])
	}

	return text
}

func (u *up) run(ctx context.Context) error {
	p, o := u.plan, u.o

	compose, err := Compose(p, o.Project, o.Shared)

	if err != nil {
		return err
	}

	if err := os.WriteFile(u.path("compose.yaml"), compose, 0o644); err != nil {
		return err
	}

	// The builds' sources: inside the repository, checked before anything starts.
	src := u.path("src")

	if o.Source != "" {
		if src, err = filepath.Abs(o.Source); err != nil {
			return err
		}
	}

	builds := map[string][]string{}

	for _, name := range built(p) {
		args, err := u.buildArgs(src, name)

		if err != nil {
			return failure{msg: err.Error()}
		}

		builds[name] = args
	}

	// Whatever runs in the background stops with the first failure.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := u.login(ctx); err != nil {
		return err
	}

	pulls := u.startPulls(ctx)

	if len(builds) > 0 {
		if err := u.source(ctx, src); err != nil {
			return err
		}

		u.keptLogin(ctx)

		if err := u.build(ctx, builds); err != nil {
			return err
		}
	}

	if err := u.awaitPulls(ctx, pulls); err != nil {
		return err
	}

	if len(u.auth) > 0 {
		os.RemoveAll(u.path("docker-auth"))
	}

	if err := u.restore(ctx); err != nil {
		return err
	}

	u.s.state("progress Starting the containers")
	u.s.mark("begin", "start")

	if err := u.docker(ctx, "up.log", nil, nil, "compose", "-f", u.path("compose.yaml"), "up", "-d", "--remove-orphans", "--wait", "--wait-timeout", fmt.Sprint(StartWait)); err != nil {
		return fail("Starting the containers failed: %s", u.tail("up.log"))
	}

	u.s.mark("end", "start")

	return nil
}

// login logs in to the plan's registries, in a Docker config of the preview's own: builds may pull private base
// images too. It's removed once the images are pulled.
func (u *up) login(ctx context.Context) error {
	if len(u.plan.Registries) == 0 {
		return nil
	}

	config := u.path("docker-auth")

	os.RemoveAll(config)

	if err := os.MkdirAll(config, 0o700); err != nil {
		return err
	}

	u.auth = []string{"DOCKER_CONFIG=" + config}
	u.s.mark("begin", "login")

	for _, r := range u.plan.Registries {
		password := strings.NewReader(u.o.Getenv(r.PasswordEnv))

		if err := u.docker(ctx, "login.log", u.auth, password, "login", r.Host, "-u", r.Username, "--password-stdin"); err != nil {
			return fail("Couldn't log in to %s: %s", r.Host, u.tail("login.log"))
		}
	}

	u.s.mark("end", "login")

	return nil
}

// pulling is a pull in the background.
type pulling struct {
	*pull
	waiting atomic.Bool
	done    chan struct{}
	err     error
}

// startPulls pulls the prebuilt images in the background, while the source is fetched and the builds run, rather
// than leave them to compose after the builds. A pull that waits retries until CI has pushed its image.
func (u *up) startPulls(ctx context.Context) []*pulling {
	var out []*pulling

	for _, pl := range pulls(u.plan) {
		job := &pulling{pull: pl, done: make(chan struct{})}
		out = append(out, job)

		go func() {
			defer close(job.done)

			part := "pull-" + job.service
			u.s.mark("begin", part)
			job.err = u.pullImage(ctx, job)
			u.s.mark("end", part)
		}()
	}

	return out
}

func (u *up) pullImage(ctx context.Context, job *pulling) error {
	end := u.o.Now().Add(time.Duration(job.wait) * time.Second)

	for {
		err := u.docker(ctx, "pull-"+job.service+".log", u.auth, nil, "pull", "-q", job.image)

		if err == nil || job.wait == 0 || !u.o.Now().Before(end) || ctx.Err() != nil {
			return err
		}

		job.waiting.Store(true)

		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-time.After(u.o.PullRetry):
		}
	}
}

// awaitPulls waits for the pulls still going once the builds are done, saying what each is doing.
func (u *up) awaitPulls(ctx context.Context, pulls []*pulling) error {
	for _, job := range pulls {
		for waiting := true; waiting; {
			if job.waiting.Load() {
				u.s.state("progress %s: waiting for %s", job.service, job.image)
			} else {
				u.s.state("progress %s: pulling %s", job.service, job.image)
			}

			select {
			case <-job.done:
				waiting = false

			case <-ctx.Done():
				return ctx.Err()

			case <-time.After(time.Second):
			}
		}

		if job.err != nil {
			after := ""

			if job.wait > 0 {
				after = fmt.Sprintf(" Waited %s: is CI pushing it?", shortDuration(job.wait))
			}

			return fail("%s: couldn't pull %s.%s %s", job.service, job.image, after, u.tail("pull-"+job.service+".log"))
		}
	}

	return nil
}

// shortDuration is a wait as stagd.yaml writes it: 20m, 1h, 1h30m.
func shortDuration(seconds int) string {
	h, m := seconds/3600, seconds/60%60

	switch {
	case h == 0:
		return fmt.Sprintf("%dm", m)

	case m == 0:
		return fmt.Sprintf("%dh", h)
	}

	return fmt.Sprintf("%dh%dm", h, m)
}

// source fetches the repository at the preview's commit into dir, unless it's here already (Options.Source).
func (u *up) source(ctx context.Context, dir string) error {
	if u.o.Source != "" {
		return nil
	}

	if u.plan.Source == nil {
		return fail("there's no source to build from")
	}

	u.s.state("progress Fetching the source")
	u.s.mark("begin", "source")

	os.RemoveAll(dir)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.plan.Source.Url, nil)

	if err != nil {
		return fail("Couldn't fetch the source at this commit")
	}

	if token := u.o.Getenv(u.plan.Source.TokenEnv); u.plan.Source.TokenEnv != "" && token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	res, err := u.o.HTTP.Do(req)

	if err != nil {
		return fail("Couldn't fetch the source at this commit")
	}

	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fail("Couldn't fetch the source at this commit")
	}

	if err := u.o.Run(ctx, Cmd{Name: "tar", Args: []string{"-xzf", "-", "-C", dir, "--strip-components=1"}, Stdin: res.Body}); err != nil {
		return fail("Couldn't unpack the source")
	}

	u.s.mark("end", "source")

	return nil
}

// buildArgs is docker build's arguments for a service.
func (u *up) buildArgs(src, name string) ([]string, error) {
	b := u.plan.Builds[name]
	// --load: where the default builder isn't the engine's own (Docker Desktop's), the image is still loaded into it.
	args := []string{"build", "--load", "-t", BuiltImage(u.o.Project, name)}

	if b.Dockerfile != "" {
		if strings.HasPrefix(b.Dockerfile, "/") {
			return nil, fmt.Errorf("dockerfile %q must stay inside the repository", b.Dockerfile)
		}

		dockerfile, err := inside(src, b.Context+"/"+b.Dockerfile)

		if err != nil {
			return nil, err
		}

		args = append(args, "-f", dockerfile)
	}

	if b.Target != "" {
		args = append(args, "--target", b.Target)
	}

	keys := make([]string, 0, len(b.Args))

	for k := range b.Args {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, k := range keys {
		args = append(args, "--build-arg", k+"="+b.Args[k])
	}

	context, err := inside(src, b.Context)

	if err != nil {
		return nil, err
	}

	return append(args, context), nil
}

// keptLogin logs in to where built images are kept, in a Docker config of its own: a login that fails only means
// building, never a failed deploy.
func (u *up) keptLogin(ctx context.Context) {
	k := u.o.Kept

	if k == nil {
		return
	}

	u.keptConfig = u.path("kept-auth")

	os.RemoveAll(u.keptConfig)

	if err := os.MkdirAll(u.keptConfig, 0o700); err != nil {
		return
	}

	u.keptOK = u.docker(ctx, "kept-login.log", nil, strings.NewReader(k.Password), "--config", u.keptConfig, "login", k.Host, "-u", k.Username, "--password-stdin") == nil
}

// build runs the builds side by side, as many at once as there are CPUs; the first to fail stops the rest. The
// progress names every build still running: "api, web: building".
func (u *up) build(ctx context.Context, builds map[string][]string) error {
	type result struct {
		name string
		err  error
	}

	queue := built(u.plan)
	done := make(chan result)

	var running []string

	progress := func() {
		if len(running) > 0 {
			u.s.state("progress %s: building", strings.Join(running, ", "))
		}
	}

	for len(queue) > 0 || len(running) > 0 {
		for len(queue) > 0 && len(running) < u.o.CPUs {
			name := queue[0]
			queue = queue[1:]
			running = append(running, name)

			go func() {
				part := "build-" + name
				u.s.mark("begin", part)
				err := u.buildOne(ctx, name, builds[name])
				u.s.mark("end", part)

				done <- result{name, err}
			}()
		}

		progress()

		r := <-done

		for i, name := range running {
			if name == r.name {
				running = append(running[:i], running[i+1:]...)

				break
			}
		}

		if r.err != nil {
			// The rest stop with the context; their results go nowhere.
			go func() {
				for range running {
					<-done
				}
			}()

			if ctx.Err() != nil {
				return ctx.Err()
			}

			return fail("%s: the build failed: %s", r.name, u.tail("build-"+r.name+".log"))
		}
	}

	return nil
}

// buildOne builds a service, or pulls back the image kept for this commit when there is one.
func (u *up) buildOne(ctx context.Context, name string, args []string) error {
	log := "build-" + name + ".log"
	built := BuiltImage(u.o.Project, name)
	kept := ""

	if u.o.Kept != nil {
		kept = u.o.Kept.Images[name]
	}

	if kept != "" && u.keptOK && !u.o.Kept.Rebuild {
		if u.docker(ctx, "", nil, nil, "--config", u.keptConfig, "pull", "-q", kept) == nil && u.docker(ctx, "", nil, nil, "tag", kept, built) == nil {
			return os.WriteFile(u.path(log), []byte("The image built before for this commit, pulled back: "+kept+"\n"), 0o644)
		}
	}

	if err := u.docker(ctx, log, u.auth, nil, args...); err != nil {
		return err
	}

	if kept != "" {
		u.mu.Lock()
		u.keep[name] = true
		u.mu.Unlock()
	}

	return nil
}

// restore puts back the volumes kept while it slept, before anything starts, with compose's labels so it takes them
// as its own. Each link is in the environment, never in an argument.
func (u *up) restore(ctx context.Context) error {
	for i, r := range u.plan.Restore {
		part := fmt.Sprintf("restore-%d", i+1)
		log := part + ".log"
		volume := u.o.Project + "_" + r.Volume

		u.s.state("progress Putting back %s's data, kept while it slept", r.Volume)
		u.s.mark("begin", part)

		_ = u.docker(ctx, "", nil, nil, "volume", "create", "--label", "com.docker.compose.project="+u.o.Project, "--label", "com.docker.compose.volume="+r.Volume, volume)

		env := []string{"STAGD_VOLUME_URL=" + u.o.Getenv(r.UrlEnv)}
		unpack := `curl -fsS "$STAGD_VOLUME_URL" | tar xzf - -C /v`

		if err := u.docker(ctx, log, env, nil, "run", "--rm", "--user", "0", "-v", volume+":/v", "-e", "STAGD_VOLUME_URL", VolumeHelperImage, "sh", "-c", unpack); err != nil {
			return fail("Couldn't put back %s's data: %s", r.Volume, u.tail(log))
		}

		u.s.mark("end", part)
	}

	return nil
}

// keepBuilt pushes what was built to where it's kept, for the next instance, once the preview runs: one at a time.
func (u *up) keepBuilt(ctx context.Context) {
	if u.o.Kept == nil {
		return
	}

	defer os.RemoveAll(u.keptConfig)

	if !u.keptOK {
		return
	}

	for _, name := range built(u.plan) {
		kept := u.o.Kept.Images[name]

		if !u.keep[name] || kept == "" {
			continue
		}

		if u.docker(ctx, "", nil, nil, "tag", BuiltImage(u.o.Project, name), kept) == nil {
			_ = u.docker(ctx, "keep-"+name+".log", nil, nil, "--config", u.keptConfig, "push", "-q", kept)
		}
	}
}
