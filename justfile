# advp — development tasks. Run `just` to list recipes.

set shell := ["bash", "-euo", "pipefail", "-c"]

binary := "advp"
pkg := "github.com/mbovo/advp"
main := "./cmd/advp"
bin_dir := "bin"
dist_dir := "dist"

version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
commit := `git rev-parse HEAD 2>/dev/null || true`
date := `date -u +%Y-%m-%dT%H:%M:%SZ`
ldflags := "-s -w -X " + pkg + "/internal/version.Version=" + version + " -X " + pkg + "/internal/version.Commit=" + commit + " -X " + pkg + "/internal/version.Date=" + date

# Pinned tool versions, used by the recipes below (and installed by `just tools`).
golangci_version := "v2.14.0"
govulncheck_version := "v1.8.0"
goreleaser_version := "v2.18.2"
actionlint_version := "v1.7.12"
# Always the pinned version (CI uses the same); override with GOLANGCI_LINT=/path.
golangci := "GOLANGCI_LINT_VERSION=" + golangci_version + " scripts/golangci-lint.sh"
# Other tools run at their pinned version via `go run` (no install needed; cached
# after the first run). Override with GOVULNCHECK=/GORELEASER=/ACTIONLINT=/path.
govulncheck := env("GOVULNCHECK", "go run golang.org/x/vuln/cmd/govulncheck@" + govulncheck_version)
goreleaser := env("GORELEASER", "go run github.com/goreleaser/goreleaser/v2@" + goreleaser_version)
actionlint := env("ACTIONLINT", "go run github.com/rhysd/actionlint/cmd/actionlint@" + actionlint_version)

# Target platforms for `just cross` (the ICMP code supports Linux and macOS).
platforms := "linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"
# GOOS values linted/vetted so platform-specific files are checked too.
lint_goos := "darwin linux"

# List available recipes
[group('help')]
default:
    @just --list --unsorted

# Print the version that would be embedded in a build
[group('help')]
version:
    @echo "{{ version }} ({{ commit }}, {{ date }})"

# ---------------------------------------------------------------- build

# Build the binary into ./bin
[group('build')]
build:
    CGO_ENABLED=0 go build -trimpath -ldflags '{{ ldflags }}' -o {{ bin_dir }}/{{ binary }} {{ main }}

# Build and run with arguments, e.g. `just run -c 5 1.1.1.1`
[group('build')]
run *args: build
    ./{{ bin_dir }}/{{ binary }} {{ args }}

# Install into $GOBIN (or $GOPATH/bin)
[group('build')]
install:
    CGO_ENABLED=0 go install -trimpath -ldflags '{{ ldflags }}' {{ main }}

# Cross-compile for all supported platforms into ./dist
[group('build')]
cross:
    #!/usr/bin/env bash
    set -euo pipefail
    for p in {{ platforms }}; do
        os="${p%/*}"; arch="${p#*/}"
        out="{{ dist_dir }}/{{ binary }}_${os}_${arch}"
        echo "→ $out"
        CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags '{{ ldflags }}' -o "$out" {{ main }}
    done

# Grant raw-socket capability to the built binary (Linux, needs sudo)
[group('build')]
[linux]
setcap: build
    sudo setcap cap_net_raw+ep {{ bin_dir }}/{{ binary }}

# Allow unprivileged ICMP sockets for all groups (Linux, needs sudo)
[group('build')]
[linux]
ping-group:
    sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"

# ---------------------------------------------------------------- test

# Run unit tests
[group('test')]
test *args:
    go test {{ args }} ./...

# Run tests with the race detector, uncached
[group('test')]
test-race:
    go test -race -count=1 ./...

# Run tests with coverage and print the total
[group('test')]
cover:
    go test -race -covermode=atomic -coverprofile=coverage.out ./...
    go tool cover -func=coverage.out | tail -n 1

# Open the HTML coverage report
[group('test')]
cover-html: cover
    go tool cover -html=coverage.out

# Smoke tests: run the built binary end to end (ICMP cases skip if blocked unless ADVP_REQUIRE_ICMP=1)
[group('test')]
smoke: build
    ADVP_BIN={{ justfile_directory() }}/{{ bin_dir }}/{{ binary }} go test -tags smoke -count=1 -v ./test/smoke/

# Quick manual check: ping 127.0.0.1 three times with the built binary
[group('test')]
ping-loopback: build
    ./{{ bin_dir }}/{{ binary }} -c 3 -i 0.2 -W 1 --color never 127.0.0.1

# Test the release scripts (next-version.sh)
[group('test')]
test-scripts:
    bash scripts/next-version_test.sh

# Unit tests, script tests and smoke tests
[group('test')]
test-all: test-race test-scripts smoke

# ---------------------------------------------------------------- quality

# Format code (gofmt + goimports via golangci-lint)
[group('quality')]
fmt:
    {{ golangci }} fmt ./...

# Fail if code is not formatted
[group('quality')]
fmt-check:
    {{ golangci }} fmt --diff ./...

# go vet for every supported OS
[group('quality')]
vet:
    for os in {{ lint_goos }}; do echo "→ vet GOOS=$os"; GOOS=$os go vet -tags smoke ./...; done

# Run golangci-lint for every supported OS
[group('quality')]
lint:
    for os in {{ lint_goos }}; do echo "→ lint GOOS=$os"; GOOS=$os {{ golangci }} run ./...; done

# Run golangci-lint and apply automatic fixes
[group('quality')]
lint-fix:
    {{ golangci }} run --fix ./...

# Lint GitHub workflows (actionlint) and shell scripts (shellcheck, if installed)
[group('quality')]
lint-ci:
    {{ actionlint }}
    if command -v shellcheck >/dev/null; then shellcheck scripts/*.sh packaging/*.sh; else echo "shellcheck not installed, skipped"; fi

# Scan dependencies for known vulnerabilities
[group('quality')]
vuln:
    {{ govulncheck }} ./...

# Tidy go.mod / go.sum
[group('quality')]
tidy:
    go mod tidy

# Fail if go.mod / go.sum are not tidy
[group('quality')]
tidy-check:
    go mod tidy -diff

# Pre-push gate: fmt, tidy, vet, lint, race tests, script tests, cross build (CI also runs smoke, vuln, snapshot)
[group('quality')]
check: fmt-check tidy-check vet lint test-race test-scripts cross

# ---------------------------------------------------------------- release

# Validate the GoReleaser configuration
[group('release')]
release-check:
    {{ goreleaser }} check

# Build a local snapshot release into ./dist (no publishing)
[group('release')]
release-snapshot:
    {{ goreleaser }} release --snapshot --clean

# Print the version the next merge to main would release (Conventional Commits)
[group('release')]
next-version:
    scripts/next-version.sh

# Manually tag and push a release, e.g. `just tag v1.2.0` (the Release workflow publishes it)
[group('release')]
tag v:
    @[[ "{{ v }}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "version must look like v1.2.3"; exit 1; }
    @test -z "$(git status --porcelain)" || { echo "working tree is dirty"; exit 1; }
    git tag -a {{ v }} -m "advp {{ v }}"
    git push origin {{ v }}

# ---------------------------------------------------------------- misc

# Install pinned development tools into $GOBIN
[group('misc')]
tools:
    go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@{{ golangci_version }}
    go install golang.org/x/vuln/cmd/govulncheck@{{ govulncheck_version }}
    go install github.com/goreleaser/goreleaser/v2@{{ goreleaser_version }}
    go install github.com/rhysd/actionlint/cmd/actionlint@{{ actionlint_version }}

# Remove build artifacts
[group('misc')]
clean:
    rm -rf {{ bin_dir }} {{ dist_dir }} coverage.out
