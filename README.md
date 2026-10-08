<div align="center">

# APIHub

**A production-hardened AI gateway — many upstreams, one endpoint**

Built on [New API](https://github.com/QuantumNous/new-api) by QuantumNous,
itself derived from [One API](https://github.com/songquanpeng/one-api) by JustSong.

[![license](https://img.shields.io/badge/license-AGPL--3.0-blue)](./LICENSE)

</div>

---

## What APIHub is

APIHub is an OpenAI-compatible gateway that fronts many upstream model providers
behind a single endpoint, with the reliability behaviour that a single-instance
upstream gateway does not provide:

- **Per-(channel, model) circuit breaking** — a model that starts failing is
  isolated without taking its sibling models on the same channel offline.
- **Concurrency ceilings and first-token timeouts** — channels are protected
  from being overloaded, and slow channels are detected before users feel them.
- **Automatic channel disable with corroboration** — one ambiguous upstream
  response is not enough to disable a channel.
- **Correct streaming billing** — partial usage, interrupted streams and
  termination frames are accounted for accurately instead of charging a full
  prompt for a response that never arrived.
- **Zero-downtime deployment** — in-flight requests, including long-lived SSE
  streams, survive a release.

The gateway layer, billing, quota, channel management, user/auth and the admin
console are inherited from upstream and remain under active development there.

## Project status

This is the **source of record for the production deployment**. A push to `main`
builds the frontend and binary and installs them on the production host through
GitHub Actions.

## The load balancer

Upstream New API routes on channel priority. APIHub adds a policy layer on top,
configured per channel in `data/loadbalancer.yaml` and **hot-reloaded within
5 seconds** — no restart required.

```yaml
enabled: true

default:
  max_inflight: 50        # per-channel concurrent request ceiling
  ttft_timeout_ms: 15000  # stream with no first token after this = slow
  breaker:
    failure_threshold: 5
    cooldown_seconds: 60
    half_open_probes: 1
    per_model: true        # break (channel, model), not the whole channel

channels:
  7:                       # a single-provider embedding channel
    max_inflight: 30
```

### Why `per_model` matters

The default upstream behaviour is to break a whole channel when a model fails.
In practice only a few failure classes justify that — exhausted quota, revoked
credentials, a banned upstream, an upstream outage. "Model does not exist",
"model is EOL" and "streaming transport interrupted" are all model-scoped
problems, and taking the entire channel down for them discards healthy capacity.

`per_model: true` narrows the blast radius: only the failing
`(channel, model)` pair is broken, and other models on the same key keep
serving. The full rationale is documented inline in
[`loadbalancer/policy.example.yaml`](./loadbalancer/policy.example.yaml).

`max_inflight` is always counted per channel regardless of this setting,
because a concurrency limit is a property of the upstream account's budget,
not of an individual model.

## Streaming billing correctness

Several upstream failure modes previously billed a full prompt for output that
never arrived:

| Failure | Previous behaviour | Now |
| --- | --- | --- |
| Stream truncated (EOF, no `[DONE]`) | Counted as a complete response | Charged for partial usage, marked interrupted |
| Gateway error inside a 200 stream | Whole prompt billed | Charged for what arrived, failure recorded |
| Zero-output stream | Billed as success | Zero-usage failure, no charge |

The terminal-frame contract is consolidated in one place rather than patched
per handler, so a new relay format does not silently reintroduce the bug.

## Zero-downtime deployment

`supervisorctl restart` tears the listener down before starting the new
process; every request arriving in that window is refused. APIHub makes that
window unnecessary.

Set `APIHUB_REUSEPORT=1` and both processes can hold the same port via
`SO_REUSEPORT`: the kernel spreads newly accepted connections across both,
while each process keeps serving what it already accepted. The application
already drains gracefully on `SIGTERM` (waiting up to
`SHUTDOWN_TIMEOUT_SECONDS`, default 120s, for SSE streams to finish).

[`scripts/deploy.sh`](./scripts/deploy.sh) encodes the handover:

1. Back up the current binary, config and database under a timestamped name.
2. Install the new binary. The running process keeps its open inode, so
   replacing the file does not disturb it.
3. Start the **idle** supervisor slot. Both now share the port.
4. Wait until the new pid is genuinely in the socket group and answering
   requests. A `200` alone proves nothing — the response may have come from the
   process being retired.
5. `SIGTERM` the old process and let it drain.
6. Re-verify the surviving slot, then record the new backup path.

Two supervisor programs, `apihub-blue` and `apihub-green`, both point at the
same binary; exactly one is `RUNNING`. The script only ever rewrites the config
of a slot that is **not** running, because `supervisorctl update` restarts any
program whose config it reloads — verified empirically, not assumed.

```bash
deploy.sh --binary ./apihub --version v29.36   # handover
deploy.sh --status                             # which slot is live
deploy.sh --rollback                           # restore the previous binary
```

The first handover from a build that predates `SO_REUSEPORT` cannot be
zero-downtime — the incumbent cannot share a port. It requires one explicit
`--bootstrap` run, which accepts a single brief connection gap. Every deploy
after that is zero-downtime.

### Rollback

Before the binary is touched, a timestamped backup is taken on the persistent
volume (`backups/<timestamp>/`), including the previous binary and a
`sqlite3 .backup` snapshot of the database. `--rollback` restores the previous
binary **and the frontend that was live alongside it**, then restarts the slot
that was previously live.

## Frontend as a separate artifact

The dashboard ships as two artifacts built from one source tree:

- **embedded** — `//go:embed web/dist` puts the frontend inside the binary. This
  is what a Docker image or a bare `go build` gets, and it is the fallback that
  always works.
- **on disk** — a `web/dist` bundle published next to the binary and served by
  `APIHUB_STATIC_DIR`. Static assets are read from disk on every request, but
  `index.html` is read once at startup and held for the life of the process.
  Replacing files *inside* a bound directory therefore swaps the JS while
  leaving the old HTML in place, which yields either a console that silently
  never updates or a new index paired with old chunks. Publishing a frontend
  means pointing a slot at a different directory and restarting it — which is
  what `deploy.sh` does once per version.

```bash
tar -czf apihub-web.tar.gz -C web/dist .
deploy.sh --binary ./apihub --version v29.36 --web ./apihub-web.tar.gz
```

`APIHUB_STATIC_DIR` unset means "serve the embedded copy" — the pre-existing
behaviour, and the instant way to undo a frontend release. When it is set, the
directory is validated first (`index.html` ≥ 200 bytes, ≥ 10 files under
`static/`, the same thresholds the embedded copy is held to) and any failure
falls back to the embedded copy with a log line rather than taking the console
down.

**Each slot is bound to its own version directory, never a shared `current`
symlink.** The handover is process-scoped: for the seconds both processes run,
a shared directory would serve some requests a new `index.html` and some an old
script bundle. The `web/current` symlink exists for humans only; nothing serves
through it.

Three gates protect the result, and each one exists because the failure it
catches is silent:

1. **Before** anything is bound, the bundle is validated on the host. A bad
   bundle aborts the deploy while the old slot is still serving.
2. **After** the old slot has drained — when the port has exactly one owner —
   the served `index.html` must reference the asset filenames of the bundle
   that was just installed. If it does not, the deploy rolls itself back:
   a healthy service showing a stale console is worse than the previous
   release.
3. `common.LoadStaticBundle` re-checks the same thresholds at startup, so a bad
   directory is caught however it arrives.

Readiness additionally asserts that the running process reports the `--version`
this deploy was asked for. That is the only check that can tell "the right
binary" from "a stale artifact that happens to serve the right frontend": the
frontend fingerprint proves the console is current but says nothing about which
binary is serving it.

Before any of that, the uploaded binary is asked what it is (`--version`, which
exits long before the database is opened). This is not redundant with the
readiness check: `common.InitEnv` overwrites `common.Version` from the `VERSION`
environment variable, which `deploy.sh` writes from `--version` itself — so a
binary whose build-time stamp never applied would still report the expected
version once running. Only the artifact knows. A mismatch aborts the deploy
before the lock, the backup, or the first byte written.

Old bundles are pruned to the newest `WEB_KEEP` (default 3), never including
the live one or the one `--rollback` would return to.

**`--rollback` is not zero-downtime.** It stops both slots before starting the
previous release, so traffic is refused for the duration of the stop — up to
`SHUTDOWN_TIMEOUT_SECONDS` (default 120s) if the outgoing process has in-flight
work to drain. This is inherent: there is no second process to hand the port to,
and a rollback that started the old binary *before* stopping the new one could
not tell the two apart. Reach for it when a release is already wrong, not as a
routine deploy step.

## Configuration

Selected environment variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `APIHUB_REUSEPORT` | `0` | Enable `SO_REUSEPORT`; required for zero-downtime deploys |
| `APIHUB_STATIC_DIR` | *(empty)* | Serve the frontend from this directory; empty serves the copy embedded in the binary |
| `SHUTDOWN_TIMEOUT_SECONDS` | `120` | Grace period for in-flight requests on `SIGTERM` |
| `SQLITE_PATH` | `one-api.db` | SQLite database location |
| `GLOBAL_API_RATE_LIMIT` | `360` | Global API request limit |

See `.env.example` for the full set inherited from upstream.

## Development

```bash
make build-web     # frontend (bun)
make build-api     # backend (go)
make test          # full test suite
make verify-embed  # assert web/dist is a real build, not a placeholder
bash scripts/deploy_test.sh   # frontend bundle staging, config binding, fingerprint probe
```

`make verify-embed` exists because a placeholder `index.html` builds into a
binary that starts, serves `200`, and renders a blank console. That failure
once cost 2h41m of production; the check is enforced in CI, at startup, and
again on the host before each frontend publish.

## Attribution and licence

APIHub is a derivative work under the **AGPL-3.0**. It is not an independent
from-scratch implementation: the gateway core, billing, quota and console
originate in New API, and the New API line itself derives from One API.

As required by AGPLv3 Section 7, this distribution preserves upstream
attribution and marks its changes. See [`NOTICE`](./NOTICE) for the required
notices, the upstream links, and the summary of APIHub contributions.

New API itself carries an additional Section 7(b) notice that modified
versions with a user interface must preserve:

> Frontend design and development by New API contributors.
>
> https://github.com/QuantumNous/new-api

If you fork this, keep those notices intact. Dropping them is a licence
violation, not a branding choice.