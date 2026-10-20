package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreKeepsOneTokenPerAPI(t *testing.T) {
	s := Store{Dir: t.TempDir(), NoKeychain: true}

	if err := s.Save("http://localhost:8787", "local-token"); err != nil {
		t.Fatal(err)
	}

	if err := s.Save("https://api.stagd.dev", "prod-token"); err != nil {
		t.Fatal(err)
	}

	got, err := s.Load("http://localhost:8787/")
	if err != nil || got != "local-token" {
		t.Fatalf("local = %q, %v", got, err)
	}

	if err := s.Delete("http://localhost:8787"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Load("http://localhost:8787"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("after delete: %v", err)
	}

	got, err = s.Load("https://api.stagd.dev")
	if err != nil || got != "prod-token" {
		t.Fatalf("prod = %q, %v", got, err)
	}
}

func TestStoreFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	s := Store{Dir: dir, NoKeychain: true}

	if err := s.Save("http://localhost:8787", "x"); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}

	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 600", mode)
	}
}
