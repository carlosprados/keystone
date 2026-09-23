#!/bin/sh
# Pre-start gate for a Keystone install that updates itself.
#
# Runs as ExecStartPre, before every start of the agent, and decides one thing:
# whether the version about to run has had enough chances.
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
# as whoever runs this gate.
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

write_state() {
	tmp="$STATE.tmp"
	mkdir -p "$(dirname "$STATE")"
	{
		echo "# Written by keystone-update-gate."
		echo "KEYSTONE_UPDATE_PENDING=$1"
		echo "KEYSTONE_UPDATE_BOOTS=$2"
		echo "KEYSTONE_UPDATE_CONFIRMED=$3"
		echo "KEYSTONE_UPDATE_LAST_FAILURE=$4"
	} >"$tmp"
	mv "$tmp" "$STATE"
}

pending="$(read_key KEYSTONE_UPDATE_PENDING)"
boots="$(read_key KEYSTONE_UPDATE_BOOTS)"
confirmed="$(read_key KEYSTONE_UPDATE_CONFIRMED)"

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
write_state "$pending" "$boots" "$confirmed" "$(read_key KEYSTONE_UPDATE_LAST_FAILURE)"
exit 0
