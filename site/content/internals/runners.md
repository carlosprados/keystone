+++
title = "Runners"
weight = 33
description = "How processes and containers are actually started, watched and stopped."
+++

# Runners

A runner is the thing that makes a workload exist. Two implementations, one
interface.

## ProcessRunner

The default, and the reason Keystone is viable on small hardware. A component is
just a child process.

**Process groups.** Every component is started with `Setpgid`, so signals reach the
whole tree. A shell script that spawns children is stopped properly rather than
leaving orphans.

**Stopping.** `SIGTERM` to the process group, wait for the timeout, then `SIGKILL`,
then a final 3 s grace. A component that ignores `SIGTERM` still dies.

**Logs.** stdout and stderr are captured and streamed into the agent's log, tagged
with the component name and stream:

```
[runner] component=api stream=stdout msg=listening on :8080
```

**Resource limits.** `RLIMIT_NOFILE` from `[resources].open_files` is applied.
CPU and memory limits for processes are placeholders today — use a container, or a
systemd slice, if you need them enforced.

**Privilege dropping.** See [Process privileges](../../security/process-privileges/).

## ContainerRunner

Two paths, chosen by `[lifecycle.run.container].runtime`:

| Value | Behaviour |
|---|---|
| `auto` *(default)* | Try containerd; fall back to a CLI |
| `containerd` | containerd v2 client only, over its socket |
| `cli`, `nerdctl`, `docker`, `podman` | Shell out to that CLI |

The containerd path talks directly to the socket
(`KEYSTONE_CONTAINERD_SOCKET`, namespace `KEYSTONE_CONTAINERD_NAMESPACE`) and
manages images, snapshots and tasks itself. The CLI path is the pragmatic fallback
for devices where Docker or Podman is already the way things are done.

Container components report `pid = 0` in the API — there is no host PID that means
anything — so their liveness signal is the supervision loop. See the caveat in
[Component state](../../concepts/component-state/).

## Health probes

Three forms, all with `interval`, `timeout` and `failure_threshold`:

```toml
check = "http://127.0.0.1:8080/healthz"   # 2xx is healthy — a redirect is not
check = "https://127.0.0.1:8443/healthz"  # same, over TLS
check = "tcp://127.0.0.1:5432"            # a successful connect is healthy
check = "cmd:/usr/local/bin/check.sh"     # exit 0 is healthy
```

The HTTP probe accepts **200–299 only**. A health endpoint that redirects reads as
unhealthy, which catches a surprising number of misconfigured reverse proxies.

Health interacts with the restart policy:

- `restart_policy = "always"` — after `failure_threshold` consecutive failures the
  runner stops the component, which makes the exit path restart it.
- `on-failure` / `never` — a failing probe is reported (`last_health: unhealthy`)
  but does not by itself trigger a restart. The state is honest; the decision is
  yours.

## Restart policy and backoff

```
attempt 1 → 1s   attempt 2 → 2s   attempt 3 → 4s   …   capped at 60s
```

±25 % jitter, so a device that reboots with several crash-looping components does
not produce a synchronised thundering herd. `max_retries` defaults to 5 for
`always` and `on-failure`; exhausting it is terminal (`failed`).

## The exit contract

When a workload exits and will **not** be restarted, the runner calls back once
with the exit error — `nil` for a clean exit. The agent treats both as terminal:

- error → `failed`
- clean → `stopped`

and in both cases clears the PID, resets the health verdict, deregisters the handle
and releases the runner. Treating a clean exit as "nothing to report" is what used
to freeze a dead component at `running/healthy`.
