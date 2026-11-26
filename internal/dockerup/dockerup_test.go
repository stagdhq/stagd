package dockerup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const samplePlan = `{
  "version": 1,
  "services": [
    {"name": "web", "env": {"API_URL": "http://api:8080", "PRICE": "$5"}, "secretEnv": {"SESSION_SECRET": "STAGD_SECRET_1"}, "ports": [3000], "dependsOn": ["api"]},
    {"name": "api", "ports": [8080], "dependsOn": ["db", "migrate"], "waitFor": {"db": "healthy", "migrate": "completed"}, "health": {"command": ["curl", "-f", "http://localhost:8080/health"]}},
    {"name": "migrate", "image": "ghcr.io/acme/migrate:1", "oneShot": true, "dependsOn": ["db"], "waitSeconds": 1800},
    {"name": "db", "image": "postgres:17", "ports": [5432], "mounts": [{"volume": "pgdata", "path": "/var/lib/postgresql/data"}], "health": {"command": ["pg_isready"], "startSeconds": 1800}}
  ],
  "volumes": [{"name": "pgdata"}],
  "ingress": [{"name": "web", "service": "web", "port": 3000, "primary": true}, {"name": "api", "service": "api", "port": 8080}],
  "builds": {"web": {"context": ".", "dockerfile": "docker/web.Dockerfile", "args": {"B": "2", "A": "1"}}, "api": {"context": "api", "target": "prod"}},
  "source": {"url": "https://example.test/source.tar.gz", "tokenEnv": "STAGD_SOURCE_TOKEN"},
  "registries": [{"host": "ghcr.io", "username": "acme", "passwordEnv": "STAGD_REGISTRY_PASSWORD_1"}]
}`

func TestParse(t *testing.T) {
	p, err := Parse([]byte(samplePlan))

	if err != nil {
		t.Fatal(err)
	}

	if len(p.Services) != 4 || p.Builds["web"].Dockerfile != "docker/web.Dockerfile" || p.Services[0].SecretEnv["SESSION_SECRET"] != "STAGD_SECRET_1" {
		t.Fatalf("read as %v", p)
	}

	for name, plan := range map[string]string{
		"a newer version":         `{"version": 2, "services": [{"name": "web", "image": "nginx"}]}`,
		"a field it can't read":   `{"version": 1, "services": [{"name": "web", "image": "nginx", "gpu": true}]}`,
		"no services":             `{"version": 1}`,
		"a name twice":            `{"version": 1, "services": [{"name": "web", "image": "a"}, {"name": "web", "image": "b"}]}`,
		"a name with a slash":     `{"version": 1, "services": [{"name": "../web", "image": "a"}]}`,
		"nothing to run":          `{"version": 1, "services": [{"name": "web"}]}`,
		"a secret in no variable": `{"version": 1, "services": [{"name": "web", "image": "a", "secretEnv": {"K": "${X}"}}]}`,
		"a build of no service":   `{"version": 1, "services": [{"name": "web", "image": "a"}], "builds": {"api": {"context": "."}}}`,
		"an ingress to nothing":   `{"version": 1, "services": [{"name": "web", "image": "a"}], "ingress": [{"name": "web", "service": "api", "port": 80}]}`,
		"a wait it can't do":      `{"version": 1, "services": [{"name": "web", "image": "a", "waitFor": {"db": "ready"}}]}`,
	} {
		if _, err := Parse([]byte(plan)); err == nil {
			t.Errorf("%s: read", name)
		}
	}
}

func TestCompose(t *testing.T) {
	p, err := Parse([]byte(samplePlan))

	if err != nil {
		t.Fatal(err)
	}

	out, err := Compose(p, "stagd-preview", false)

	if err != nil {
		t.Fatal(err)
	}

	got := string(out)

	for _, want := range []string{
		"name: stagd-preview\n",
		"image: stagd-build/web:stagd-preview\n",
		"PRICE: $$5\n",
		"SESSION_SECRET: ${STAGD_SECRET_1}\n",
		"- 18000:3000\n",
		"- 18001:8080\n",
		"condition: service_healthy\n",
		"condition: service_completed_successfully\n",
		"dev.stagd.oneshot: \"true\"\n",
		"restart: \"no\"\n",
		"- pgdata:/var/lib/postgresql/data\n",
		"start_period: 1800s\n",
		"start_period: 120s\n",
		"start_interval: 1s\n",
		"max-size: 10m\n",
		"volumes:\n  pgdata: {}\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}

	shared, _ := Compose(p, "stagd-abc", true)

	if strings.Contains(string(shared), "18000") || !strings.Contains(string(shared), "- web.stagd-abc\n") {
		t.Errorf("shared:\n%s", shared)
	}
}

// fakeDocker stands in for docker: each call logged; builds take a moment, and fail for the services in failing;
// pulls of the images in late fail until they've been tried that many times. tar runs for real.
type fakeDocker struct {
	mu       sync.Mutex
	calls    []string
	failing  map[string]bool
	late     map[string]int
	tries    map[string]int
	kept     map[string]bool
	stdin    map[string]string
	building int
	most     int
}

func (f *fakeDocker) run(ctx context.Context, c Cmd) error {
	if c.Name == "tar" {
		return Exec(ctx, c)
	}

	line := strings.Join(c.Args, " ")

	if c.Stdin != nil {
		data, _ := io.ReadAll(c.Stdin)

		f.mu.Lock()
		f.stdin[line] = string(data)
		f.mu.Unlock()
	}

	f.mu.Lock()
	f.calls = append(f.calls, line)
	f.mu.Unlock()

	args := c.Args

	if args[0] == "--config" {
		args = args[2:]
	}

	switch args[0] {
	case "build":
		f.mu.Lock()
		f.building++
		f.most = max(f.most, f.building)
		f.mu.Unlock()

		defer func() {
			f.mu.Lock()
			f.building--
			f.mu.Unlock()
		}()

		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-time.After(50 * time.Millisecond):
		}

		image := ""

		for i, a := range args {
			if a == "-t" {
				image = args[i+1]
			}
		}

		for service := range f.failing {
			if strings.HasPrefix(image, "stagd-build/"+service+":") {
				fmt.Fprintln(c.Out, "step 3/7")
				fmt.Fprintln(c.Out, "npm ERR! broken")

				return errors.New("exit status 1")
			}
		}

	case "pull":
		image := args[len(args)-1]

		f.mu.Lock()
		f.tries[image]++
		tries := f.tries[image]
		f.mu.Unlock()

		if c.Args[0] == "--config" && !f.kept[image] {
			return errors.New("not found")
		}

		if tries <= f.late[image] {
			fmt.Fprintln(c.Out, "manifest unknown")

			return errors.New("exit status 1")
		}
	}

	return nil
}

// sourceServer serves a gzipped tarball with one top directory, as GitHub's are, wanting the token.
func sourceServer(t *testing.T) *httptest.Server {
	var b bytes.Buffer

	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)

	for name, body := range map[string]string{"acme-web-abc/docker/web.Dockerfile": "FROM scratch\n", "acme-web-abc/api/Dockerfile": "FROM scratch\n"} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))})
		tw.Write([]byte(body))
	}

	tw.Close()
	gz.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer src-token" {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		w.Write(b.Bytes())
	}))

	t.Cleanup(srv.Close)

	return srv
}

func run(t *testing.T, plan string, change func(*Options, *fakeDocker)) (string, *fakeDocker, error) {
	t.Helper()

	srv := sourceServer(t)
	p, err := Parse([]byte(strings.Replace(plan, "https://example.test/source.tar.gz", srv.URL, 1)))

	if err != nil {
		t.Fatal(err)
	}

	f := &fakeDocker{failing: map[string]bool{}, late: map[string]int{}, tries: map[string]int{}, kept: map[string]bool{}, stdin: map[string]string{}}
	env := map[string]string{"STAGD_SOURCE_TOKEN": "src-token", "STAGD_REGISTRY_PASSWORD_1": "reg-pass", "STAGD_KEPT_PASSWORD": "kept-pass"}

	o := Options{
		Dir:       t.TempDir(),
		Project:   "stagd-preview",
		Getenv:    func(k string) string { return env[k] },
		Run:       f.run,
		CPUs:      2,
		PullRetry: 10 * time.Millisecond,
	}

	if change != nil {
		change(&o, f)
	}

	err = Up(context.Background(), p, o)
	state, _ := os.ReadFile(filepath.Join(o.Dir, "state"))

	return string(state), f, err
}

// parts is the parts that began and ended, from the state's times.
func parts(state string) []string {
	begun := map[string]bool{}

	var out []string

	for _, line := range strings.Split(state, "\n")[1:] {
		f := strings.Fields(line)

		if len(f) != 3 {
			continue
		}

		if f[1] == "begin" {
			begun[f[2]] = true
		} else if begun[f[2]] {
			out = append(out, f[2])
		}
	}

	sort.Strings(out)

	return out
}

func TestUp(t *testing.T) {
	state, f, err := run(t, samplePlan, nil)

	if err != nil {
		t.Fatal(err)
	}

	if first := strings.SplitN(state, "\n", 2)[0]; first != "running" {
		t.Fatalf("state %q", first)
	}

	if got, want := strings.Join(parts(state), " "), "build-api build-web login pull-db pull-migrate script source start"; got != want {
		t.Errorf("parts: %s, want %s", got, want)
	}

	// Logged in with the password from the environment, never an argument; the builds as the plan says.
	if f.stdin["login ghcr.io -u acme --password-stdin"] != "reg-pass" {
		t.Errorf("login: %v", f.stdin)
	}

	calls := strings.Join(f.calls, "\n")

	for _, want := range []string{
		"--build-arg A=1 --build-arg B=2",
		"build --load -t stagd-build/api:stagd-preview --target prod ",
		"pull -q postgres:17",
		"compose -f ",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("no %q in\n%s", want, calls)
		}
	}

	if !strings.Contains(calls, "/src/docker/web.Dockerfile") || !strings.Contains(calls, "--wait-timeout 900") {
		t.Errorf("calls:\n%s", calls)
	}

	if f.most != 2 {
		t.Errorf("%d builds at once, want 2", f.most)
	}
}

func TestUpFailedBuild(t *testing.T) {
	state, _, err := run(t, samplePlan, func(_ *Options, f *fakeDocker) { f.failing["web"] = true })

	if err == nil {
		t.Fatal("no error")
	}

	if first := strings.SplitN(state, "\n", 2)[0]; first != "failed web: the build failed: step 3/7 npm ERR! broken" {
		t.Errorf("state %q", first)
	}
}

func TestUpWaitsForCI(t *testing.T) {
	state, f, err := run(t, samplePlan, func(_ *Options, f *fakeDocker) { f.late["ghcr.io/acme/migrate:1"] = 3 })

	if err != nil {
		t.Fatalf("%v: %s", err, state)
	}

	if f.tries["ghcr.io/acme/migrate:1"] != 4 {
		t.Errorf("tried %d times", f.tries["ghcr.io/acme/migrate:1"])
	}

	// One that doesn't wait fails at once, saying why.
	noWait := strings.Replace(samplePlan, `, "waitSeconds": 1800`, "", 1)
	state, _, _ = run(t, noWait, func(_ *Options, f *fakeDocker) { f.late["ghcr.io/acme/migrate:1"] = 1 })

	if first := strings.SplitN(state, "\n", 2)[0]; first != "failed migrate: couldn't pull ghcr.io/acme/migrate:1. manifest unknown" {
		t.Errorf("state %q", first)
	}
}

func TestUpKept(t *testing.T) {
	kept := func(rebuild bool) func(*Options, *fakeDocker) {
		return func(o *Options, f *fakeDocker) {
			o.Kept = &Kept{Host: "nscr.io", Username: "token", Password: "kept-pass", Images: map[string]string{"web": "nscr.io/ws/stagd-build:aaa", "api": "nscr.io/ws/stagd-build:bbb"}, Rebuild: rebuild}
			f.kept["nscr.io/ws/stagd-build:aaa"] = true
		}
	}

	_, f, err := run(t, samplePlan, kept(false))

	if err != nil {
		t.Fatal(err)
	}

	calls := strings.Join(f.calls, "\n")

	// web's pulled back, api's built and kept once it runs.
	if strings.Contains(calls, "build --load -t stagd-build/web:") || !strings.Contains(calls, "tag nscr.io/ws/stagd-build:aaa stagd-build/web:stagd-preview") {
		t.Errorf("web built:\n%s", calls)
	}

	if !strings.Contains(calls, "build --load -t stagd-build/api:") || !strings.HasSuffix(calls, "push -q nscr.io/ws/stagd-build:bbb") {
		t.Errorf("api not built and kept:\n%s", calls)
	}

	_, f, _ = run(t, samplePlan, kept(true))

	if calls := strings.Join(f.calls, "\n"); !strings.Contains(calls, "build --load -t stagd-build/web:") || !strings.Contains(calls, "push -q nscr.io/ws/stagd-build:aaa") {
		t.Errorf("rebuild:\n%s", calls)
	}
}

func TestUpLocalSource(t *testing.T) {
	src := t.TempDir()
	plan := strings.Replace(samplePlan, `"source": {"url": "https://example.test/source.tar.gz", "tokenEnv": "STAGD_SOURCE_TOKEN"},`, "", 1)

	state, f, err := run(t, plan, func(o *Options, _ *fakeDocker) { o.Source = src })

	if err != nil {
		t.Fatalf("%v: %s", err, state)
	}

	if calls := strings.Join(f.calls, "\n"); !strings.Contains(calls, src+"/docker/web.Dockerfile") {
		t.Errorf("not built from %s:\n%s", src, calls)
	}

	if strings.Contains(strings.Join(parts(state), " "), "source") {
		t.Errorf("fetched a source: %s", state)
	}
}

func TestInside(t *testing.T) {
	for path, want := range map[string]string{".": "/s", "./api": "/s/api", "preview/../docker": "/s/docker", "..": "", "/etc": "", "a/../../b": ""} {
		got, err := inside("/s", path)

		if (err != nil) != (want == "") || got != want {
			t.Errorf("inside(%q) = %q, %v", path, got, err)
		}
	}
}
