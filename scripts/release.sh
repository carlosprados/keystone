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

# How long to wait for the docs deployment of the commit about to be tagged.
# The build takes ~40 s; the rest of the budget absorbs a queued runner.
DOCS_APPEAR_TIMEOUT=${DOCS_APPEAR_TIMEOUT:-180}
DOCS_FINISH_TIMEOUT=${DOCS_FINISH_TIMEOUT:-600}

# wait_for_docs blocks until the documentation site has been published for the
# commit being tagged.
#
# Why block rather than report: the point of bumping the version in a commit of
# its own is that the site names the release before the release exists. Tagging
# while that deployment is still in flight is usually harmless, but a tag cannot
# be moved or deleted — the repository rules forbid it — so a *failed* docs
# deployment is worth stopping for. You would otherwise publish binaries beside
# a site still naming the previous version, and the only way back is to cut
# another version.
#
# A run that never appears or never finishes is not treated as failure: a slow
# or queued runner is not a reason to hold a release. That case warns and
# continues. Set SKIP_DOCS_WAIT=1 to skip the wait entirely.
wait_for_docs() {
	local target="$1" version="$2" short="${1:0:7}"

	if [ -n "${SKIP_DOCS_WAIT:-}" ]; then
		echo "==> SKIP_DOCS_WAIT set; not waiting for the docs deployment"
		return 0
	fi
	if ! command -v gh >/dev/null; then
		echo "==> gh not on PATH; cannot check the docs deployment for $short"
		return 0
	fi

	# The run is created a moment after the push, so first wait for it to exist.
	local waited=0 status="" conclusion="" line
	echo "==> waiting for the docs deployment of $short"
	while :; do
		line="$(docs_run_state "$target")"
		status="${line%% *}"
		conclusion="${line#* }"
		[ -n "$status" ] && break
		if [ "$waited" -ge "$DOCS_APPEAR_TIMEOUT" ]; then
			echo "==> warning: no docs run for $short after ${DOCS_APPEAR_TIMEOUT}s; tagging anyway"
			return 0
		fi
		sleep 5
		waited=$((waited + 5))
	done

	waited=0
	while [ "$status" != "completed" ]; do
		if [ "$waited" -ge "$DOCS_FINISH_TIMEOUT" ]; then
			echo "==> warning: the docs deployment of $short is still $status after ${DOCS_FINISH_TIMEOUT}s; tagging anyway"
			return 0
		fi
		sleep 10
		waited=$((waited + 10))
		line="$(docs_run_state "$target")"
		status="${line%% *}"
		conclusion="${line#* }"
	done

	case "$conclusion" in
	success)
		echo "==> docs published for $short"
		;;
	*)
		die "the docs deployment of $short finished as \"$conclusion\", so the site does not name $version.
       Fix the deployment and re-run, or re-run with SKIP_DOCS_WAIT=1 to tag anyway.
       Note the tag cannot be moved afterwards: correcting this later costs a new version."
		;;
	esac
}

# docs_run_state echoes "<status> <conclusion>" for the pages run of a commit,
# or nothing when no run exists yet. A failure to reach the API is reported as
# no run rather than as an error, so a network blip retries instead of aborting.
docs_run_state() {
	gh run list --workflow pages.yml --branch main --limit 20 \
		--json headSha,status,conclusion \
		--jq "[.[] | select(.headSha==\"$1\")] | first | select(. != null) | \"\(.status) \(.conclusion // \"-\")\"" \
		2>/dev/null || true
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

	wait_for_docs "$target" "$version"

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
