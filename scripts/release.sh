#!/usr/bin/env bash
# Release helper: keeps the documented version, the tag and the published site in step.
#
# The docs footer names the release from params.version in site/hugo.toml, and
# release.yml refuses to build a tag that disagrees with it. That value has to be
# published *before* the tag exists, because GitHub Pages keys a deployment by the
# commit and discards a second deployment of one it has already published — so a
# tag can never republish the site. See the header of .github/workflows/pages.yml.
#
# Hence two steps, in this order:
#
#   scripts/release.sh prepare v0.3.1   # bump on a branch, open the PR
#   scripts/release.sh tag v0.3.1       # once that PR is merged: tag main and push
#
# Run them through Task: `task release:prepare RELEASE=v0.3.1`.

set -euo pipefail

# Every path below is repo-relative, so do not depend on where this was invoked.
cd "$(git rev-parse --show-toplevel)"

HUGO_TOML="site/hugo.toml"
VERSION_RE='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'

die() {
	echo "error: $*" >&2
	exit 1
}

# The one place that knows how the version is spelled in hugo.toml. The guard in
# release.yml reads it with the same expression; keep them together.
documented_version() {
	sed -n 's/^[[:space:]]*version = "\(.*\)"$/\1/p' "$1"
}

is_prerelease() {
	case "$1" in
	*-*) return 0 ;;
	*) return 1 ;;
	esac
}

require_clean_tree() {
	git diff-index --quiet HEAD -- ||
		die "the working tree has uncommitted changes; commit or stash them first"
}

require_version_arg() {
	[ -n "${1:-}" ] || die "usage: $0 $2 <version>, e.g. $0 $2 v0.3.1"
	[[ $1 =~ $VERSION_RE ]] || die "\"$1\" is not a version tag; expected vMAJOR.MINOR.PATCH"
}

cmd_prepare() {
	local version="${1:-}"
	require_version_arg "$version" prepare
	is_prerelease "$version" &&
		die "$version is a pre-release; the site stays on the last stable version, so there is nothing to bump. Tag it directly."

	command -v gh >/dev/null || die "gh is not on PATH; needed to open the pull request"
	require_clean_tree

	git fetch --quiet origin main --tags
	git rev-parse -q --verify "refs/tags/$version" >/dev/null &&
		die "tag $version already exists; pick the next version"

	local current
	current="$(documented_version "$HUGO_TOML")"
	[ "$current" != "$version" ] ||
		die "$HUGO_TOML already documents $version; nothing to prepare"

	local branch="keystone/release-$version"
	git rev-parse -q --verify "refs/heads/$branch" >/dev/null &&
		die "branch $branch already exists; delete it or finish that release"

	echo "==> branch $branch off origin/main"
	git checkout -q -b "$branch" origin/main

	echo "==> $HUGO_TOML: $current -> $version"
	sed -i "s|^\([[:space:]]*version = \"\).*\"$|\1$version\"|" "$HUGO_TOML"
	[ "$(documented_version "$HUGO_TOML")" = "$version" ] ||
		die "the bump did not take; $HUGO_TOML now reads \"$(documented_version "$HUGO_TOML")\""

	# Prove the footer really renders it, rather than trusting the file. Skipped
	# when Hugo is absent, since the CI build is not what this script guards.
	if command -v hugo >/dev/null; then
		echo "==> checking the footer renders $version"
		(cd site && hugo --gc --minify --quiet)
		grep -q "Documenting Keystone <strong>$version</strong>" site/public/index.html ||
			die "the built site does not show $version in the footer"
	else
		echo "==> hugo not on PATH; skipping the rendered-footer check"
	fi

	git commit -q -m "chore(release): document $version" -- "$HUGO_TOML"
	git push -q -u origin "$branch"

	gh pr create --base main --head "$branch" \
		--title "chore(release): document $version" \
		--body "Bumps the version the documentation site names, ahead of cutting \`$version\`.

This has to land before the tag: Pages discards a second deployment of a commit it
has already published, so a tag cannot republish the site. Merging this is the
deploy that puts \`$version\` in the footer.

After merge:

\`\`\`
task release:tag RELEASE=$version
\`\`\`"

	echo
	echo "Prepared. Merge that PR, then: task release:tag RELEASE=$version"
}

cmd_tag() {
	local version="${1:-}"
	require_version_arg "$version" tag
	require_clean_tree

	git fetch --quiet origin main --tags
	git rev-parse -q --verify "refs/tags/$version" >/dev/null &&
		die "tag $version already exists"

	# Tag what is on the remote, not whatever the local checkout happens to be.
	local target
	target="$(git rev-parse origin/main)"

	if is_prerelease "$version"; then
		echo "==> $version is a pre-release; not checking the documented version"
	else
		local documented
		documented="$(git show "origin/main:$HUGO_TOML" | documented_version /dev/stdin)"
		[ "$documented" = "$version" ] ||
			die "origin/main documents \"$documented\", not \"$version\". Run: task release:prepare RELEASE=$version"
	fi

	# Informational: the site should already carry the bump, published by the merge.
	if command -v gh >/dev/null; then
		local docs
		docs="$(gh run list --workflow pages.yml --branch main --limit 1 \
			--json headSha,conclusion --jq ".[] | select(.headSha==\"$target\") | .conclusion" 2>/dev/null || true)"
		case "$docs" in
		success) echo "==> docs already published for ${target:0:7}" ;;
		"") echo "==> note: no docs run found for ${target:0:7} yet" ;;
		*) echo "==> warning: the docs run for ${target:0:7} is \"$docs\"; the site may not name $version" ;;
		esac
	fi

	echo "==> tagging ${target:0:7} as $version"
	git tag -a "$version" "$target" -m "Release $version"
	git push -q origin "$version"

	echo
	echo "Pushed $version. GoReleaser is building; watch it with: gh run watch"
}

case "${1:-}" in
prepare)
	shift
	cmd_prepare "$@"
	;;
tag)
	shift
	cmd_tag "$@"
	;;
*)
	echo "usage: $0 {prepare|tag} <version>" >&2
	exit 1
	;;
esac
