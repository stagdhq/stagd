package cli

import (
	"os"
	"testing"

	"stagd.dev/stagd/internal/client/auth"
)

// Tests never touch the real keychain or ~/.config.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "stagd-cli-test")
	if err != nil {
		panic(err)
	}

	store = auth.Store{Dir: dir, NoKeychain: true}
	code := m.Run()

	_ = os.RemoveAll(dir)
	os.Exit(code)
}
