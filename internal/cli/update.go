package cli

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"
)

// releasesURL is where stagd's releases are: /latest redirects to the
// newest one's tag, /download/<tag>/<file> has its files. Tests replace it.
var releasesURL = "https://github.com/stagdhq/stagd/releases"

// executable is this binary's path, which update replaces; tests replace it.
var executable = os.Executable

func updateCommand() *cli.Command {
	return &cli.Command{
		Name:  "update",
		Usage: "update stagd to its latest release",
		Description: "Downloads the latest release for this machine, checks it against the release's checksums,\n" +
			"and replaces this binary with it. Installed with Homebrew: brew upgrade stagd.",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "check", Usage: "only say whether there's a newer release"},
		},
		Action: update,
	}
}

func update(ctx context.Context, cmd *cli.Command) error {
	w := cmd.Root().Writer
	current := cmd.Root().Version

	latest, err := latestRelease(ctx)
	if err != nil {
		return fmt.Errorf("finding the latest release: %w", err)
	}

	if !older(current, latest) {
		_, err := fmt.Fprintf(w, "stagd %s is the latest release.\n", current)

		return err
	}

	if cmd.Bool("check") {
		_, err := fmt.Fprintf(w, "stagd %s is out (this is %s): stagd update installs it.\n", latest, current)

		return err
	}

	exe, err := executable()
	if err != nil {
		return err
	}

	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}

	// Homebrew keeps track of its own files: a cask's, or a formula's from before.
	if strings.Contains(exe, "/Caskroom/") || strings.Contains(exe, "/Cellar/") {
		return cli.Exit("this stagd came from Homebrew: brew upgrade stagd", 1)
	}

	asset, name := releaseAsset(runtime.GOOS, runtime.GOARCH)
	base := releasesURL + "/download/" + latest

	fmt.Fprintf(cmd.Root().ErrWriter, "Downloading stagd %s (%s/%s)…\n", latest, runtime.GOOS, runtime.GOARCH)

	archive, err := download(ctx, base+"/"+asset)
	if err != nil {
		return err
	}

	sums, err := download(ctx, base+"/checksums.txt")
	if err != nil {
		return err
	}

	if err := checkSum(archive, asset, sums); err != nil {
		return err
	}

	binary, err := fromArchive(archive, asset, name)
	if err != nil {
		return err
	}

	// Windows won't overwrite a running .exe, but moves it aside.
	if err := replace(exe, binary, runtime.GOOS == "windows"); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return cli.Exit(fmt.Sprintf("can't write %s: run stagd update with sudo, or install again with curl -fsSL https://stagd.dev/install.sh | bash", filepath.Dir(exe)), 1)
		}

		return err
	}

	_, err = fmt.Fprintf(w, "Updated stagd %s to %s.\n", current, latest)

	return err
}

// latestRelease is the newest release's tag, from where /latest redirects:
// no API call, so no rate limit.
func latestRelease(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, releasesURL+"/latest", nil)
	if err != nil {
		return "", err
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	_, tag, ok := strings.Cut(res.Header.Get("Location"), "/releases/tag/")
	if !ok || tag == "" {
		return "", fmt.Errorf("no release to be found (%s)", res.Status)
	}

	return tag, nil
}

func download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	res, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", url, res.Status)
	}

	return io.ReadAll(res.Body)
}

// checkSum checks data against name's line in a checksums.txt.
func checkSum(data []byte, name string, sums []byte) error {
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])

	lines := bufio.NewScanner(strings.NewReader(string(sums)))

	for lines.Scan() {
		fields := strings.Fields(lines.Text())

		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			if fields[0] != got {
				return errors.New("the download doesn't match its checksum: not installing it")
			}

			return nil
		}
	}

	return fmt.Errorf("%s isn't in the release's checksums", name)
}

// releaseAsset is this machine's archive in a release, and the binary's name in it.
func releaseAsset(goos, goarch string) (asset, binary string) {
	if goos == "windows" {
		return fmt.Sprintf("stagd_windows_%s.zip", goarch), "stagd.exe"
	}

	return fmt.Sprintf("stagd_%s_%s.tar.gz", goos, goarch), "stagd"
}

// fromArchive is one file from a release's archive: a .zip for Windows, else a .tar.gz.
func fromArchive(archive []byte, asset, name string) ([]byte, error) {
	if strings.HasSuffix(asset, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}

		for _, f := range zr.File {
			if f.Name != name {
				continue
			}

			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()

			return io.ReadAll(rc)
		}

		return nil, fmt.Errorf("no %s in the release", name)
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}

	tr := tar.NewReader(gz)

	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("no %s in the release", name)
		}

		if err != nil {
			return nil, err
		}

		if h.Name == name {
			return io.ReadAll(tr)
		}
	}
}

// replace puts a new binary where exe is: written next to it, then renamed
// over it, so stagd is never half-written. aside first moves the old one
// to exe.old (Windows: a running .exe can be moved, not overwritten); the
// next update removes it.
func replace(exe string, binary []byte, aside bool) error {
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".stagd-update-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(binary); err != nil {
		tmp.Close()

		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}

	if aside {
		os.Remove(exe + ".old")

		if err := os.Rename(exe, exe+".old"); err != nil {
			return err
		}
	}

	return os.Rename(tmp.Name(), exe)
}

// older reports whether version is older than latest, both vMAJOR.MINOR.PATCH.
// A development build (dev, or git describe's v0.2.0-11-gabc) counts as older.
func older(version, latest string) bool {
	v, ok := semver(version)
	if !ok {
		return true
	}

	l, ok := semver(latest)
	if !ok {
		return false
	}

	for i := range v {
		if v[i] != l[i] {
			return v[i] < l[i]
		}
	}

	return false
}

func semver(s string) ([3]int, bool) {
	var out [3]int

	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}

	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}

		out[i] = n
	}

	return out, true
}
