# Docker Go SDK over `ssh://`: what works and what breaks

Research for ticket `.scratch/v1-spec/issues/03-docker-sdk-over-ssh.md`.

Method: source reading of `moby/moby` v28.5.2 and `docker/cli` v29.7.2 (pinned via
the Go module cache), the OpenSSH `ssh_config(5)` / `sshd_config(5)` man pages,
macOS `paths.h` / `/etc/zprofile` verified on a real Mac, official Docker docs, the
Docker Desktop release notes, and the moby / docker-cli / docker-compose issue
trackers — plus **live experiments** run against a real daemon through the exact
`commandconn` transport that `ssh://` uses.

Date: 2026-08-31. Every issue/PR state below was re-checked against the GitHub API
on that date rather than taken on trust.

---

## Verdict

**Viable as config-only? NO. Viable with a day of code and config? YES — but with
one teardown hole that is not about SSH at all and that you must decide about.**

Four findings, in descending order of cost:

1. **The official Docker Go SDK does NOT speak `ssh://`.** Verified by running it:
   `client.NewClientWithOpts(client.WithHost("ssh://user@host"))` returns **no
   error** and then does a plain TCP dial of the literal string `user@host`.
   `ssh://` lives in `github.com/docker/cli/cli/connhelper`, a different module.
   Upstream tracks this as [moby/moby#46821](https://github.com/moby/moby/issues/46821),
   *"FromEnv should work with an SSH transport set via DOCKER_HOST"* — **OPEN since
   2023-11-15**. So the remote Runner is not "config, not code": it is ~10 lines of
   wiring, a dependency on `docker/cli`, an `openssh-client` binary in the control
   plane image, a keypair, and a `known_hosts`. Small. Not zero. `map.md` currently
   claims zero.

2. **🔴 Teardown is safe for `docker stop`, and NOT safe for engine shutdown on
   Docker Desktop.** The `docker stop -t 30` chain is bulletproof: enforced entirely
   daemon-side in `daemon/stop.go` under `context.WithoutCancel`, so the transport
   cannot shorten it, skip it, or abort it — verified by source *and* by a live run
   (trapped SIGTERM, 5s of teardown work, exit 0). **But** Docker Desktop
   deliberately runs the daemon with a **2-second shutdown timeout** and, until
   recently, stamped **`StopTimeout: 1`** onto every container it created
   ([moby/moby#52775](https://github.com/moby/moby/issues/52775), closed;
   maintainer @vvoland: *"this is deliberate choice on the Docker Desktop side to
   make sure containers don't block VM shutdown"*; @corhere reproduced it **on
   Docker for Mac**). Meaning: whenever the Docker Desktop engine or VM goes down —
   Mac reboot, Docker Desktop restart, auto-update, sleep — an in-flight Run gets
   ~2 seconds, not 30, and **the `git commit && git push` trap does not finish.**
   Our design survives the `StopTimeout: 1` bug *only because we pass `-t 30`
   explicitly*. It does not survive an engine shutdown at all.

3. **Liveness detection over `ssh://` is absent, and worse than it looks.**
   `commandConn.SetDeadline`/`SetReadDeadline`/`SetWriteDeadline` are **no-ops**;
   the SDK's hijack path skips TCP keepalive because the conn is not a
   `*net.TCPConn`; and the connhelper sets `ConnectTimeout` but never
   `ServerAliveInterval`. Reproduced live: a *silently* wedged transport leaves a
   log-follow stream blocked **forever, with no error**. A loudly-dead transport
   errors immediately; a silent one does not. Fixable with `ServerAliveInterval`
   plus an application watchdog — but it has to be designed in.

4. **Docker Desktop on macOS has no headless story.** It is a per-user GUI app;
   "Start Docker Desktop when you sign in to your computer" is a *login* event, not
   a boot event, and is off by default. A request for boot-time start
   ([docker/for-mac#4388](https://github.com/docker/for-mac/issues/4388)) was closed
   and locked without implementation. An unattended Mac mini Runner therefore needs
   macOS auto-login (which fights FileVault), or a different daemon (Colima).
   And the `ssh host docker …` **PATH** problem is the *default* behaviour, not an
   edge case — mechanism verified locally, see §5.

The Journal itself is not at risk from any of the SSH findings, because the
container pushes its own Journal at teardown. That property is now load-bearing:
**do not build the Journal from the control plane's log stream.**

---

## 1. Teardown signal delivery (`docker stop --time=30`)

### 1.1 The grace period is daemon-side. The transport is irrelevant.

`moby/moby` `daemon/stop.go`, `func (daemon *Daemon) containerStop`:

```go
// containerStop sends a stop signal, waits, sends a kill signal. It uses
// a [context.WithoutCancel], so cancelling the context does not cancel
// the request to stop the container.
func (daemon *Daemon) containerStop(ctx context.Context, ctr *container.Container, options containertypes.StopOptions) (retErr error) {
	// Cancelling the request should not cancel the stop.
	ctx = context.WithoutCancel(ctx)
```

Sequence, verbatim from that function:

1. `stopSignal = ctr.StopSignal()`; `stopTimeout = ctr.StopTimeout()`; then
   `if options.Timeout != nil { stopTimeout = *options.Timeout }` —
   **an explicit `-t` always wins** over the container's configured `StopTimeout`.
2. `daemon.killPossiblyDeadProcess(ctr, stopSignal)` — signal to the container init.
3. `subCtx, cancel = context.WithTimeout(ctx, wait)`, `wait = stopTimeout seconds`.
4. `<-ctr.Wait(subCtx, WaitConditionNotRunning)` — exits in time → return.
5. Else log `"Container failed to exit within %s of signal %d - using the force"`
   and `daemon.Kill(ctr)`.

Defaults:

- `defaultStopSignal = syscall.SIGTERM` — `container/container.go:55`
- `defaultStopTimeout = 10` seconds on Linux — `container/container_unix.go:26`
  (30 on Windows). **Always pass `Timeout` explicitly.**

Client side is one query parameter (`client/container_stop.go`:
`query.Set("t", strconv.Itoa(*options.Timeout))`), and the route handler
(`api/server/router/container/container_routes.go`, `postContainersStop`) just does
`strconv.Atoi(r.Form.Get("t"))`. **There is no client-side timing logic at all.**
Nothing about `ssh://` can alter the signal, the grace, or the ordering.

### 1.2 Live verification, through the `commandconn` transport

Container ran `trap 'echo TRAPPED_SIGTERM; sleep 5; echo TEARDOWN_DONE; exit 0' TERM`,
stopped with `Timeout: 30`:

```
LOG 2026-08-31T18:46:28.317848169Z TRAPPED_SIGTERM
LOG 2026-08-31T18:46:33.324921551Z TEARDOWN_DONE
STOP returned after 6.184s
WAIT exit=0
```

SIGTERM reached PID 1, the trap ran 5s of teardown, exit code **0** (not 137), and
`ContainerStop` returned as soon as the container exited rather than burning the
full 30s. Exactly the contract the entrypoint needs.

### 1.3 🔴 Docker Desktop truncates the grace in two ways

**(a) `StopTimeout: 1` on every container it created.**
[moby/moby#52775](https://github.com/moby/moby/issues/52775), *"StopTimeout=1
inexplicably"*, filed 2026-06-05, labels `kind/bug` + `platform/desktop`, now
CLOSED. Key comments:

- @corhere (contributor): *"Looks like an issue with Docker Desktop. **I can
  reproduce it on Docker for Mac**, but not with Docker Engine on a Linux box."*
- @thaJeztah (maintainer): *"default daemon **shutdown timeout is set to 2
  seconds** unless configured in daemon.json / container **StopTimeout is set to 1
  second** unless configured (normal default is 10 seconds + 5 seconds grace)."*
- @vvoland: *"looks like this is **deliberate choice on the Docker Desktop side to
  make sure containers don't block VM shutdown**. I will see what I can do to avoid
  this."* Fix shipped in Docker Desktop 4.81/4.82.

Docker Desktop release notes, **4.82.0 (2026-07-13)**, verbatim:

> Fixed a bug where containers created by Docker Desktop had their stop timeout set
> to 1 second instead of the Docker Engine default, causing `docker stop` to
> terminate processes too quickly.

Follow-up in moby: [moby/moby#53146](https://github.com/moby/moby/pull/53146)
*"daemon: Add configurable default container stop timeout"*, **MERGED 2026-07-27**,
so Desktop can set a daemon default without stamping every container.

**Our design is immune to this specific bug, but only because we pass `-t 30`
explicitly.** Anything that relies on the container's configured `StopTimeout` —
`docker restart` without `-t`, `compose down`, the engine's own shutdown path —
would have got 1 second.

**(b) The engine shutdown path ignores stop timeouts entirely (until very recently).**
Docker Desktop release notes, **4.88.0 (2026-08-24)**, verbatim:

> Fixed container stop timeouts and `unless-stopped` restart policies not being
> honored during engine shutdown.

Combined with the documented 2-second daemon shutdown timeout, this is the real
hole: **a Run that is in flight when the Docker Desktop engine or VM stops does not
get its 30 seconds and loses its Journal push.** Triggers for an engine shutdown on
a Mac mini: reboot, Docker Desktop auto-update, Mac sleep (§5.5), the user quitting
Docker Desktop, and Resource Saver.

Mitigations, in order of laziness:

1. **Pin Docker Desktop ≥ 4.88.0** on the Runner and **turn auto-update off.**
   (Latest at time of writing: 4.88.1, 2026-08-25.)
2. **Disable sleep** and **disable Resource Saver** (§5.5).
3. Accept that a Run interrupted by an engine shutdown loses its Journal, and make
   that visible: on control-plane startup, reconcile containers by Run-id label and
   mark any Run whose container vanished without an exit record as `lost`.

### 1.4 Two ways teardown breaks that have nothing to do with SSH

Both belong in ticket 06 (entrypoint contract); flagging here because they defeat
the same chain:

- **A shell trap only fires between commands, or during `wait`.** If PID 1 is
  `sh -c "claude --long-thing"`, SIGTERM is recorded but the handler does not run
  until `claude` returns — so the 30s grace buys nothing and SIGKILL lands with the
  trap never having executed. The entrypoint must run the agent as
  `agent & wait $!` (or use `exec` + a supervisor), not as a plain foreground
  command.
- **An OOM kill is SIGKILL.** `--memory` exceeded → the kernel kills the process,
  container exits 137, `.State.OOMKilled=true`, **and no trap runs.** Size the
  memory limit with headroom, and treat `OOMKilled` as a distinct Run outcome.

### 1.5 What a dead link does and does not cost

- **A dead link mid-stop does not abort the stop.** `context.WithoutCancel` means
  the daemon completes SIGTERM → 30s → SIGKILL even if the pipe dies the instant
  the request lands. The control plane loses the *answer*, not the *action*. It
  must reconcile with `ContainerInspect`, not retry blindly.
- **A dead link *before* the stop means no stop at all.** `map.md` says "time is a
  container kill via a context deadline". If the SSH link is down when the deadline
  fires, the kill never reaches the daemon and the Run runs forever.
  → **The Run must also self-limit from inside the container**, so teardown never
  depends on the control plane being reachable. Feeds ticket 06.
- `POST /containers/{id}/stop` **blocks for up to the full grace period**. Over
  `ssh://` that is one SSH channel held open 30s+. Never put a client-side HTTP
  timeout shorter than `grace + slack` on that call (see §3.6 on `WithTimeout`).

---

## 2. Log streaming over SSH, link drops, orphans, reattach

### 2.1 The container's life is not tied to the API connection

`daemon/logs.go` `ContainerLogs`, on context cancellation, touches only the reader:
`defer logs.ConsumerGone()`. No container-lifecycle call anywhere in that path.

Verified live — killing the transport process mid-follow:

```
LOG tick 4
>>> killing the transport process (simulating SSH link drop)
>>> log stream returned after link kill: err=command [docker system dial-stdio] has exited with signal: killed, ...
>>> container still Running=true after transport death
```

No orphan risk in the "container dies" sense. The opposite risk is real: the
container **keeps running** and the control plane must find it again. **Label every
container with the Run id at create time** and reconcile with a `ContainerList`
label filter on startup and after every transport error.

### 2.2 Reattach works; `since` is nanosecond-precision, inclusive, and a *seek hint*

`client/container_logs.go` passes `since`/`until` through
`api/types/time.GetTimestamp`, which emits `fmt.Sprintf("%d.%09d", t.Unix(), t.Nanosecond())`
— full nanosecond resolution — and `daemon/logs.go` parses it back with
`timetypes.ParseTimestamps`. Params in use on `GET /containers/{id}/logs`:
`stdout`, `stderr`, `since`, `until`, `timestamps`, `details`, `follow`, `tail`.
(Note: the Engine API OpenAPI spec still types `since`/`until` as plain `integer`
seconds — **the spec is stale**; the daemon's own parser documents
`"%d.%09d"` seconds.nanoseconds.)

Live reattach after the transport died, resuming from the last line seen:

```
>>> reattaching with Since = 2026-08-31T18:47:02.929796505Z
RE 2026-08-31T18:47:02.929796505Z tick 4      <-- duplicate; boundary is inclusive
RE 2026-08-31T18:47:03.931170260Z tick 5
RE 2026-08-31T18:47:04.938072808Z tick 6
```

And the filter has a subtlety worth knowing —
`daemon/logger/loggerutils/logfile.go`, `forwarder.Do`:

```go
if !fwd.since.IsZero() {
    if msg.Timestamp.Before(fwd.since) {
        continue
    }
    // We've found our first message with a timestamp >= since. As message
    // timestamps might not be monotonic, we need to skip the since check for all
    // subsequent messages so we do not filter out later messages which happen to
    // have timestamps before since.
    fwd.since = time.Time{}
}
```

So `since` is **inclusive** and **self-disables after the first match** — it is a
seek hint, not a strict filter. There is no server-side cursor, offset or sequence
number in the Engine API.

**Resume recipe:** always request `Timestamps: true`; persist the last-seen
RFC3339Nano timestamp; resume with `Since: <lastTs>`; de-duplicate client-side on
`(timestamp, stream, line)` **for the boundary timestamp only**. Do not resume at
`lastTs + 1ns` — that silently drops any line sharing the same nanosecond.

Verified separately: after the container exits, `ContainerLogs` with `Tail: "5"`
replays correctly, so a post-mortem read is always available as a backstop. Caveat:
log rotation (`max-size`/`max-file`) can still eat history behind your resume point.

### 2.3 🔴 The dangerous case: a *silent* link stall

`cli/connhelper/commandconn/commandconn.go`:

```go
func (*commandConn) SetDeadline(t time.Time) error {
	logrus.Debugf("unimplemented call: SetDeadline(%v)", t)
	return nil
}
func (*commandConn) SetReadDeadline(t time.Time) error { /* same no-op */ }
func (*commandConn) SetWriteDeadline(t time.Time) error { /* same no-op */ }
```

Maintainer confirmation on
[docker/compose#10255](https://github.com/docker/compose/issues/10255#issuecomment-1431053741)
(@milas): *"those options aren't supported for SSH connections but just mean **the
deadlines won't be respected**."*

And `moby/moby` `client/hijack.go`, `setupHijackConn`:

```go
// When we set up a TCP connection for hijack, there could be long periods
// of inactivity ... Setting TCP KeepAlive on the socket connection will
// prohibit ECONNTIMEOUT unless the socket connection truly is broken
if tcpConn, ok := conn.(*net.TCPConn); ok {
    _ = tcpConn.SetKeepAlive(true)
    _ = tcpConn.SetKeepAlivePeriod(30 * time.Second)
}
```

Over `ssh://` the conn is a `*commandConn`, so **that assertion never succeeds and
the keepalive is never set.** The protection the SDK authors wrote specifically for
long-idle streams does not apply to us.

Live reproduction (`SIGSTOP` on the transport = a link that stops moving data
without closing):

```
>>> SIGSTOP-ing transport (silent link hang simulation)
>>> RESULT: stream still blocked 45s after silent transport hang; NO error, NO deadline fired
```

Meanwhile the connhelper adds exactly one ssh option
(`cli/connhelper/connhelper.go`, `addSSHTimeout`): `-o ConnectTimeout=30`, which
only covers **connection establishment**. Per `ssh_config(5)`,
`ServerAliveInterval` defaults to **0 (disabled)** and `ServerAliveCountMax` to
**3**. So out of the box there is no keepalive at any layer.

**Mitigations, all three required:**

1. **`ServerAliveInterval 15` / `ServerAliveCountMax 3`** — either in the control
   plane's `~/.ssh/config`, or programmatically via
   `connhelper.GetConnectionHelperWithSSHOpts(host, []string{"-o", "ServerAliveInterval=15", ...})`
   (see §3.5). ssh then disconnects ~45s after the peer goes quiet, which surfaces
   as an EOF the transport *does* report — the loud-kill experiment proves that
   path works. **This is the single most important config line in the design.**
2. **`http.Transport.ResponseHeaderTimeout`** — verified to be implemented with a
   `time.Timer` in Go's `net/http` (`transport.go:3078`), so it works despite the
   no-op deadlines. Protects the request phase only.
3. **An application-level watchdog** on the log stream: no bytes and no
   container-state change for N seconds → tear it down and reattach with `since`.
   The only thing that covers a stalled *body*.

### 2.4 The daemon will not close it on you

`cmd/dockerd/daemon.go:189` sets only `ReadHeaderTimeout: 5 * time.Minute` on the
API server — no `ReadTimeout`, `WriteTimeout` or `IdleTimeout`. The daemon holds a
follow stream open indefinitely. Failures always originate in the middle (SSH),
never at the daemon.

### 2.5 `ContainerWait` has the same exposure

`POST /containers/{id}/wait` is a long-poll held open for the entire Run. Its own
source comments that it *"does not work well with HTTP proxies… at any time, the
proxy could cut off the response stream"* — an `ssh` pipe is exactly such a
middlebox. **Treat a `ContainerWait` error as "unknown, go re-inspect", never as
"the Run failed."** Prefer polling `ContainerInspect` for the authoritative exit
code and `OOMKilled` flag.

---

## 3. How the SDK actually connects to `ssh://user@host`

### 3.1 The SDK doesn't. `docker/cli` does. (verified by running it)

```
construct err: <nil>
DaemonHost()="ssh://nonexistent-user@nonexistent-host-xyz"
Ping err: error during connect: Head "http://nonexistent-user%40nonexistent-host-xyz/_ping":
          dial tcp: lookup nonexistent-user@nonexistent-host-xyz: no such host
```

Why: `client.WithHost` (`client/options.go:63`) calls
`sockets.ConfigureTransport(transport, "ssh", "user@host")`, and
`go-connections/sockets/sockets.go` has no `ssh` case — it falls to `default:` and
installs a plain `net.Dialer`. **No error, wrong behaviour.** A control plane that
just sets `DOCKER_HOST=ssh://…` and calls `client.FromEnv` fails at runtime with a
DNS error that reads like a network problem and is not.

Upstream: [moby/moby#46821](https://github.com/moby/moby/issues/46821) — **OPEN
since 2023-11-15**. @thaJeztah: *"That `connhelper` package is currently part of
the `docker/cli` repository… means that we currently can't add this functionality
to the API-client itself."* Also [docker/cli#3480](https://github.com/docker/cli/issues/3480).

### 3.2 The wiring you must copy

`docker/cli` `cli/context/docker/load.go`, `func (ep *Endpoint) ClientOpts()`:

```go
helper, err := connhelper.GetConnectionHelper(ep.Host)
...
result = append(result,
    client.WithHTTPClient(&http.Client{
        // No TLS, and no proxy.
        Transport: &http.Transport{DialContext: helper.Dialer},
    }),
    client.WithHost(helper.Host),        // "http://docker.example.com" — a dummy
    client.WithDialContext(helper.Dialer),
)
```

🔴 **Order is load-bearing.** `WithHost` runs `ConfigureTransport`, which for a
non-unix scheme **overwrites `Transport.DialContext`** with a TCP dialer *and* sets
`tr.Proxy = http.ProxyFromEnvironment`. `WithDialContext(helper.Dialer)` must come
*after* to put the ssh dialer back. Reorder these and your ssh transport is silently
clobbered into the failure in §3.1.

Two side effects of that dummy host worth knowing:

- **`HTTP_PROXY`/`HTTPS_PROXY` in the control plane's environment will be applied**
  to the (fake) `http://docker.example.com`. Coolify environments often set these.
  Set `Proxy: nil` on your transport explicitly. Related:
  [docker/cli#5797](https://github.com/docker/cli/issues/5797) (open),
  [docker/cli#2917](https://github.com/docker/cli/issues/2917) (closed).
- **`DaemonHost()` lies** — it returns `http://docker.example.com`, so you cannot
  tell which Runner a client points at by asking it.
  [docker/cli#6164](https://github.com/docker/cli/issues/6164) (open). Track the
  Runner name yourself.

This recipe also *replaces* the SDK's default `http.Client`, discarding moby's
pooling defaults (`MaxIdleConns: 6`, `IdleConnTimeout: 30s`, set in
`client/client.go:defaultHTTPClient` referencing
[moby/moby#45539](https://github.com/moby/moby/issues/45539)). With a bare
`&http.Transport{}` you get Go's defaults: `DefaultMaxIdleConnsPerHost = 2` and
`IdleConnTimeout = 0` (**never expire**) — so up to 2 idle `ssh` processes linger
indefinitely. Set both explicitly, and `defer cli.Close()`
(`baseTransport.CloseIdleConnections()`); the CLI historically failed to and leaked
— [docker/cli#3899](https://github.com/docker/cli/pull/3899) (open).

### 3.3 What it shells out to — exact argv

Captured from a live debug run of `connhelper.GetConnectionHelper`:

| `DOCKER_HOST` | argv |
|---|---|
| `ssh://runner@macmini` | `ssh -l runner "-o ConnectTimeout=30" -T -- macmini docker system dial-stdio` |
| `ssh://runner@macmini:2222/Users/runner/.docker/run/docker.sock` | `ssh -l runner -p 2222 "-o ConnectTimeout=30" -T -- macmini docker '--host=unix:///Users/runner/.docker/run/docker.sock' system dial-stdio` |
| `ssh://macmini` | `ssh "-o ConnectTimeout=30" -T -- macmini docker system dial-stdio` |

(`-o ConnectTimeout=30` really is one argv element with an embedded space —
deliberate, asserted in `connhelper_test.go::TestSSHFlags`. Verified that OpenSSH
parses it, and that a genuinely bad option name is rejected, so the parse is real.)

`-T` is deliberate — `disablePseudoTerminalAllocation`, commented in source as
*"prevent SSH from executing as a login shell"*. That comment is the direct cause of
the Docker-Desktop-on-macOS PATH failure in §5.2.

**The remote command is bare `docker`, with no absolute path**, and it is a *CLI*
subcommand, not a daemon endpoint. So the remote host needs the `docker` **CLI**
installed and on the non-interactive `PATH`. There is no override:
[docker/cli#5626](https://github.com/docker/cli/issues/5626) *"Remove hardcoded
`docker` CLI command name for SSH hosts"* — **OPEN**; the proposed
`DOCKER_SSH_REMOTE_BINARY` PR #5627 is unmerged.

### 3.4 `docker system dial-stdio`

`cli/command/system/dial_stdio.go`:

```go
cmd := &cobra.Command{
    Use:    "dial-stdio",
    Short:  "Proxy the stdio stream to the daemon connection. Should not be invoked manually.",
    Hidden: true,
    ...
}
```

- **Hidden, not experimental.** Registered unconditionally; its docs page is an
  auto-generated stub not linked from the command index. We depend on an
  effectively-undocumented interface.
- It dials **whatever endpoint the remote CLI resolves** (`dockerCLI.Client().Dialer()`),
  i.e. the remote user's active `docker context` is honoured. Important for Docker
  Desktop (§5.3).
- Requires the conn to implement `halfCloser` (`CloseRead` + `CloseWrite`), else
  `"the raw stream connection does not implement halfCloser"`.
- **Minimum remote version: Docker 18.09** — stated in the `GetConnectionHelper`
  docstring and baked into the runtime error string. Introduced by
  [docker/cli#1014](https://github.com/docker/cli/pull/1014), merged 2018-08-14.
- Manual probe, useful for ticket 08:
  ```
  DOCKER_HOST=ssh://macmini docker system dial-stdio <<EOF
  GET /_ping HTTP/1.1
  Host: example.com

  EOF
  ```

### 3.5 Requirements on the CLIENT

- **An OpenSSH `ssh` binary on `$PATH`.** `commandconn.New` does
  `exec.CommandContext(ctx, "ssh", args...)`. If absent:
  `exec: "ssh": executable file not found in $PATH` (verified). No
  `golang.org/x/crypto/ssh` fallback; a request to add one is
  [docker/cli#6572](https://github.com/docker/cli/issues/6572), **OPEN, no
  maintainer response**. → **A `FROM scratch`/distroless control-plane image is
  impossible.** Concrete, non-obvious cost of the "free" remote Runner.
- **No minimum ssh version is enforced.** Only `-l`, `-p`, `-o ConnectTimeout=30`,
  `-T`, `--` are passed — all ancient options. Any OpenSSH of the last decade works.
- **`~/.ssh/config` is fully honoured** (no `-F` is passed, so it invokes the real
  `ssh` with a bare hostname). This is the escape hatch for everything:
  `ServerAliveInterval`, `ControlMaster`, `IdentityFile`, `IdentitiesOnly`,
  `ProxyJump`, `StrictHostKeyChecking`. Caveat: `-l`/`-p` from the URL **override**
  `User`/`Port` from the config file.
- **Programmatic ssh flags exist**:
  `connhelper.GetConnectionHelperWithSSHOpts(daemonURL, sshFlags []string)` — added
  by [docker/cli#2541](https://github.com/docker/cli/pull/2541) (merged 2020-11-17)
  and **never used by docker/cli itself**, so there is no env/CLI way to reach it,
  but our Go control plane can call it directly. Source warning: *"Command does not
  currently perform sanitization or quoting on the sshFlags and callers are expected
  to sanitize this argument."* Sanitize them.
- **`ssh://user:password@host` is rejected outright** —
  `"invalid SSH URL: plain-text password is not supported"` (verified). Same for
  query params and fragments. Official docs agree (`docs/reference/dockerd.md`):
  *"you need to set up `ssh` so that it can reach the remote host with public key
  authentication. Password authentication is not supported. If your key is
  protected with passphrase, you need to set up `ssh-agent`."*
- **`ControlMaster` is NOT used by default and is a trap if you turn it on.**
  History: added by [docker/cli#2132](https://github.com/docker/cli/pull/2132)
  (2019-10), **reverted by [docker/cli#2303](https://github.com/docker/cli/pull/2303),
  MERGED 2020-02-13**, and [docker/cli#4699](https://github.com/docker/cli/pull/4699)
  *"resurrect ssh multiplexing"* has been **OPEN since 2023-12-06** and unmerged.
  Users who enable it in their own `~/.ssh/config` hit sshd's session cap:
  [docker/compose#11677](https://github.com/docker/compose/issues/11677) —
  `mux_client_request_session: session request failed: Session open refused by peer`,
  sshd logging `error: no more sessions`. Per `sshd_config(5)`, **`MaxSessions`
  defaults to 10** *per network connection* — which only bites with multiplexing.
  Without multiplexing you instead hit **`MaxStartups`, default `10:30:100`**: at 10
  concurrent *unauthenticated* connections sshd starts refusing ~30% of new ones.
  **Neither setting is free.** Cap `MaxConnsPerHost` on your transport, and if you
  do enable `ControlMaster`, raise `MaxSessions` on the Mac.
- `c.cmd.Env = os.Environ()` — the whole environment is inherited, so
  **`SSH_AUTH_SOCK` and `HOME` propagate**; agent-based auth works.
- `setPdeathsig(cmd)` sets `Pdeathsig: SIGKILL` **on Linux only**
  (`pdeathsig_linux.go`) — our control plane is Linux, so its `ssh` children die
  with it. `createSession` sets `Setsid: true` (`session_unix.go`, for
  `ProxyCommand`, docker/cli#1707).
- `ctx = context.WithoutCancel(ctx)` in `commandconn.New` — the ssh process is
  deliberately not killed by the caller's context; the `http.Client` owns its
  lifetime. Added by [docker/cli#3900](https://github.com/docker/cli/pull/3900)
  (merged 2022-12-08) to fix ssh processes being killed while still pooled
  ([docker/compose#9448](https://github.com/docker/compose/issues/9448)), hardened
  by [docker/cli#5816](https://github.com/docker/cli/pull/5816) (2025-02-11).
  **Verified live that it still reaps correctly**: cancelling a `ContainerLogs`
  follow took the transport process count 1 → 0 within 6s. Budget for teardown
  latency though: `kill()` is SIGTERM then SIGKILL after **3s**, and `handleEOF`
  blocks up to **10s** on `cmd.Wait()` before returning
  `"command %v did not exit after %v"`.
- **Pin the API version.** [docker/cli#6125](https://github.com/docker/cli/issues/6125)
  (**OPEN**, filed by maintainer @thaJeztah) reports negotiation intermittently
  reporting the wrong version over ssh, with two dials for one `docker version`.
  Use `client.WithAPIVersion("1.4x")` rather than trusting negotiation.

### 3.6 Do NOT set a client timeout

The SDK's default `http.Client` has **no `Timeout`** — only `Transport` and
`CheckRedirect` (`client/client.go:defaultHTTPClient`). Good: a followed log stream
is not cut by the SDK. **Never call `client.WithTimeout(d)`** — it sets
`http.Client.Timeout`, which covers reading the response *body* and therefore hard-
kills long streams and any `stop` that uses its full grace. `docker/cli` itself
deliberately does not set one on the ssh path.

### 3.7 SSH from inside the control plane's own container

All real, all ours to solve:

1. **`known_hosts`.** `ssh_config(5)`: `StrictHostKeyChecking` defaults to `ask`,
   and *"new host keys will be added … only after the user has confirmed"*. A
   container has no controlling terminal, so the prompt cannot be answered and the
   connection **fails**. Either bake/mount a `known_hosts` entry (`ssh-keyscan`), or
   set `StrictHostKeyChecking accept-new` (TOFU — accepts a *new* key once, still
   refuses a *changed* one). Do **not** use `StrictHostKeyChecking no`: it accepts
   changed keys, i.e. accepts MITM.
2. **The key.** `IdentityFile` defaults to `~/.ssh/id_ed25519` etc. `HOME` must be
   set, `~/.ssh` must be `0700` and the key `0600` or ssh refuses it. Coolify mounts
   secrets as files — check the resulting mode.
3. **Passphrase-protected keys are a hard no headless.** ssh will prompt; with no
   tty and no `SSH_ASKPASS`/`DISPLAY` it fails. Options: (a) a passphrase-less key
   mounted read-only, (b) forward an agent socket (`SSH_AUTH_SOCK` propagates, but
   the socket must exist inside the container's mount namespace and survive
   restarts — fragile under Coolify). **Recommend (a).** Also set
   `BatchMode=yes` so any prompt fails fast instead of hanging.
4. **Restrict the key on the remote.** It only needs to do one thing:
   `restrict,command="docker system dial-stdio" ssh-ed25519 AAAA…`
   (`restrict` implies no agent/port/pty/X11 forwarding). **Caveat:** a forced
   `command=` breaks the socket-path URL form, which sends a *different* remote
   command. Pick one. Hardening, not correctness.
5. **The remote user must reach the daemon socket without `sudo`**, because
   `dial-stdio` runs non-interactively. Fine on Docker Desktop (socket owned by the
   login user).

---

## 4. Does the full API surface work over that transport?

**Yes — verified end to end**, live, through the identical `commandconn` transport
(`GetCommandConnectionHelper("docker", "system", "dial-stdio")`, which differs from
`ssh://` only by the absence of the ssh hop; the `net.Conn` implementation,
half-close semantics, process-per-connection model and deadline no-ops are all the
same code):

```
PING ok api=1.54 os=linux
PULL ok 784 bytes of progress json in 2.027s          <- streaming image pull
CREATE ok id=e73cb3f4cc6b warnings=[]
START ok
INSPECT Memory=268435456 NanoCpus=500000000           <- limits round-trip intact
LOG ... (follow, with timestamps, many lines)
STOP returned after 6.184s                            <- SIGTERM, trap ran, exit 0
WAIT exit=0
--- replay last 5 lines after exit ---                <- post-mortem log read
REMOVE ok
```

and the hijacked path separately:

```
ATTACH ok (hijack over commandconn)
ATTACH-OUT(fd=1): GOT:hello-over-the-wire
CloseWrite ok (half-close supported over commandconn)
```

Why this is structurally safe rather than lucky:

- **Resource limits are request body, not transport.** `HostConfig.Resources`
  (`Memory`, `NanoCPUs`, `CPUShares`, `CPUQuota`, `MemoryReservation`,
  `MemorySwap`, `PidsLimit` — `api/types/container/hostconfig.go:373-408`) is JSON
  in `POST /containers/create`. Nothing transport-specific.
- **Hijack needs `CloseWrite`**, and `commandConn` implements `CloseRead`/`CloseWrite`
  explicitly — exactly what `setupHijackConn` looks for (`types.CloseWriter`) and
  what `dial-stdio` itself requires.
- **Image pull happens on the remote daemon.** The client only streams progress
  JSON back; registry bandwidth is the Mac mini's.

Caveats on this surface:

- Registry credentials still travel from the **client** in `X-Registry-Auth`, so a
  private base image means the control plane holds the registry credential.
  Unchanged from local, but worth stating.
- **Windows remote hosts are unsupported** over `ssh://` (a named pipe has
  `CloseWrite` but not `CloseRead`, so `dial-stdio` errors) —
  [docker/cli#4718](https://github.com/docker/cli/issues/4718), open. Irrelevant to
  a macOS Runner; noted so nobody proposes a Windows Runner later.
- Large-body operations (`docker build` context, `ContainerCopyTo`) traverse the
  pipe and are slow. We do none of them.

---

## 5. Docker Desktop on macOS

### 5.1 Licensing — not a blocker

Free for personal use, education, non-commercial open source, and small businesses
(**fewer than 250 employees AND less than $10M annual revenue**); paid above that.
Personal machine + small project = free.

### 5.2 🔴 The `PATH` trap — this is the default, not an edge case

`ssh host docker system dial-stdio` is a **non-interactive, non-login** command
(the connhelper forces `-T`; the source comment says so explicitly). `sshd` runs it
as `$SHELL -c '<command>'`. Verified the mechanism on a real Mac:

| Check | Result |
|---|---|
| `_PATH_STDPATH` in the macOS SDK `paths.h` | `"/usr/bin:/bin:/usr/sbin:/sbin"` — **no `/usr/local/bin`** |
| `/etc/zshenv` | **does not exist** |
| `/etc/zprofile` header | `# System-wide profile for interactive zsh(1) login shells.` — and it is the **only** place `path_helper` is invoked |
| `/etc/paths` | lists `/usr/local/bin` first — but only `path_helper` applies it |

Docker Desktop installs its binaries in `/Applications/Docker.app/Contents/Resources/bin`
and symlinks them into `/usr/local/bin`. So for a non-login non-interactive zsh,
`path_helper` never runs, `PATH` stays `/usr/bin:/bin:/usr/sbin:/sbin`, and
`docker` is not found. **Adding a file to `/etc/paths.d` does NOT fix this.**

Reported and never fixed:
- [docker/cli#3045](https://github.com/docker/cli/issues/3045) *"Simplify setup
  required for remote DOCKER_HOST over SSH"* — **OPEN since 2021-04-09**.
- [docker/for-mac#4382](https://github.com/docker/for-mac/issues/4382) —
  `stderr=bash: docker: command not found`, exit 127. Closed as stale.
- [docker/cli#2115](https://github.com/docker/cli/issues/2115) —
  `zsh:1: command not found: docker`. Closed, no fix.

It surfaces as the connhelper's generic
`"make sure the URL is valid, and Docker 18.09 or later is installed on the remote host"`,
which points at entirely the wrong thing.

**Fixes, cheapest first:**
1. `echo 'export PATH="/usr/local/bin:$PATH"' >> ~/.zshenv` on the Mac — zsh sources
   `~/.zshenv` for *every* invocation including non-login non-interactive. Only valid
   if the SSH user's login shell is zsh (`dscl . -read /Users/$USER UserShell`).
2. Shell-independent: `SetEnv PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin` in
   a `Match User` block in `/etc/ssh/sshd_config` (OpenSSH 7.8+).
3. Or `~/.ssh/environment` + `PermitUserEnvironment PATH` (from the #3045 thread).
4. Or sidestep it entirely — see §6's socket-forwarding alternative, which needs no
   `docker` binary on the Mac at all.

**This is the first thing ticket 08 should check.**

### 5.3 Which socket, which context

Official: *"The Docker CLI relies on the current context to retrieve the socket
path, the current context is set to `desktop-linux` on Docker Desktop startup."*
Because `dial-stdio` uses the resolved client dialer (§3.4), a remote CLI on
`desktop-linux` dials `unix:///Users/<user>/.docker/run/docker.sock` and proxies it.
**`/var/run/docker.sock` is not required for `ssh://`** — it only matters for
clients that hardcode the default path, and it is behind the Settings toggle
*"Allow the default Docker socket to be used"* (requires a password; recreated at
boot by a launchd job with the user's home path hardcoded, i.e. single-user).

Two ways to be explicit, both verified in §3.3's argv table:
- `DOCKER_HOST=ssh://runner@macmini/Users/runner/.docker/run/docker.sock`
  → remote runs `docker --host=unix://… system dial-stdio`, bypassing contexts.
- or set `DOCKER_CONTEXT`/`DOCKER_HOST` in the remote user's `~/.zshenv`.

Official docs confirm the path form: *"You can optionally specify the location of
the socket by appending a path component to the end of the SSH address."*

### 5.4 🔴 No headless story — this is the biggest structural risk

Docker Desktop is a **per-user GUI application**; the VM is started by the logged-in
user's Docker Desktop process, and the socket lives under `/Users/<user>/.docker/run/`.
Therefore:

- The SSH user **must be the same macOS user** that runs Docker Desktop.
- Autostart is a **login** event, not a boot event. The setting is worded *"Start
  Docker Desktop when you sign in to your computer"*, and it is **disabled by
  default**.
- With nobody signed in to the GUI, Docker Desktop is not running.
  [docker/for-mac#3567](https://github.com/docker/for-mac/issues/3567): logging out
  stops the engine and containers; relaunching over SSH gives
  `LSOpenURLsWithRole() failed with error -10810`.
  [docker/for-mac#4388](https://github.com/docker/for-mac/issues/4388) (start the
  daemon at system boot) — **closed, frozen, locked, not implemented**.
  [docker/for-mac#6504](https://github.com/docker/for-mac/issues/6504) (headless
  install) — closed; quarantine prompt, GUI password prompt, GUI ToS acceptance all
  block it.
- macOS **Remote Login** (sshd) is off by default and must be enabled, with the SSH
  user in the allowed list. TCC is probably not an issue for a socket in `~/.docker/run`
  and repos in `$HOME`, but *is* if Run workspaces live in `~/Documents` or
  `~/Desktop` — in which case grant Full Disk Access to
  `/usr/libexec/sshd-keygen-wrapper`.

**The only working unattended pattern is macOS auto-login + "Start Docker Desktop
when you sign in".** That fights FileVault: with FileVault on, a reboot needs a disk
unlock before any GUI session exists. (On Apple silicon + macOS 26+, FileVault can
be unlocked over SSH after restart if Remote Login is on.)

If auto-login-with-FileVault-off is unacceptable, **Colima** is the honest
alternative: CLI-only, no GUI app, `brew services start colima` runs it under
launchd, socket at `$HOME/.colima/default/docker.sock`, and it becomes the default
context at startup. (Still a user LaunchAgent by default; a true boot-without-login
setup needs a launchd *system* daemon.) **OrbStack** documents an explicit headless
mode but still requires a user context, not root. Lima is Colima's engine and has
the same shape. All three install their CLI where `path_helper` puts it, so **§5.2
applies to every one of them** — it is an sshd/macOS problem, not a Docker Desktop
problem.

### 5.5 Sleep, restarts, Resource Saver

- **Mac sleep kills containers.** Long-standing and still reported:
  [docker/for-mac#85](https://github.com/docker/for-mac/issues/85),
  [moby/moby#22745](https://github.com/moby/moby/issues/22745),
  [docker/for-mac#7493](https://github.com/docker/for-mac/issues/7493) (**open**;
  engine stops working in sleep, only fix is quit + relaunch),
  [docker/for-mac#6655](https://github.com/docker/for-mac/issues/6655).
  → `sudo pmset -a sleep 0 disablesleep 1`.
- **Docker Desktop restart / auto-update** takes the engine down. Combined with
  §1.3(b), pre-4.88.0 an engine shutdown did not honour stop timeouts at all.
  → pin the version, disable auto-update.
- **Resource Saver** stops the Linux VM after a period with **no containers
  running**; default 5 minutes, **enabled by default**. It should not touch a
  running container, but the VM cold-start it forces on the next Run adds latency.
  → disable it in Settings > Resources.
- **Virtualization stack**: from 4.86 Docker VMM uses Docker's own hypervisor,
  replacing `libkrun` (4.35–4.85); Apple Virtualization framework is the
  alternative. Docker VMM requires ≥ 4 GB allocated to the VM and has documented
  issues (no Rosetta, virtiofs quirks) — **none about signal delivery**.

### 5.6 Resource limits inside the VM

`--memory`/`--cpus` are real cgroup v2 limits inside a real Linux kernel, but they
are **bounded by the VM's own allocation** (Settings > Resources):

- **Memory limit** — *"RAM allocated to the Docker VM"*, **"Defaults to 50% of your
  host's memory."** On a 16 GB Mac mini that is ~8 GB for *all* Runs combined.
- **CPU limit** — *"the maximum number of CPUs to be used by Docker Desktop"*.
  `--cpus` is CFS quota relative to the **VM's vCPU count**, not the Mac's cores.
  On Apple silicon, P/E-core asymmetry means "1 vCPU" is not a fixed amount of work.

So the per-Runner cap in the Agent YAML must be set against the VM's allocation, not
the Mac's physical RAM. Docker Desktop moved to **cgroup v2** after 4.2.0
(evidence: [docker/for-mac#6118](https://github.com/docker/for-mac/issues/6118),
where `/sys/fs/cgroup/memory/memory.limit_in_bytes` disappeared), so the limit file
is `/sys/fs/cgroup/memory.max`.

The classic *"`--memory` is ignored on Mac"* report
([docker/for-mac#2931](https://github.com/docker/for-mac/issues/2931), closed) is a
measurement artifact — `free -m` reads `/proc/meminfo`, which shows the *VM's* RAM,
not the cgroup limit. **No current credible report of `--memory` being silently
ignored.** OOM behaviour should be standard (exit 137, `.State.OOMKilled=true`) but
is not documented for Docker Desktop specifically → verify (§7 D).

---

## 6. What breaks over `ssh://` that does not break locally

Honest enumeration. Nothing here is individually a showstopper; together they are a
day of work plus a permanent operational surface.

| # | Breaks over `ssh://` | Local socket | Cost |
|---|---|---|---|
| 1 | **The SDK alone is not enough.** `WithHost("ssh://…")` silently degrades to a TCP dial. Must import `docker/cli/cli/connhelper` and wire three options **in the right order**. | Just works. | ~10 LOC + a heavyweight module dep. **Corrects `map.md`.** |
| 2 | **The image must contain `openssh-client`.** No pure-Go fallback. | No binary needed. | Kills `FROM scratch`/distroless for the control plane. |
| 3 | **No liveness detection at all.** `SetDeadline` is a no-op; hijack keepalive is skipped for non-`*net.TCPConn`; no `ServerAliveInterval`. A silent stall hangs forever (reproduced). | TCP keepalive on hijack; a unix socket dies with the daemon. | `ServerAliveInterval` + app watchdog. **The main technical risk.** |
| 4 | **A new `ssh` process + full handshake per connection**, and both multiplexing choices have a ceiling: `ControlMaster` off → `MaxStartups 10:30:100` (30% refusal at 10 concurrent); on → `MaxSessions 10` per connection. Each concurrent Run pins ~2 connections (log follow + wait). | ~free `connect(2)`. | Latency per call; a real concurrency ceiling to size against. |
| 5 | **Host-key trust must be provisioned.** `StrictHostKeyChecking` defaults to `ask`; a container cannot answer; connection fails outright. | N/A. | Bake `known_hosts` or `accept-new`, plus rotation pain if the Mac is reinstalled. |
| 6 | **A second credential class.** Passphrase-less SSH key (passphrases are unusable headless; agent forwarding into a container is fragile) alongside the git PAT and the age keys. | N/A. | Its own storage, mounting, and rotation story. |
| 7 | **`HTTP_PROXY` from the environment gets applied** to the dummy `http://docker.example.com` host, and **`DaemonHost()` lies** about which Runner you're on. | Neither happens. | Set `Proxy: nil`; track the Runner name yourself. |
| 8 | **Errors are opaque and uniform.** Bad key, no route, `docker` not on remote PATH, daemon down — all arrive as `command [ssh …] has exited with exit status 255, make sure the URL is valid, and Docker 18.09 or later is installed on the remote host: stderr=…`. The stderr buffer is **reset past 4096 bytes**, so long stderr is truncated. | Distinct `connect: no such file or directory` etc. | Log the full error including stderr. [docker/cli#4221](https://github.com/docker/cli/issues/4221), open. |
| 9 | **Depends on a hidden CLI command.** `dial-stdio` is `Hidden: true`, *"Should not be invoked manually"*, with a stub doc page. Needs a `docker` **CLI** ≥ 18.09 on the remote, on the non-interactive PATH, with no override. | N/A. | An undocumented interface we depend on + one more thing to keep installed. |
| 10 | **API-version negotiation is flaky over ssh** ([docker/cli#6125](https://github.com/docker/cli/issues/6125), open, filed by a maintainer). | Reliable. | Pin `client.WithAPIVersion`. |
| 11 | **Leaked remote processes are an observed failure mode.** [moby/moby#46076](https://github.com/moby/moby/issues/46076): hundreds of `docker system dial-stdio` + `sshd: user@notty`, 6 GB RSS. Closed with no fix. | N/A. | `defer cli.Close()`, bounded `MaxConnsPerHost`, and a periodic remote process check. |
| 12 | **`ContainerWait` and `/events` become unreliable narrators** — long-polls through a middlebox; moby's own source warns about it. | Reliable. | Poll `ContainerInspect` for authoritative state. |
| 13 | **A dead link before a stop = no teardown at all.** Cannot kill what you cannot reach. | The socket is there or the daemon is gone. | The Run must self-limit internally. **Feeds ticket 06.** |
| 14 | **Bulk transfer is slow** (`build` context, `cp`). | Local. | We do none today; a constraint on future design. |

### The alternative worth considering before committing

**Forward the daemon socket over one persistent SSH connection** instead of using
the per-connection connhelper:

```
ssh -N -o ServerAliveInterval=15 -o ExitOnForwardFailure=yes \
    -L /run/macmini-docker.sock:/Users/runner/.docker/run/docker.sock runner@macmini
```

then `DOCKER_HOST=unix:///run/macmini-docker.sock`. This deletes rows **1, 3, 4, 7,
9, 10 and 11** from the table above outright:

- the Go SDK's `unix` path is first-class — **no `docker/cli` dependency, no custom
  transport, no dummy host, no `dial-stdio`, no version-negotiation flakiness**;
- **no `docker` CLI needed on the Mac at all**, so the entire §5.2 PATH problem
  disappears;
- one supervised process with an explicit lifecycle and its own
  `ServerAliveInterval`, instead of N invisible ones with no deadlines.

`man ssh`: *"`-L local_socket:remote_socket` … Whenever a connection is made to the
local port or socket, the connection is forwarded over the secure channel."*
`sshd_config(5)`: `AllowStreamLocalForwarding` — *"The available options are **yes
(the default)**"*. Costs: a supervisor (or `autossh`) in the control-plane image,
and `openssh-client` is still required.

Recommendation: **start with `ssh://`** — fewer moving parts today, and the
mitigations are config lines. But write the Runner behind an interface thin enough
that swapping to `unix://` over a forwarded socket is a config change, because if
ticket 08 finds the transport flaky this is the fallback and it is a smaller change
than it sounds.

---

## 7. Commands for ticket 08 to run on real hardware

Run in order. Sections A–E run from any machine with ssh access; F must run from
**inside the actual control-plane container** in Coolify, because that is where
`HOME`, `PATH` and mount modes differ.

### A. Does SSH even reach the daemon (the PATH trap — do this first)

```bash
# 1. non-interactive, non-login PATH — exactly what the connhelper gets
ssh -T runner@macmini 'echo "PATH=$PATH"; echo "SHELL=$SHELL"; command -v docker || echo "!!! docker NOT on PATH !!!"'
ssh -T runner@macmini 'dscl . -read /Users/$USER UserShell'

# 2. the exact command the connhelper runs. Silence = success; Ctrl-C out.
#    Any output (esp. "command not found", exit 127) is the failure.
ssh -l runner "-o ConnectTimeout=30" -T -- macmini docker system dial-stdio </dev/null

# 3. a real request through it
DOCKER_HOST=ssh://runner@macmini docker system dial-stdio <<'EOF'
GET /_ping HTTP/1.1
Host: example.com

EOF

# 4. which socket / context the remote CLI resolves
ssh -T runner@macmini 'docker context ls; docker context inspect --format "{{.Endpoints.docker.Host}}"'
ssh -T runner@macmini 'ls -l /var/run/docker.sock ~/.docker/run/docker.sock 2>&1'

# 5. versions and daemon facts
ssh -T runner@macmini 'docker version --format "cli={{.Client.Version}} srv={{.Server.Version}} srvapi={{.Server.APIVersion}}"'
ssh -T runner@macmini 'docker info --format "driver={{.LoggingDriver}} cgroup={{.CgroupVersion}} ncpu={{.NCPU}} mem={{.MemTotal}} arch={{.Architecture}}"'
ssh -V   # client openssh version
#    Docker Desktop app version — MUST be >= 4.88.0
ssh -T runner@macmini 'defaults read /Applications/Docker.app/Contents/Info.plist CFBundleShortVersionString'
```

### B. Is the StopTimeout bug present, and does the daemon survive being alone

```bash
# 6. the StopTimeout=1 bug (moby/moby#52775) — <nil> good, 1 = pre-4.82 Desktop
DOCKER_HOST=ssh://runner@macmini docker create --name sttest alpine:3.20
DOCKER_HOST=ssh://runner@macmini docker inspect -f '{{.Config.StopTimeout}}' sttest
DOCKER_HOST=ssh://runner@macmini docker rm sttest

# 7. reboot the Mac with NOBODY logged into the GUI, wait 3 min, then:
ssh -T runner@macmini 'docker info --format "{{.ServerVersion}}"' \
  || echo "!!! Docker Desktop does not come up headless — the Runner is not unattended !!!"

# 8. GUI logout while a container runs
ssh -T runner@macmini 'docker desktop status'          # then log out at the console
ssh -T runner@macmini 'docker ps'                      # still alive?

# 9. sleep behaviour
ssh -T runner@macmini 'pmset -g | egrep "sleep|disablesleep|hibernatemode"'
#    start a long container, let the Mac sleep 5 min, wake, then:
ssh -T runner@macmini 'docker ps -a --format "{{.Names}} {{.Status}}"'

# 10. Docker Desktop settings that matter (auto-update, Resource Saver, VM size)
ssh -T runner@macmini 'plutil -p ~/Library/Group\ Containers/group.com.docker/settings-store.json | egrep -i "vmType|cpus|memoryMiB|swapMiB|autoStart|resourceSaver|AutoUpdate"'
```

### C. Teardown chain end to end — THE load-bearing test

```bash
export DOCKER_HOST=ssh://runner@macmini

# 11. SIGTERM reaches PID 1, trap runs, full grace honoured, exit 0 not 137.
#     NOTE the `sleep 1 & wait $!` form — a plain `sleep 1` would delay the trap.
docker run -d --name tdtest alpine:3.20 sh -c \
  'trap "echo TRAP at \$(date +%s); sleep 20; echo TEARDOWN_DONE; exit 0" TERM; echo UP \$(date +%s); while :; do sleep 1 & wait $!; done'
sleep 3
time docker stop --time=30 tdtest          # expect ~20s: NOT 1s, NOT 30s, NOT immediate
docker inspect -f 'exit={{.State.ExitCode}} oom={{.State.OOMKilled}}' tdtest   # expect exit=0
docker logs tdtest | tail -3               # expect TRAP then TEARDOWN_DONE
docker rm tdtest

# 12. grace is NOT truncated when teardown outlives it
docker run -d --name tdslow alpine:3.20 sh -c \
  'trap "echo TRAP; sleep 60" TERM; while :; do sleep 1 & wait $!; done'
sleep 3
time docker stop --time=30 tdslow          # expect ~30s then SIGKILL
docker inspect -f 'exit={{.State.ExitCode}}' tdslow    # expect 137
docker rm tdslow

# 13. THE ENGINE-SHUTDOWN HOLE (§1.3b) — does an in-flight Run get its grace?
docker run -d --name shutdowntest alpine:3.20 sh -c \
  'trap "echo TRAP; sleep 20; echo TEARDOWN_DONE" TERM; echo UP; while :; do sleep 1 & wait $!; done'
sleep 3
#    now QUIT Docker Desktop from the menu bar (or `pkill -f "Docker Desktop"`),
#    restart it, and:
docker logs shutdowntest    # did TEARDOWN_DONE print, or was it killed at ~2s?
docker rm -f shutdowntest
#    ^ this single test decides whether a Run interrupted by an engine restart
#      can be trusted to have pushed its Journal.

# 14. the trap-blocking failure mode, to confirm the entrypoint contract matters
docker run -d --name trapblock alpine:3.20 sh -c \
  'trap "echo TRAP; exit 0" TERM; sleep 300'     # NOTE: no `& wait`
sleep 3
time docker stop --time=10 trapblock      # expect the FULL 10s then SIGKILL, no TRAP
docker logs trapblock; docker rm trapblock
```

### D. Resource limits inside the VM

```bash
# 15. limits round-trip and are actually enforced (cgroup v2)
docker run --rm --memory 256m --cpus 0.5 alpine:3.20 sh -c \
  'stat -fc %T /sys/fs/cgroup; cat /sys/fs/cgroup/memory.max; cat /sys/fs/cgroup/cpu.max; nproc'
#    expect cgroup2fs / 268435456 / "50000 100000"

# 16. OOM kill is real, observable, and SKIPS THE TRAP
docker run -d --name oomtest --memory 64m alpine:3.20 sh -c \
  'trap "echo TRAP_RAN" TERM; dd if=/dev/zero of=/dev/shm/f bs=1M count=512'
sleep 10
docker inspect -f 'exit={{.State.ExitCode}} oom={{.State.OOMKilled}}' oomtest   # expect 137 / true
docker logs oomtest    # TRAP_RAN must NOT appear
docker rm -f oomtest

# 17. what the VM itself is allowed (a container cannot exceed this)
docker info --format 'vm_ncpu={{.NCPU}} vm_mem={{.MemTotal}}'
ssh -T runner@macmini 'sysctl -n hw.ncpu hw.memsize'    # compare to the host
```

### E. Long stream and link-drop behaviour

```bash
# 18. does a follow survive a long, QUIET run?
docker run -d --name quiet alpine:3.20 sh -c 'echo start; sleep 900; echo end'
time docker logs -f quiet          # must still be attached after 15 min and print "end"
docker rm -f quiet

# 19. kill the link mid-follow; container must survive and be re-attachable
docker run -d --name dropme alpine:3.20 sh -c 'i=0; while :; do i=$((i+1)); echo tick $i; sleep 1; done'
docker logs -f --timestamps dropme &          # note the last timestamp printed
sleep 10; pkill -f "system dial-stdio"
sleep 20
docker ps --filter name=dropme                # MUST still be running
docker logs --timestamps --since <last-ts> dropme | head   # expect exactly 1 duplicate line
docker rm -f dropme

# 20. THE ServerAliveInterval TEST — verify BOTH halves.
#     (a) WITHOUT it: physically unplug the Mac's network mid-follow.
#         Expect the follow to hang indefinitely with no error.
#     (b) add to ~/.ssh/config:  ServerAliveInterval 15 / ServerAliveCountMax 3
#         Repeat. Expect the follow to error out within ~45s.
#     If (b) does not error, the §2.3 mitigation does not work and the design
#     needs the socket-forwarding fallback instead.
```

### F. From inside the control-plane container specifically

```bash
CP=<control-plane-container>

# 21. the image actually has ssh
docker exec $CP sh -c 'command -v ssh || echo "!!! no openssh-client in image !!!"'

# 22. HOME, key modes, known_hosts, and any proxy env that will hijack the transport
docker exec $CP sh -c 'echo HOME=$HOME; ls -la $HOME/.ssh; env | egrep -i "proxy|SSH_AUTH_SOCK"'

# 23. a first connection with NO known_hosts entry — expect it to FAIL
docker exec $CP sh -c 'rm -f $HOME/.ssh/known_hosts; ssh -T -o BatchMode=yes runner@macmini true; echo rc=$?'
#     then prove the fix
docker exec $CP sh -c 'ssh-keyscan -H macmini >> $HOME/.ssh/known_hosts; ssh -T -o BatchMode=yes runner@macmini true; echo rc=$?'

# 24. concurrency / handshake cost, and the sshd ceilings
docker exec $CP sh -c 'time (for i in $(seq 1 20); do docker -H ssh://runner@macmini version >/dev/null; done)'
docker exec $CP sh -c 'pgrep -c ssh'
ssh -T runner@macmini 'sudo sshd -T 2>/dev/null | egrep "maxsessions|maxstartups"'
#     then set ControlMaster auto / ControlPersist 10m in ~/.ssh/config and re-time.
#     Watch for "Session open refused by peer" / "no more sessions" in the Mac's logs.

# 25. leaked remote processes after a few dozen calls (moby/moby#46076)
ssh -T runner@macmini 'pgrep -fc "system dial-stdio"; pgrep -fc "sshd.*notty"'

# 26. socket-forwarding fallback — does it work at all? (settles §6's alternative)
docker exec $CP sh -c \
  'ssh -fN -o ExitOnForwardFailure=yes -L /tmp/dsock:/Users/runner/.docker/run/docker.sock runner@macmini \
   && DOCKER_HOST=unix:///tmp/dsock docker version'

# 27. optional hardening — restricted key. NOTE it breaks the ssh://host/path form.
#     on the Mac, authorized_keys line:
#       restrict,command="docker system dial-stdio" ssh-ed25519 AAAA...
#     then: docker -H ssh://runner@macmini ps
```

---

## 8. Facts checked and their versions

### Source-verified (module cache, pinned versions)

| Fact | Source | Version |
|---|---|---|
| Bare SDK does not handle `ssh://`; falls through to a TCP dial | `client/options.go:63` `WithHost` → `go-connections/sockets/sockets.go` `ConfigureTransport` `default:`. **Reproduced live.** | docker/docker v28.5.2, go-connections v0.6.0 |
| `ssh://` is implemented in docker/cli, not moby | `cli/connhelper/connhelper.go` | docker/cli v29.7.2 |
| Remote command is `docker system dial-stdio`, or `docker --host=unix://<path> system dial-stdio` with a URL path | `cli/connhelper/connhelper.go` `getConnectionHelper` | docker/cli v29.7.2 |
| ssh argv: `-l user [-p port] "-o ConnectTimeout=30" -T -- host <cmd>` | `cli/connhelper/ssh/ssh.go` `Spec.args` + `addSSHTimeout` + `disablePseudoTerminalAllocation`. **Captured from a live debug run.** | docker/cli v29.7.2 |
| `-T` set to avoid a login shell on the remote | source comment on `disablePseudoTerminalAllocation` | docker/cli v29.7.2 |
| Client needs an `ssh` binary on `$PATH`; no Go fallback | `commandconn.New` → `exec.CommandContext(ctx, "ssh", …)`. **Reproduced the error.** | docker/cli v29.7.2 |
| Passwords / query params / fragments in the URL are rejected | `cli/connhelper/ssh/ssh.go` `newSpec`. **Reproduced.** | docker/cli v29.7.2 |
| `GetConnectionHelperWithSSHOpts` exists and is unused by docker/cli itself; caller must sanitize flags | `cli/connhelper/connhelper.go`, `Spec.Command` doc comment | docker/cli v29.7.2 |
| Whole environment inherited (`SSH_AUTH_SOCK`, `HOME` propagate) | `c.cmd.Env = os.Environ()` in `commandconn.New` | docker/cli v29.7.2 |
| `Pdeathsig: SIGKILL` on Linux; `Setsid: true` | `commandconn/pdeathsig_linux.go`, `session_unix.go` | docker/cli v29.7.2 |
| `SetDeadline`/`SetReadDeadline`/`SetWriteDeadline` are no-ops | `commandconn/commandconn.go` | docker/cli v29.7.2 |
| `kill()` = SIGTERM then SIGKILL after 3s; `handleEOF` waits up to 10s | `commandconn/commandconn.go` | docker/cli v29.7.2 |
| stderr buffer resets past 4096 bytes | `commandconn/commandconn.go` `stderrWriter.Write` | docker/cli v29.7.2 |
| Hijack TCP keepalive is skipped for non-`*net.TCPConn` | `client/hijack.go` `setupHijackConn` | docker/docker v28.5.2 |
| `dial-stdio` is `Hidden: true`, requires `halfCloser` | `cli/command/system/dial_stdio.go` | docker/cli v29.7.2 |
| CLI wiring recipe; `WithHost` sets `Proxy` + overwrites `DialContext`, so `WithDialContext` must come last | `cli/context/docker/load.go` `Endpoint.ClientOpts`, `go-connections/sockets` | docker/cli v29.7.2 |
| Stop = SIGTERM → grace → SIGKILL, daemon-side, under `context.WithoutCancel`; explicit `-t` overrides `StopTimeout` | `daemon/stop.go` `containerStop`. **Reproduced live.** | moby v28.5.2 |
| Default stop signal SIGTERM; default stop timeout **10s** on Linux | `container/container.go:55`, `container/container_unix.go:26` | moby v28.5.2 |
| Client sends grace as `?t=<seconds>`; no client-side timing | `client/container_stop.go`, `container_routes.go` `postContainersStop` | moby v28.5.2 |
| Resource limits are plain JSON in the create body | `api/types/container/hostconfig.go:373-408` | moby v28.5.2 |
| `logs` params: `stdout stderr since until timestamps details follow tail` | `client/container_logs.go` | moby v28.5.2 |
| `since` is nanosecond-precision, **inclusive**, and **self-disables after the first match** | `api/types/time/timestamp.go` (`"%d.%09d"`), `daemon/logger/loggerutils/logfile.go` `forwarder.Do`. **Reproduced: one duplicate at the seam.** | moby v28.5.2 |
| Daemon API server sets only `ReadHeaderTimeout: 5m` | `cmd/dockerd/daemon.go:189` | moby v28.5.2 |
| SDK default `http.Client` has **no** `Timeout`; `WithTimeout` would kill streams | `client/client.go` `defaultHTTPClient`, `client/options.go` `WithTimeout` | moby v28.5.2 |
| Moby's default pooling (`MaxIdleConns 6`, `IdleConnTimeout 30s`) is lost when you supply your own `http.Client` | `client/client.go` `defaultHTTPClient`, ref moby/moby#45539 | moby v28.5.2 |
| Go defaults for a bare transport: `DefaultMaxIdleConnsPerHost = 2`, `IdleConnTimeout` 0 = never | `net/http/transport.go:62`, `:229` | go1.27.0 |
| `ResponseHeaderTimeout` uses a `time.Timer`, so it works despite no-op deadlines | `net/http/transport.go:3078` | go1.27.0 |
| `StrictHostKeyChecking` default `ask`; `ServerAliveInterval` default 0; `ServerAliveCountMax` 3; `BatchMode` no; `ControlMaster`/`ControlPersist` no | `ssh_config(5)` | OpenSSH 10.3p1 |
| `MaxSessions` default 10 (per connection); `MaxStartups` default `10:30:100`; `AllowStreamLocalForwarding` default **yes** | `sshd_config(5)` | OpenSSH 10.3p1 |
| `_PATH_STDPATH = "/usr/bin:/bin:/usr/sbin:/sbin"`; `/etc/zshenv` absent; `path_helper` runs only from `/etc/zprofile` (login shells) | macOS SDK `paths.h`, `/etc/zprofile`, `/etc/paths`. **Verified on a real Mac.** | macOS 26 / Darwin 25.6 |
| `-H ssh://user@host[/path/to/socket]` documented, incl. the socket-path form | `docker/cli` `docs/reference/commandline/docker.md` §"Using SSH sockets" | docker/cli v29.7.2 |

### Issue / PR states, re-checked against the GitHub API on 2026-08-31

| Ref | Title | State |
|---|---|---|
| [moby/moby#46821](https://github.com/moby/moby/issues/46821) | FromEnv should work with an SSH transport set via DOCKER_HOST | **OPEN** (2023-11-15) |
| [moby/moby#52775](https://github.com/moby/moby/issues/52775) | StopTimeout=1 inexplicably | CLOSED (2026-06-05), `kind/bug` + `platform/desktop` |
| [moby/moby#53146](https://github.com/moby/moby/pull/53146) | daemon: Add configurable default container stop timeout | **MERGED** 2026-07-27 |
| [moby/moby#46076](https://github.com/moby/moby/issues/46076) | hundreds of leaked `dial-stdio` + `sshd: user@notty` processes | CLOSED, no fix |
| [docker/cli#3045](https://github.com/docker/cli/issues/3045) | Simplify setup required for remote DOCKER_HOST over SSH (the macOS PATH bug) | **OPEN** (2021-04-09) |
| [docker/cli#5626](https://github.com/docker/cli/issues/5626) | Remove hardcoded `docker` CLI command name for SSH hosts | **OPEN** (2024-11-16) |
| [docker/cli#4699](https://github.com/docker/cli/pull/4699) | resurrect ssh multiplexing | **OPEN**, unmerged (2023-12-06) |
| [docker/cli#2303](https://github.com/docker/cli/pull/2303) | revert "connhelper: use ssh multiplexing" | **MERGED** 2020-02-13 |
| [docker/cli#3900](https://github.com/docker/cli/pull/3900) | don't kill ssh on context cancel | MERGED 2022-12-08 |
| [docker/cli#5816](https://github.com/docker/cli/pull/5816) | `context.WithoutCancel` hardening | MERGED 2025-02-11 |
| [docker/cli#6125](https://github.com/docker/cli/issues/6125) | API-version negotiation intermittently wrong over ssh | **OPEN** (filed by a maintainer) |
| [docker/cli#4221](https://github.com/docker/cli/issues/4221) | opaque `exit status 255` errors | **OPEN** |
| [docker/cli#4718](https://github.com/docker/cli/issues/4718) | ssh:// to a Windows host fails (`halfCloser`) | **OPEN** |
| [docker/cli#6572](https://github.com/docker/cli/issues/6572) | use native Go ssh instead of the binary | **OPEN**, no maintainer response |
| [docker/for-mac#4388](https://github.com/docker/for-mac/issues/4388) | start the daemon at system boot | CLOSED, frozen, locked, not implemented |
| [docker/for-mac#7493](https://github.com/docker/for-mac/issues/7493) | engine stops working when the Mac sleeps | **OPEN** |
| [docker/compose#11677](https://github.com/docker/compose/issues/11677) | `no more sessions` with ControlMaster | CLOSED (stale) |

### Docker Desktop release notes (docs.docker.com/desktop/release-notes/, fetched directly)

| Version | Date | Note |
|---|---|---|
| 4.82.0 | 2026-07-13 | *"Fixed a bug where containers created by Docker Desktop had their stop timeout set to 1 second instead of the Docker Engine default, causing `docker stop` to terminate processes too quickly."* |
| 4.86.0 | 2026-08-10 | Docker VMM switches to Docker's own hypervisor (replacing `libkrun`, used 4.35–4.85) |
| **4.88.0** | **2026-08-24** | *"Fixed container stop timeouts and `unless-stopped` restart policies not being honored during engine shutdown."* ← **minimum version for a Runner** |
| 4.88.1 | 2026-08-25 | latest at time of writing |

### Live experiments

Run on darwin/arm64 against Docker Engine 29.4.0 (API 1.54) through
`connhelper.GetCommandConnectionHelper("docker", "system", "dial-stdio")` — the
identical `commandconn` code path `ssh://` uses, minus the ssh hop. They prove
everything about the transport's *semantics* (half-close, hijack, deadlines,
process lifecycle, reattach, stop timing) and nothing about SSH's own behaviour
(handshake cost, auth, keepalive), which is what §7 exists for.

---

## 9. Open / UNKNOWN

Ranked by how much a wrong guess costs.

1. **Does an engine/VM shutdown give an in-flight Run its 30 seconds on ≥ 4.88.0?**
   The release note says it was fixed; the moby thread says the daemon shutdown
   timeout is 2s by design. **These are in tension and I could not resolve it from
   docs.** If containers still get ~2s, then every Docker Desktop restart, update
   and sleep silently loses a Run's Journal, and that must be designed around
   (checkpoint pushes mid-Run, or a `lost` Run state). → §7 command 13. **Highest
   value single test in this document.**
2. **Does Docker Desktop run unattended?** Whether the VM starts after a reboot with
   no GUI login, and survives a logout. Everything points to *no*, and the upstream
   request was closed and locked. If confirmed, the `macmini` Runner needs macOS
   auto-login (fighting FileVault) or a swap to Colima. → §7 commands 7–8.
3. **Non-interactive ssh `PATH` on the actual Mac mini.** The mechanism is verified
   and predicts failure; the outcome on the target box is not. Cheap to fix, but it
   must be known before anything else is debugged. → §7 command 1.
4. **A genuinely silent link drop, with and without `ServerAliveInterval`.** I
   reproduced the hang with `SIGSTOP`; I could not reproduce a real partition
   without hardware. Everything in §2.3's mitigation list rests on
   `ServerAliveInterval` converting a partition into a prompt EOF. → §7 command 20,
   **both halves**.
5. **Mac sleep and the VM's clock.** Whether suspend/resume corrupts wall-clock
   budgets, the 30s grace, or schedule ticks. → §7 command 9.
6. **The VM's resource allocation on the actual box**, and whether OOM surfaces as
   exit 137 / `OOMKilled: true` (not documented for Docker Desktop specifically).
   → §7 commands 15–17.
7. **SSH handshake cost per API call**, and whether `ControlMaster` is needed or a
   liability given `MaxSessions`. Bounds how chatty the control plane can be.
   → §7 command 24.
8. **Which daemon the Mac mini actually runs today** (Docker Desktop vs Colima vs
   OrbStack vs nothing) and its version. Everything in §5 assumes Docker Desktop.
9. **Default logging driver and rotation settings.** Only affects how far back the
   live view can replay; the Journal is unaffected because the container pushes it.
   → §7 command 5.
10. **Whether the restricted-`authorized_keys` hardening is worth its cost.** It
    forecloses the socket-path URL form. A judgement call, not a correctness one.
11. **Throughput of ssh vs unix socket.** No published benchmark from the Docker
    projects. Irrelevant unless we ever ship a build context or `docker cp`.

---

## 10. What this changes in `map.md`

- **"Two Runners … `macmini` (remote, reached over `ssh://`)"** — the decision
  stands; the *"config, not code"* premise behind it does not. Amend to: *"the
  remote Runner costs a `docker/cli` connhelper dependency, `openssh-client` in the
  control-plane image, an SSH keypair, a `known_hosts` entry, an `~/.ssh/config`
  with `ServerAliveInterval`, a log-stream watchdog, and a pinned Docker Desktop
  ≥ 4.88.0 with sleep/auto-update/Resource Saver disabled."* Roughly a day, plus a
  standing operational surface — not zero.
- **"The container pushes, at teardown"** — **confirmed and now load-bearing.**
  Because the container owns its own Journal push, a lost log stream costs the live
  view and nothing else. Record this explicitly so nobody later "simplifies" the
  Journal into control-plane-side log capture; over `ssh://` that would make the
  Journal hostage to link quality.
- 🔴 **"`docker stop --time=30` gives it room"** — true for `docker stop`, **and not
  true for a Docker Desktop engine shutdown**, where the daemon's own shutdown
  timeout is 2 seconds by design. Add a known ceiling: *"a Run interrupted by an
  engine/VM shutdown on the `macmini` Runner may lose its Journal."* Pending §9.1.
- **"Limits use what already exists … time is a container kill via a context
  deadline"** — insufficient for the remote Runner. A context deadline in the
  control plane cannot kill a container it cannot reach, and an OOM kill is SIGKILL
  so the trap never runs at all. **The Run needs an internal time limit too**, and
  `OOMKilled` needs to be a distinct Run outcome. Feeds ticket 06.
- **The entrypoint must run the agent as `agent & wait $!`, not as a foreground
  command** — otherwise a shell trap does not fire until the agent returns and the
  30s grace buys nothing. This is a *new* constraint on ticket 06 that has nothing
  to do with SSH.
- **"Live log streaming to the UI"** (currently under *Not yet specified*, with the
  open question *"whether the remote Runner changes the answer"*) — **it does.** The
  UI stream must tolerate mid-Run reconnects; the resume primitive is
  `Timestamps: true` + `Since: <last-seen>` with `<=` de-duplication at the boundary
  timestamp only, and it needs an idle watchdog because the transport will not tell
  you it has stalled.
- **Nothing in `map.md` is invalidated outright.** One decision is mis-costed (two
  Runners), one is under-specified (limits), one has a newly discovered ceiling
  (teardown vs engine shutdown), and one open question is now answered (streaming).
