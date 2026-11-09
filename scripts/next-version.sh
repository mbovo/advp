#!/usr/bin/env bash
# Print the next release tag (vX.Y.Z) for HEAD, computed from the Conventional
# Commits since the latest release tag. Print nothing when no release is
# needed. Diagnostics go to stderr.
#
#   type!: ... or a "BREAKING CHANGE:" footer  -> major (minor while < v1.0.0)
#   feat: ...                                  -> minor
#   fix:, perf:, refactor:, any other subject  -> patch
#   docs:, test:, ci:, chore:, style:, build:  -> no release on their own
#   "[skip release]" anywhere in the message   -> commit ignored
#
# Merge commits are ignored; pre-release tags (v1.2.0-rc.1) are not used as
# the base version. Compatible with bash 3.2 (macOS).
set -euo pipefail

last=$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' --exclude '*-*' 2>/dev/null || true)
range=HEAD
base=v0.0.0
if [ -n "$last" ]; then
	range="$last..HEAD"
	base=$last
fi

ver=${base#v}
IFS=. read -r major minor patch <<EOF
$ver
EOF

bump=0 # 0 = none, 1 = patch, 2 = minor, 3 = major
reason=""
while IFS= read -r -d '' msg; do
	# git separates entries with a newline after the NUL terminator.
	while [ "${msg#$'\n'}" != "$msg" ]; do msg=${msg#$'\n'}; done
	[ -z "$msg" ] && continue
	case "$msg" in *"[skip release]"*) continue ;; esac
	subject=${msg%%$'\n'*}
	level=1
	if printf '%s\n' "$subject" | grep -Eq '^[A-Za-z]+(\([^)]*\))?!:' ||
		printf '%s\n' "$msg" | grep -Eq '^BREAKING[ -]CHANGE:'; then
		level=3
	elif printf '%s\n' "$subject" | grep -Eq '^feat(\([^)]*\))?:'; then
		level=2
	elif printf '%s\n' "$subject" | grep -Eq '^(docs|tests?|ci|chore|style|build)(\([^)]*\))?:'; then
		level=0
	fi
	if [ "$level" -gt "$bump" ]; then
		bump=$level
		reason=$subject
	fi
done < <(git log --no-merges --format='%B%x00' "$range")

case $bump in
0)
	echo "next-version: no releasable commits since ${last:-the first commit}" >&2
	exit 0
	;;
1) patch=$((patch + 1)) ;;
2)
	minor=$((minor + 1))
	patch=0
	;;
3)
	if [ "$major" -eq 0 ]; then
		minor=$((minor + 1))
	else
		major=$((major + 1))
		minor=0
	fi
	patch=0
	;;
esac
echo "next-version: ${last:-v0.0.0} -> v$major.$minor.$patch (because of: $reason)" >&2
echo "v$major.$minor.$patch"
