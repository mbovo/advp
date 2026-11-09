# AGENTS.md — advp

Context for coding agents working on this repository: how it is built, why it
is shaped this way, and the gotchas found while writing it. For usage and
features read [README.md](README.md); this file does not repeat it.

## What this is

`advp` is an iputils-compatible `ping` written in Go (Linux and macOS). It adds:

- an end-of-run report: percentiles, jitter, loss episodes, timeline table,
  terminal charts;
- monitoring of the first hops (`--hops N`) or of the whole route
  (`--hops-max`, which runs a parallel traceroute first);
- a full-screen live view (`--live`);
- CSV and HTML exports.

Module: `github.com/mbovo/advp`. `go.mod` says `go 1.26`; development used
Go 1.27.

## Build, test, lint

Use the `justfile` (`just` lists every recipe). The essentials:

| Command | Purpose |
|---|---|
| `just build` / `just run -c 5 1.1.1.1` | build `bin/advp` with version ldflags / build and run |
| `just test`, `just test-race` | unit tests (no network or privileges needed) |
| `just lint` / `just vet` | run once per GOOS (`darwin`, `linux`): socket code is build-tagged |
| `just check` | **run before every commit**: fmt-check, tidy-check, vet, lint, race tests, cross build (same as CI) |
| `just smoke` | real ICMP ping to 127.0.0.1 (fails inside sandboxes that block ICMP) |
| `just tools` | install the pinned golangci-lint **v2**, govulncheck, GoReleaser |

The lint config is golangci-lint **v2** format (`.golangci.yml`). golangci-lint
v1.x cannot load Go 1.27 export data and reports bogus `typecheck` errors. Point
the recipes at another binary with `GOLANGCI_LINT=/path/to/golangci-lint`.

## Architecture

The dependency direction is strict, with no cycles:
`cmd/advp → cli → ping → {icmp, stats, report, tty}`, `report → {stats, version}`.

| Package | Responsibility / key files |
|---|---|
| `cmd/advp` | `main` → `cli.Run(os.Args[1:], stdout, stderr)` returns the exit code |
| `internal/cli` | hand-written getopt clone (`options.go`), help text (`usage.go`), resolution, privilege checks, opens sockets, builds `ping.Config` |
| `internal/ping` | the engine (`pinger.go`), `Options` + defaults, route discovery (`trace.go`), iputils formatting (`format.go`), reverse-DNS cache (`names.go`) |
| `internal/icmp` | `Conn` interface + `Config` (`conn.go`), encode/decode (`packet.go`), sockets: `socket_unix.go` + `socket_linux.go` / `socket_darwin.go` / `socket_other.go` |
| `internal/stats` | `Sample`, `Series` (16-bit seq wrap), `Aggregate`, `Bucketize`, `NiceBucket`, `GuessHops` |
| `internal/report` | `Input` snapshot → final text report (`text.go`, `chart.go`, `style.go`), live `Dashboard` (`dashboard.go`), CSV/HTML+SVG (`html.go`) |
| `internal/tty` | cbreak terminal mode + key decoding for `--live` |
| `internal/version` | `Version`/`Commit`/`Date` via `-ldflags -X`, falls back to `debug.ReadBuildInfo` |

### Engine (`ping.Pinger.Run`)

- **One goroutine owns all state.** The goroutines from `reader()` only push
  `connEvent{idx, ev}` into `p.events`: idx 0 is the destination socket, idx k
  is hop k. Never touch `Pinger` fields from another goroutine.
- **Send schedule.** Sends are anchored to `start + n*interval` (no drift).
  `-A` and `-f` send on reply through `sendSoon()`. When it is too early,
  `sendSoon()` reschedules the timer (`scheduleSend`); it never drops the send.
- **Exit codes.** iputils semantics: 0 = replies received, 1 = none (or fewer
  than `-c` before the `-w` deadline), 2 = error. Linger after the last probe
  follows iputils `schedule_exit` (`exitWait`).
- **Hop probing.** One socket per TTL, probed every `--hop-interval`. Hop
  announcements are printed only for never-seen responders (`hopState.note`
  returns `isNew`).
- **`--hops-max` (`trace.go`).** Before the PING header, all TTL sockets get
  `traceProbes` = 3 rounds, `defaultTraceGap` = 250 ms apart, then
  `defaultTraceWait` = 1 s. The path is cut at the first echo reply from the
  destination, or at a non-time-exceeded ICMP error (`!H`, `!N`…), which is kept.
  `p.hops` is truncated. The dropped sockets **stay open until the end of Run**:
  closing an fd under a polling reader is unsafe. Their events are ignored by
  the `ce.idx <= len(p.hops)` guard in `handle`. Tests shorten the timings via
  `p.traceGap` / `p.traceWait`.
- **Live view.**
  - A frame is drawn right after each send (`frameNow`) and after
    replies/errors through the paced `frameC`. The gap is
    `clamp(3×render time, minFrameGap 20 ms, maxFrameGap 1 s)`, plus a 1 s tick.
  - Keys come from `Config.Keys` (stdin in cbreak mode: ICANON and ECHO off,
    ISIG kept, so Ctrl-C still raises SIGINT).
  - SIGWINCH is registered only in live mode.
  - Per-packet lines go to `p.recent` instead of stdout.

### Sockets: platform facts (verified empirically on macOS, from kernel source for Linux)

- **Socket choice.** Prefer `SOCK_DGRAM` ICMP "ping sockets" (unprivileged).
  Use `SOCK_RAW` when root, when `PreferRaw` is set (`-R`), or when DGRAM fails.
- **Darwin DGRAM:**
  - the received data **includes the IPv4 header**;
  - the ICMP identifier is preserved, so filtering by ID happens in user space
    (`FilterID() == true`);
  - Time Exceeded arrives on the socket itself, and `SO_TIMESTAMP` works;
  - the TTL control message type is `IP_RECVTTL`.
- **Linux DGRAM:**
  - the kernel rewrites the echo ID to the bound "port" (`getsockname`) and
    demultiplexes replies itself;
  - ICMP errors arrive **only via the error queue** (`IP_RECVERR` /
    `IPV6_RECVERR`, read with `MSG_ERRQUEUE`, decoded by `parseExtErr`);
  - local errors (e.g. EMSGSIZE with `-M do`) are queued with an **empty
    payload**, so they carry no sequence number;
  - requires `net.ipv4.ping_group_range`.
- **ICMPv6.** The kernel computes the checksum.
- **Record route.** `-R` is IPv4-only and needs a raw socket to read the route back.

### Statistics semantics

- A probe without an answer counts as **pending**, not lost, until `lostAfter`:
  `-W` if given, else `clamp(2×max RTT, 1 s, -W)`. Final reports use
  `lostAfter = 0`. `LossPct` only counts completed probes.
- Jitter is the mean |ΔRTT| of consecutive replies. A loss breaks the chain.
  `Bucketize` passes the previous RTT in, so a 1-sample bucket still has jitter
  (`HasJitter`).
- Percentiles use linear interpolation between closest ranks. `mdev` is the
  standard deviation (iputils).

### Report and live layout rules

- `Dashboard.frame()` must return **exactly `height` lines**, with the footer
  last.
- Priorities: chart > timeline table. In the report view, hop sparklines are
  shown only for routes of 4 hops or fewer. The `hops` view never drops the hop
  table; it shortens the summary instead.
- Tables drop columns via `table.drop` when too wide.
- `truncANSI` keeps escape sequences intact.
- The footer uses attribute-specific off codes (`\x1b[22m`), not `\x1b[0m`.
  A full reset would end the reverse video.

## Conventions

- **Tests.** Engine tests drive `ping.New` with `fakeConn` (`pinger_test.go`)
  and `route` (`trace_test.go`), which simulate latency, loss, duplicates, ICMP
  errors and route changes. Add a fake-based test for every behavior change.
  `ping` tests cannot import `cli` (import cycle), so set `Options` fields
  directly.
- **Messages.** Wording mirrors iputils, including capitalised error strings
  (`ST1005` is disabled on purpose).
- **New flags.**
  - Parse them in `cli/options.go`. Long options use one of three maps:
    `longFlags` / `longWithArg` / `longOptArg`. For `longOptArg`, `--opt N` is
    accepted only when N is a small integer (`isSmallUint`).
  - Add a parse test in `cli/options_test.go`.
  - Document the flag in `cli/usage.go` and in the README options table.
- **Exports.** Unchecked writes are allowed only for terminal output
  (`io.WriteString`) and `csv.Writer.Write`; the errcheck config lists them.
  Everything else checks errors.
- **Commits.** Imperative subject, body explaining *why*, and the trailer
  `Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>`.
- **The README is co-edited by the maintainer.**
  - Run `git status` / `git log` before editing it; the maintainer has
    committed README changes mid-session before (`a86a653`).
  - Keep the "vibecoded" CAUTION banner at the top. Its cost table is a
    model-made estimate (commit `8efe936`); update it only when asked.

## Agent environment gotchas (sandboxed sessions)

- **No ICMP.** Outbound ICMP was blocked (`sendmsg: operation not permitted`,
  even for the system `ping`), so real-network behavior has **never been
  observed**. Rely on the fake-based tests. Exercise the live view in a pty:
  `(sleep 2; printf q) | script -q "$TMPDIR/out" ./bin/advp --live 127.0.0.1`.
- **No module downloads.** sumdb writes are denied. Build tools from the module
  cache with a wrapper module in `$TMPDIR`
  (`GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build …`). That is how
  golangci-lint v2.14.0 was built. Set `GOLANGCI_LINT_CACHE="$TMPDIR/…"`.
- **Read-only user caches.** gopls cannot write its cache; `gopls imports`
  silently does nothing.
- **Writable locations.** Only the repo and `$TMPDIR`. `sed -i` failed;
  `perl -pe … > tmp && mv tmp file` works.
- **Re-check after interrupted edits.** A timed-out shell command applied an
  edit twice once; re-check files after interrupted multi-step edits.

## Status and open items

History (oldest first): flat prototype → `e68ec88` project layout + tooling →
`21ae59e` `--live` → `8b9a024` `--hops-max` → `8efe936` cost estimate.

Not verified yet:

- Real pings on Linux: error queue, ID rewrite, `SO_MARK`, `SO_BINDTODEVICE`,
  PMTU modes.
- Raw-socket paths (root).
- IPv6 end to end.
- Tracing real routers, which rate-limit ICMP.
- `govulncheck`, GoReleaser, and the GitHub workflows have never run.

Known gaps:

- `-T`, `-F`, `-N` are accepted but not implemented; `-B` and `-U` are no-ops.
- `icmp` coverage is ~27% (socket code is untestable without network).
- There is no LICENSE; that is the maintainer's choice.

Possible next steps (suggestions, not requested yet): scrolling in the live
view, re-tracing when the route changes, and per-hop loss alerts.
