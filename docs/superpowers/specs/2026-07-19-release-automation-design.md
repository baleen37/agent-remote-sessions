# Release Automation Design

- Date: 2026-07-19
- Status: approved for implementation
- Package: `agent-remote-sessions`

## Goal and scope

Every releasable commit merged into `main` publishes one version of ars to
GitHub Releases and the public npm registry. Conventional Commits determine
the version without a release pull request or a version commit.

The release contains native ars binaries for darwin/arm64, linux/amd64, and
linux/arm64. Users may install a native archive or run:

~~~sh
npm install -g agent-remote-sessions
~~~

The first release is `v1.0.0`. `feat` creates a minor release, `fix` and
`perf` create a patch release, and a breaking change creates a major release.
Documentation, chore, and test-only commits do not release. Pre-releases,
maintenance branches, Homebrew, Windows binaries, and automatic version
commits are outside this design.

The repository also gains an MIT `LICENSE`. This design does not change ars
commands, session discovery, SSH behavior, or public JSON.

## Workflow

The existing `.github/workflows/ci.yml` keeps its Linux and macOS verification
matrix. One `release` job is added with these gates:

1. run only for a push to `main`
2. require every `verify` matrix job to succeed
3. serialize through a `release-main` concurrency group without canceling the
   active release
4. check out the full history and tags
5. preflight a `0.0.0` release build and npm package before semantic-release
6. run semantic-release, which either reports no release or publishes the next
   stable version

Only the release job receives `contents: write` and `id-token: write`.
Pull-request jobs and ordinary branch pushes retain read-only permissions.

semantic-release owns commit analysis, release notes, the `v${version}` tag,
npm publication, and the GitHub Release. It uses `@semantic-release/exec` to
run the repository's release builder during prepare, `@semantic-release/npm`
with a generated package root, and `@semantic-release/github` to upload the
native archives and checksum file. It does not use the git or changelog
plugins, so a release never pushes a generated commit to `main`.

## Release builder

The existing `cmd/ars-build` remains the only build entry point. A release
form is added instead of introducing GoReleaser or a second build program:

~~~sh
go run ./cmd/ars-build --release 1.0.0
~~~

Release mode accepts a stable `MAJOR.MINOR.PATCH` value, removes and recreates
only the repository's `dist` directory, and then:

1. builds the existing embedded collectors for darwin/arm64, linux/amd64, and
   linux/arm64
2. builds ars with `CGO_ENABLED=0` for those same three targets
3. creates one archive per target containing `ars`, `README.md`, and `LICENSE`
4. computes `SHA256SUMS` over the three archives
5. assembles the generated npm package

All Go release builds use `-trimpath` and omit environment-specific VCS and
build identifiers. The target list stays in Go beside the existing collector
targets so CI, archives, and npm cannot silently drift apart.

`dist` contains exactly:

~~~text
dist/
  ars_1.0.0_darwin_arm64.tar.gz
  ars_1.0.0_linux_amd64.tar.gz
  ars_1.0.0_linux_arm64.tar.gz
  SHA256SUMS
  npm/
    package.json
    README.md
    LICENSE
    bin/ars.js
    vendor/ars-darwin-arm64
    vendor/ars-linux-amd64
    vendor/ars-linux-arm64
~~~

Generated collectors, native binaries, archives, and npm package files remain
ignored build outputs.

## npm package contract

The repository root has a private `package.json` and lockfile only for the
release toolchain. It cannot be published accidentally. A committed npm
package template supplies stable metadata and the launcher; release mode
copies it into `dist/npm`, adds the native binaries and documentation, and
semantic-release writes the actual version only into that generated package.

The published package is named `agent-remote-sessions`, has public access, and
exposes one `ars` bin entry. The small Node launcher maps only these pairs:

| Node platform | Node architecture | Native binary |
| --- | --- | --- |
| `darwin` | `arm64` | `ars-darwin-arm64` |
| `linux` | `x64` | `ars-linux-amd64` |
| `linux` | `arm64` | `ars-linux-arm64` |

The launcher inherits stdin, stdout, and stderr so fzf and resumed SSH PTYs
remain interactive. It returns the native process exit code and mirrors a
terminating signal. Unsupported platform pairs fail before spawning and list
the supported targets. The package never downloads code during installation
or execution; all three binaries are inside the published npm tarball.

## npm authentication bootstrap

Recurring publication uses npm Trusted Publishing with GitHub Actions OIDC,
not a long-lived `NPM_TOKEN`. The npm publisher is restricted to
`baleen37/agent-remote-sessions` and the exact `ci.yml` workflow. The release
job uses a GitHub-hosted runner and a Node/npm version that supports npm OIDC.

npm requires the package to exist before a trusted publisher can be attached,
so setup has one explicit bootstrap:

1. build and manually publish `agent-remote-sessions@0.0.0` with the non-default
   `bootstrap` dist-tag
2. configure the package's trusted publisher for this repository and
   `ci.yml`
3. merge the automation and let the first releasable `main` push publish
   `v1.0.0` on npm's `latest` tag

The bootstrap version is installable only when explicitly requested as
`@bootstrap`; it does not become the default package version. After OIDC is
verified, traditional package tokens should not be retained by the workflow.

## Failure and recovery

CI failure prevents the release job. A non-releasable commit is a successful
no-op. Release-specific packaging is preflighted before semantic-release so
deterministic build and archive failures occur before release lifecycle work.

Git tags, npm versions, and GitHub Releases are separate external state and
cannot be published atomically. Recovery starts by inspecting all three and
follows these rules:

- If neither registry contains the version, remove a failed release tag only
  after verifying its exact commit and then rerun.
- If npm contains the version but GitHub does not, preserve the immutable npm
  version and reconstruct the GitHub Release from the same tag and commit.
- If GitHub contains the version but npm does not, rebuild the exact tag and
  publish only the missing npm version.
- Never delete or reuse a version that users could already have installed.

These are operator instructions in README, not a second recovery workflow.
Keeping recovery manual avoids expanding normal release credentials and
prevents an automatic retry from overwriting ambiguous external state.

## Verification

Go tests for release mode cover:

- exact build targets and commands
- stable version validation and rejection of unsafe values
- archive names, members, and executable modes
- deterministic target ordering
- checksum contents and verification
- generated npm package contents
- cleanup limited to `dist`

Node's built-in test runner covers platform selection, unsupported targets,
stdio inheritance, native exit codes, and signal termination. Tests use a
stub executable and do not start SSH or fzf.

Every CI verification still runs:

~~~sh
go run ./cmd/ars-build --assets-only
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/ars-build
~~~

Release preflight additionally runs a `0.0.0` release build, validates all
checksums, runs `npm pack --dry-run`, installs the resulting npm tarball into a
temporary prefix on Linux, and proves that its launcher invokes the packaged
linux/amd64 binary. semantic-release dry-run coverage verifies that feature,
fix, breaking, and non-release commits map to the intended outcomes; artifact
checks do not rely on dry-run because it skips prepare and publish.

## Acceptance criteria

The implementation is complete only after the real `v1.0.0` flow proves:

1. the GitHub Actions verify and release jobs succeeded
2. GitHub Release `v1.0.0` contains the three named archives and
   `SHA256SUMS`
3. downloaded archive checksums match
4. npm reports `agent-remote-sessions@latest` as `1.0.0` with provenance
5. `npm install -g agent-remote-sessions` succeeds in a disposable prefix
6. the installed launcher starts the packaged native ars binary
7. a documentation-only commit is classified as no release and a fix is
   classified as the next patch release without publishing synthetic test
   versions

Live npm and GitHub state, not local configuration alone, is the final proof.
