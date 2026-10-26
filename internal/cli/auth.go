package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/stagdhq/stagd/internal/client/auth"
)

// store is where tokens live; replaced in tests.
var store = auth.Store{}

func authClient(cmd *cli.Command) *auth.Client {
	return &auth.Client{BaseURL: cmd.String("api-url"), HTTP: &http.Client{Timeout: 30 * time.Second}, Version: cmd.Root().Version}
}

// token returns --token / STAGD_TOKEN if set, else the one stored by `stagd login`.
func token(cmd *cli.Command) string {
	if t := cmd.String("token"); t != "" {
		return t
	}

	t, err := store.Load(cmd.String("api-url"))
	if err != nil {
		return ""
	}

	return t
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func loginCommand() *cli.Command {
	return &cli.Command{
		Name:  "login",
		Usage: "sign in to Stagd in your browser (device login)",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "no-browser", Usage: "print the link instead of opening it"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			w := cmd.Root().Writer
			client := authClient(cmd)

			code, err := client.Start(ctx)
			if err != nil {
				return err
			}

			fmt.Fprintf(w, "Your code: %s\n", code.UserCode)
			fmt.Fprintf(w, "Approve it at %s\n", code.VerificationURIComplete)

			if !cmd.Bool("no-browser") {
				if err := openBrowser(code.VerificationURIComplete); err != nil {
					fmt.Fprintln(w, "(couldn't open a browser; open the link yourself)")
				}
			}

			fmt.Fprintln(w, "Waiting for approval…")

			token, err := client.Wait(ctx, code)
			if err != nil {
				return err
			}

			if err := store.Save(cmd.String("api-url"), token); err != nil {
				return fmt.Errorf("signed in, but couldn't store the token: %w", err)
			}

			me, err := getMe(ctx, cmd, token)
			if err != nil {
				return err
			}

			fmt.Fprintf(w, "Signed in as %s (%s)\n", me.GetUser().GetEmail(), membershipLabel(me.GetActive()))

			if me.GetActive() == nil {
				fmt.Fprintln(w, "Next: create an org with `stagd orgs create`")
			}

			return nil
		},
	}
}

func logoutCommand() *cli.Command {
	return &cli.Command{
		Name:  "logout",
		Usage: "sign out and forget the stored token",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			apiURL := cmd.String("api-url")

			t, err := store.Load(apiURL)
			if errors.Is(err, auth.ErrNoToken) {
				fmt.Fprintln(cmd.Root().Writer, "Not signed in.")

				return nil
			}

			if err != nil {
				return err
			}

			// End the session on the server too, so the token can't be reused.
			_ = authClient(cmd).SignOut(ctx, t)

			if err := store.Delete(apiURL); err != nil {
				return err
			}

			fmt.Fprintln(cmd.Root().Writer, "Signed out.")

			return nil
		},
	}
}

var errNotSignedIn = errors.New("not signed in: run `stagd login`")
