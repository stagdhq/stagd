# Releasing

## Images

The image is the binary on distroless, as an unprivileged user (65532). CI publishes `main` as `:main` and `:sha-<commit>`, and `v*` tags as their version and `:latest`.

Each also comes as the runner, with `-runner` after the tag (`:sha-<commit>-runner`): Docker's CLI with `stagd` in it, which Stagd runs `stagd docker up` in.

## Releases

A `v*` tag also makes the GitHub release, with the binaries, `install.sh`, `install.ps1` and `checksums.txt`, and updates the Homebrew cask in [stagdhq/homebrew-tap](https://github.com/stagdhq/homebrew-tap).

To release, tag with a one-line summary, written as it should read at the top of the notes, and push the tag:

```bash
git tag -a v0.5.0 -m "v0.5.0: What's in it"
```

## Changelog

The release's notes, which are the changelog, are that summary and the commits since the last tag (`scripts/release-notes.sh`):

- `feat:` under New
- `fix:` under Fixes
- `deploy:`, `docs:`, `perf:` and `refactor:` under Also
- `chore:` left out

So write commit subjects for the notes, and use `feat:` or `fix:` for what users notice; `feat!:` marks a breaking change.
