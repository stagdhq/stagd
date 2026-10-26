package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

// A releases server like GitHub's: /latest redirects to the newest tag,
// /download/<tag>/ has the archive for this machine and its checksums.
func fakeReleases(t *testing.T, tag string, binary []byte, tamper bool) {
	t.Helper()

	var archive bytes.Buffer

	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "stagd", Mode: 0o755, Size: int64(len(binary))})
	tw.Write(binary)
	tw.Close()
	gz.Close()

	asset := fmt.Sprintf("stagd_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(archive.Bytes())
	sums := hex.EncodeToString(sum[:]) + "  " + asset + "\n"

	served := archive.Bytes()
	if tamper {
		served = append(append([]byte{}, served...), "tampered"...)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("HEAD /stagdhq/stagd/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/stagdhq/stagd/releases/tag/"+tag)
		w.WriteHeader(http.StatusFound)
	})

	mux.HandleFunc("GET /stagdhq/stagd/releases/download/"+tag+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
		w.Write(served)
	})

	mux.HandleFunc("GET /stagdhq/stagd/releases/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sums)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	old := releasesURL
	releasesURL = srv.URL + "/stagdhq/stagd/releases"

	t.Cleanup(func() { releasesURL = old })
}

// runAs runs stagd as a given version, its binary at exe.
func runAs(t *testing.T, version, exe string, args ...string) (string, error) {
	t.Helper()

	old := executable
	executable = func() (string, error) { return exe, nil }

	t.Cleanup(func() { executable = old })

	var out bytes.Buffer

	cmd := New(version)
	cmd.Writer, cmd.ErrWriter = &out, &bytes.Buffer{}
	cmd.ExitErrHandler = func(_ context.Context, _ *cli.Command, _ error) {}

	err := cmd.Run(context.Background(), append([]string{"stagd"}, args...))

	return out.String(), err
}

func TestUpdate(t *testing.T) {
	fakeReleases(t, "v0.3.0", []byte("the new stagd"), false)

	exe := filepath.Join(t.TempDir(), "stagd")
	os.WriteFile(exe, []byte("the old stagd"), 0o755)

	// Only saying.
	if out, err := runAs(t, "v0.2.0", exe, "update", "--check"); err != nil || !strings.Contains(out, "stagd v0.3.0 is out") {
		t.Fatalf("check: %v %q", err, out)
	}

	if got, _ := os.ReadFile(exe); string(got) != "the old stagd" {
		t.Fatal("--check changed the binary")
	}

	// The latest already: nothing to do.
	if out, err := runAs(t, "v0.3.0", exe, "update"); err != nil || !strings.Contains(out, "is the latest") {
		t.Fatalf("latest: %v %q", err, out)
	}

	// Older: replaced, executable.
	if out, err := runAs(t, "v0.2.0", exe, "update"); err != nil || !strings.Contains(out, "Updated stagd v0.2.0 to v0.3.0") {
		t.Fatalf("update: %v %q", err, out)
	}

	got, _ := os.ReadFile(exe)
	info, _ := os.Stat(exe)

	if string(got) != "the new stagd" || info.Mode().Perm() != 0o755 {
		t.Fatalf("after: %q %v", got, info.Mode())
	}

	// A development build counts as older.
	if out, _ := runAs(t, "v0.2.0-11-gabc", exe, "update", "--check"); !strings.Contains(out, "is out") {
		t.Errorf("dev build: %q", out)
	}

	// Homebrew's: brew upgrade.
	cellar := filepath.Join(t.TempDir(), "Cellar", "stagd", "0.2.0", "bin", "stagd")
	os.MkdirAll(filepath.Dir(cellar), 0o755)
	os.WriteFile(cellar, []byte("brew's stagd"), 0o755)

	if _, err := runAs(t, "v0.2.0", cellar, "update"); err == nil || !strings.Contains(err.Error(), "brew upgrade stagd") {
		t.Errorf("homebrew: %v", err)
	}
}

func TestUpdateRefusesATamperedDownload(t *testing.T) {
	fakeReleases(t, "v0.3.0", []byte("the new stagd"), true)

	exe := filepath.Join(t.TempDir(), "stagd")
	os.WriteFile(exe, []byte("the old stagd"), 0o755)

	if _, err := runAs(t, "v0.2.0", exe, "update"); err == nil || !strings.Contains(err.Error(), "doesn't match its checksum") {
		t.Fatalf("tampered: %v", err)
	}

	if got, _ := os.ReadFile(exe); string(got) != "the old stagd" {
		t.Fatal("a tampered download replaced the binary")
	}
}
