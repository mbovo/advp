#!/usr/bin/env bash
# Run the golangci-lint version pinned in the justfile (GOLANGCI_LINT_VERSION),
# regardless of what is installed on PATH. Lookup order:
#   1. $GOLANGCI_LINT (explicit binary)
#   2. golangci-lint on PATH, if it is exactly the pinned version
#   3. a copy installed once into ${XDG_CACHE_HOME:-~/.cache}/advp/
# The cached copy is built for the host even when GOOS/GOARCH are set for the
# code being linted (just lint runs once per GOOS).
set -euo pipefail

want=${GOLANGCI_LINT_VERSION:?set GOLANGCI_LINT_VERSION, e.g. v2.14.0}

if [ -n "${GOLANGCI_LINT:-}" ]; then
	exec "$GOLANGCI_LINT" "$@"
fi

have=""
if command -v golangci-lint >/dev/null 2>&1; then
	have=$(env -u GOOS -u GOARCH golangci-lint --version 2>/dev/null || true)
	case "$have" in
	*"has version ${want#v} "* | *"has version $want "*) exec golangci-lint "$@" ;;
	esac
	have=$(printf '%s\n' "$have" | awk '{print $4; exit}')
fi

dir="${XDG_CACHE_HOME:-$HOME/.cache}/advp/golangci-lint-$want"
bin="$dir/golangci-lint"
if [ ! -x "$bin" ]; then
	echo "golangci-lint: need $want, found '${have:-nothing}' on PATH; installing $want into $dir" >&2
	mkdir -p "$dir"
	env -u GOOS -u GOARCH GOBIN="$dir" go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$want"
fi
exec "$bin" "$@"
