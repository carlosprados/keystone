#!/bin/sh
# Pre-start gate for a Keystone install that updates itself.
#
# Runs as ExecStartPre, as root, before every start of the agent. It does two
# things, both of which the agent is not allowed to do itself:
#
#   1. Install a version the agent proposed: copy it out of staging/, verify the
#      copy against the trust bundle with the version already installed, move it
#      into versions/ and point `current` at it. /opt/keystone belongs to root
#      and is read-only to the agent, so a compromised agent can propose a
#      binary but never get one installed that the trust bundle does not vouch
#      for.
#   2. Decide whether the version about to run has had enough chances, and roll
#      back when it has not confirmed.
#
# WHY THIS IS A SHELL SCRIPT AND NOT PART OF THE AGENT
#
# The failure it exists for is an agent binary that does not run at all —
# truncated download, wrong architecture, missing library. Such a binary cannot
# execute its own rollback logic, so the decision cannot live inside it. It also
# cannot live in systemd's StartLimitBurst, which stops restarting and leaves
# the unit dead: on a device nobody can reach, "stopped trying" is the outcome
# being avoided.
#
# So: a small script, no dependencies beyond a POSIX shell, that does not change
# when the agent is updated.
#
# It never sources the state file. That file is written by a process that could
# in principle be compromised, and `.` on it would be arbitrary code execution
# as whoever runs this gate. For the same reason every version name read from it
# is validated before it becomes part of a path: "../../tmp/x" as the confirmed
# version would point `current` at a binary the agent wrote.
#
# It never fails the start. Every step that can fail is guarded and ends in a
# logged refusal, because an ExecStartPre that exits non-zero leaves the device
# with no agent at all.
set -eu

ROOT="${KEYSTONE_ROOT:-/opt/keystone}"
STATE="$ROOT/state/update.env"
CURRENT="$ROOT/current"
MAX_BOOTS="${KEYSTONE_UPDATE_MAX_BOOTS:-3}"

log() { echo "keystone-update-gate: $*" >&2; }

# read_key <KEY> — prints the value or nothing. Parses rather than evaluates.
read_key() {
	[ -f "$STATE" ] || return 0
	sed -n "s/^$1=//p" "$STATE" | tail -n 1 | tr -d '"'
}

# write_state <pending> <boots> <confirmed> <last-failure>. The proposal is
# always cleared: the gate handles it before anything else, on this same start.
write_state() {
	tmp="$STATE.tmp"
	mkdir -p "$(dirname "$STATE")"
	{
		echo "# Written by keystone-update-gate."
		echo "KEYSTONE_UPDATE_PENDING=$1"
		echo "KEYSTONE_UPDATE_BOOTS=$2"
		echo "KEYSTONE_UPDATE_CONFIRMED=$3"
		echo "KEYSTONE_UPDATE_LAST_FAILURE=$4"
		echo "KEYSTONE_UPDATE_PROPOSED="
	} >"$tmp"
	mv "$tmp" "$STATE"
}

# valid_name <name> — a version name is one path component of plain characters.
valid_name() {
	case "$1" in
	'' | .* | *[!A-Za-z0-9._+-]*) return 1 ;;
	*) return 0 ;;
	esac
}

pending="$(read_key KEYSTONE_UPDATE_PENDING)"
boots="$(read_key KEYSTONE_UPDATE_BOOTS)"
confirmed="$(read_key KEYSTONE_UPDATE_CONFIRMED)"
proposed="$(read_key KEYSTONE_UPDATE_PROPOSED)"
last_failure="$(read_key KEYSTONE_UPDATE_LAST_FAILURE)"

if [ -n "$confirmed" ] && ! valid_name "$confirmed"; then
	log "ignoring invalid confirmed version name '$confirmed'"
	confirmed=""
fi
if [ -n "$pending" ] && ! valid_name "$pending"; then
	log "ignoring invalid pending version name '$pending'"
	pending=""
fi

# refuse_proposal <reason> — log it, record it, forget the proposal.
refuse_proposal() {
	log "refusing proposed version '$proposed': $1"
	last_failure="proposed $proposed refused: $1"
	write_state "$pending" "${boots:-0}" "$confirmed" "$last_failure"
	rm -rf "$ROOT/versions/.incoming-gate"
	valid_name "$proposed" && rm -rf "$ROOT/staging/$proposed"
	proposed=""
}

# A proposal from the agent: install it, or refuse it. Either way it is handled
# before the pending logic below, which then counts this start.
if [ -n "$proposed" ]; then
	src="$ROOT/staging/$proposed"
	dst="$ROOT/versions/$proposed"
	tmp="$ROOT/versions/.incoming-gate"
	if ! valid_name "$proposed"; then
		refuse_proposal "invalid version name"
	elif [ ! -f "$src/keystone" ]; then
		refuse_proposal "nothing staged in $src"
	elif ! { rm -rf "$tmp" && mkdir "$tmp" && cp "$src/keystone" "$tmp/keystone"; }; then
		refuse_proposal "could not copy it out of staging"
	else
		# Signature and certificate travel with the binary; they are absent only
		# when the agent runs with verification disabled.
		for f in keystone.sig keystone.crt; do
			if [ -f "$src/$f" ]; then
				cp "$src/$f" "$tmp/$f" || true
			fi
		done
		chmod 0755 "$tmp/keystone" 2>/dev/null || true
		chown -R 0:0 "$tmp" 2>/dev/null || true
		# Verify the COPY, which only root can now change, not the staged files a
		# lingering process owned by the agent could still swap. And verify it
		# with the installed version: code the device already trusts.
		if ! "$CURRENT/keystone" --verify-update "$tmp" >&2; then
			refuse_proposal "verification failed"
		elif [ -e "$dst" ] && ! cmp -s "$tmp/keystone" "$dst/keystone"; then
			refuse_proposal "a different binary is already installed as $proposed"
		elif ! { { [ -e "$dst" ] && rm -rf "$tmp"; } || mv -T "$tmp" "$dst"; }; then
			refuse_proposal "could not move it into versions/"
		else
			if [ -z "$confirmed" ]; then
				# First update on a device that never confirmed anything: whatever
				# runs now is working, by definition, and is where to go back to.
				running="$(basename "$(readlink "$CURRENT" 2>/dev/null)" 2>/dev/null)"
				valid_name "$running" && [ "$running" != "$proposed" ] && confirmed="$running"
			fi
			if ln -sfn "versions/$proposed" "$CURRENT.tmp" && mv -T "$CURRENT.tmp" "$CURRENT"; then
				log "installed proposed version '$proposed'; it runs from this start, on trial"
				pending="$proposed"
				boots=0
				last_failure=""
				write_state "$pending" "$boots" "$confirmed" ""
				rm -rf "$src"
			else
				rm -f "$CURRENT.tmp"
				refuse_proposal "could not point $CURRENT at it"
			fi
		fi
	fi
fi

# No update in flight: the overwhelmingly common case, and it must be silent and
# free. Anything this gate does on an ordinary boot is a new way for an ordinary
# boot to fail.
if [ -z "$pending" ]; then
	exit 0
fi

case "$boots" in
'' | *[!0-9]*) boots=0 ;;
esac

if [ "$boots" -ge "$MAX_BOOTS" ]; then
	# The pending version has had its chances and never confirmed. Roll back.
	if [ -z "$confirmed" ]; then
		# Nothing to go back to. Clearing the pending marker at least stops the
		# counter from growing forever, and leaves the situation visible instead
		# of looping quietly.
		log "version '$pending' failed to confirm after $boots starts, and there is no confirmed version to roll back to; clearing the marker and letting it run"
		write_state "" 0 "" "no confirmed version to roll back to after $pending failed"
		exit 0
	fi

	log "version '$pending' failed to confirm after $boots starts; rolling back to '$confirmed'"
	if ln -sfn "versions/$confirmed" "$CURRENT.tmp" && mv -T "$CURRENT.tmp" "$CURRENT"; then
		write_state "" 0 "$confirmed" "$pending failed to confirm after $boots starts"
	else
		# The rollback itself failed. Say so loudly and leave the state alone:
		# another attempt at the next boot is better than pretending this one
		# worked.
		log "ROLLBACK FAILED: could not point $CURRENT at versions/$confirmed"
		rm -f "$CURRENT.tmp"
	fi
	exit 0
fi

boots=$((boots + 1))
log "starting pending version '$pending' (attempt $boots of $MAX_BOOTS)"
write_state "$pending" "$boots" "$confirmed" "$last_failure"
exit 0
