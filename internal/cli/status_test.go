package cli

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
)

func TestStatusHint(t *testing.T) {
	down := connect.NewError(connect.CodeUnavailable, errors.New("503"))

	cases := []struct {
		name string
		err  error
		api  string
		hint bool
	}{
		{"unavailable", down, DefaultAPIURL, true},
		{"wrapped", fmt.Errorf("listing previews: %w", down), DefaultAPIURL, true},
		{"internal", connect.NewError(connect.CodeInternal, errors.New("boom")), DefaultAPIURL + "/", true},
		{"not found", connect.NewError(connect.CodeNotFound, errors.New("no such preview")), DefaultAPIURL, false},
		{"signed out", connect.NewError(connect.CodeUnauthenticated, errors.New("sign in")), DefaultAPIURL, false},
		{"not the API's", errors.New("flag provided but not defined"), DefaultAPIURL, false},
		{"another API", down, "http://localhost:8792", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := StatusHint(c.err, c.api) != ""

			if got != c.hint {
				t.Errorf("StatusHint() = %v, want %v", got, c.hint)
			}
		})
	}
}
