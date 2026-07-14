# Releasing Codog

Codog releases are built from immutable, annotated `v*` tags. The release
workflow runs the same smoke gate used by CI, builds the supported platform
archives, generates `SHA256SUMS`, and publishes the artifacts to GitHub.

## Prepare

1. Update the version reported by Codog and finish all release changes on
   `main`.
2. Run `scripts/smoke.sh` and confirm the `main` CI run passes.
3. Confirm `git status --short --branch` is clean and `main` matches
   `origin/main`.
4. Confirm the version has no existing tag or GitHub Release.

The release script rejects a version that differs from the version compiled
into Codog. It also rejects dirty checkouts and commits other than the checked
out `HEAD`.

## Publish

Create and push an annotated tag from the verified `main` commit:

```sh
git tag -a v0.1.0 -m "Codog v0.1.0"
git push origin v0.1.0
```

Pushing the tag starts the `Release` workflow. A failed workflow can be rerun
from GitHub Actions. The manual workflow trigger accepts an existing tag and is
intended for recovery when publication failed after a tag was pushed.

The workflow publishes archives for macOS, Linux, and Windows on `amd64` and
`arm64`. Each archive contains the Codog binary, `README.md`, and `LICENSE`.
The release also includes `SHA256SUMS`.

To reproduce the artifacts without publishing them:

```sh
scripts/release.sh --version 0.1.0 --commit v0.1.0 --output-dir dist
```

## Verify

Inspect the published release and verify downloaded artifacts before announcing
it:

```sh
gh release view v0.1.0
gh release download v0.1.0 --dir dist-verify
(cd dist-verify && shasum -a 256 -c SHA256SUMS)
GOBIN="$(mktemp -d)" go install github.com/Rememorio/codog/cmd/codog@v0.1.0
```

Run the native archive's `codog --version --json` and confirm its version and
Git SHA match the release tag and commit.

## Recovery

Do not move or overwrite a published release tag. If source behavior is wrong,
fix it on `main` and publish a new patch version. If only the release job failed
before publication, rerun the workflow for the existing tag. Release assets are
uploaded with replacement enabled so a partial failed upload can be recovered
before repository release immutability is enabled.
