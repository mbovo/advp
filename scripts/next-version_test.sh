#!/usr/bin/env bash
# Tests for next-version.sh: builds throw-away repositories and checks the
# computed release tag. Run with: bash scripts/next-version_test.sh
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/next-version.sh"
fails=0

repo() {
	dir=$(mktemp -d "${TMPDIR:-/tmp}/nv-test.XXXXXX")
	git -C "$dir" init -q -b main
	git -C "$dir" config user.name test
	git -C "$dir" config user.email test@example.com
	git -C "$dir" config commit.gpgsign false
	git -C "$dir" config tag.gpgsign false
}

commit() {
	git -C "$dir" commit -q --allow-empty -m "$1"
}

tag() {
	git -C "$dir" tag -a "$1" -m "$1"
}

expect() {
	local want=$1 got
	got=$(cd "$dir" && "${BASH:-bash}" "$script" 2>/dev/null)
	if [ "$got" != "$want" ]; then
		echo "FAIL: $2: got '${got}', want '${want}'"
		fails=$((fails + 1))
	else
		echo "ok:   $2 -> '${got}'"
	fi
}

repo
commit "chore: init"
expect "" "chore only, no tags"
commit "fix: first fix"
expect "v0.0.1" "fix without tags"
commit "feat: something"
expect "v0.1.0" "feat without tags"

tag v0.1.0
expect "" "nothing since tag"
commit "docs: readme"
commit "ci(release): tweak"
commit "test: more"
expect "" "docs/ci/test only"
commit "fix(icmp): bug"
expect "v0.1.1" "scoped fix"
commit "feat(live): new view"
expect "v0.2.0" "feat beats fix"
tag v0.2.0
commit "refactor!: drop flag"
expect "v0.3.0" "breaking change before 1.0 bumps minor"

tag v1.0.0
commit "fix: a

BREAKING CHANGE: behaviour changed"
expect "v2.0.0" "breaking footer after 1.0"

tag v2.0.0
commit "Add a thing (not conventional)"
expect "v2.0.1" "non-conventional subject is a patch"

tag v2.0.1
commit "feat: experimental [skip release]"
expect "" "skip release marker"

git -C "$dir" checkout -q -b topic
commit "docs: topic docs"
git -C "$dir" checkout -q main
git -C "$dir" merge -q --no-ff -m "Merge branch 'topic'" topic
expect "" "merge commits are ignored"

tag v2.1.0-rc.1
commit "fix: after rc"
expect "v2.0.2" "pre-release tags are not a base"

rm -rf "$dir"
if [ "$fails" -gt 0 ]; then
	echo "$fails failure(s)"
	exit 1
fi
echo "all next-version tests passed"
