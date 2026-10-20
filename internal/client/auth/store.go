package auth

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

const service = "stagd"

// ErrNoToken means nobody is signed in to that API.
var ErrNoToken = errors.New("no stored token")

// Store keeps one session token per API, in the OS keychain when there is
// one, else in a file only the user can read (~/.config/stagd/tokens.json).
type Store struct {
	// Dir overrides the file location (tests).
	Dir string
	// NoKeychain forces the file (tests, machines without a keychain).
	NoKeychain bool
}

// account is the keychain entry name: the API's host, so dev and prod sign-ins don't mix.
func account(apiURL string) string {
	u, err := url.Parse(apiURL)
	if err != nil || u.Host == "" {
		return apiURL
	}

	return u.Host
}

func (s Store) file() (string, error) {
	if s.Dir != "" {
		return filepath.Join(s.Dir, "tokens.json"), nil
	}

	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "stagd", "tokens.json"), nil
}

func (s Store) readFile() (map[string]string, error) {
	path, err := s.file()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}

	if err != nil {
		return nil, err
	}

	tokens := map[string]string{}
	if err := json.Unmarshal(data, &tokens); err != nil {
		return nil, err
	}

	return tokens, nil
}

func (s Store) writeFile(tokens map[string]string) error {
	path, err := s.file()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0o600)
}

// Save stores the token for an API.
func (s Store) Save(apiURL, token string) error {
	if !s.NoKeychain {
		if err := keyring.Set(service, account(apiURL), token); err == nil {
			return nil
		}
	}

	tokens, err := s.readFile()
	if err != nil {
		return err
	}

	tokens[account(apiURL)] = token

	return s.writeFile(tokens)
}

// Load returns the stored token for an API, or ErrNoToken.
func (s Store) Load(apiURL string) (string, error) {
	if !s.NoKeychain {
		token, err := keyring.Get(service, account(apiURL))
		if err == nil {
			return token, nil
		}
	}

	tokens, err := s.readFile()
	if err != nil {
		return "", err
	}

	token, ok := tokens[account(apiURL)]
	if !ok {
		return "", ErrNoToken
	}

	return token, nil
}

// Delete forgets the token for an API, wherever it was stored.
func (s Store) Delete(apiURL string) error {
	if !s.NoKeychain {
		_ = keyring.Delete(service, account(apiURL))
	}

	tokens, err := s.readFile()
	if err != nil {
		return err
	}

	if _, ok := tokens[account(apiURL)]; !ok {
		return nil
	}

	delete(tokens, account(apiURL))

	return s.writeFile(tokens)
}
