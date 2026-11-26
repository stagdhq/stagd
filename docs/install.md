# Install

On macOS and Linux:

```bash
curl -fsSL https://stagd.dev/install.sh | bash
```

On Windows, in PowerShell:

```powershell
irm https://stagd.dev/install.ps1 | iex
```

Either downloads the latest release for your machine (amd64 or arm64), checks it against the release's checksums, and installs `stagd`: in `~/.local/bin`, or on Windows in `%LOCALAPPDATA%\Programs\stagd`, which it adds to your PATH. `STAGD_INSTALL_DIR` puts it elsewhere, `STAGD_VERSION` installs another release.

Or with Homebrew, or Go:

```bash
brew install stagdhq/tap/stagd
go install github.com/stagdhq/stagd/cmd/stagd@latest
```

## Update

`stagd update` updates it in place, the same way; `stagd update --check` only says whether there's a newer release.

With Homebrew, `brew upgrade stagdhq/tap/stagd`. Homebrew 6 and later leaves third-party taps out of a plain `brew upgrade` until you trust them, once: `brew trust stagdhq/tap`.

## The image

The relay runs from the image `ghcr.io/stagdhq/stagd`. See [relay.md](relay.md).

## Using the CLI

```bash
stagd login                          # sign in through your browser; the session goes to your OS keychain
stagd orgs create acme --name "Acme" # or: stagd orgs list, stagd orgs switch <slug>
stagd github connect                 # install the Stagd GitHub App and link it to the org
stagd compute connect namespace      # where previews run: namespace, kubernetes or docker
stagd secrets set --repo acme/web STRIPE_KEY   # asks for the value without echoing it
openssl rand -base64 32 | stagd secrets set --repo acme/web SESSION_SECRET=@-   # a random one, never on screen
stagd validate                       # check stagd.yaml and the compose file in a checkout
stagd validate --explain             # and print stagd.yaml as Stagd reads it, defaults filled in
stagd preview create --repo acme/web --pr 42
stagd preview list
```

`stagd <command> --help` explains each one. `--json` prints JSON for scripts, and `--api-url` (`STAGD_API_URL`) points the CLI at another Stagd server.
