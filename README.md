# advp — advanced ping

`advp` is a drop-in replacement for iputils `ping` written in Go. It
supports most of the GNU/Linux `ping` command line and prints the same
per-reply lines and summary. When the run ends it adds a report covering
the whole session:

- **Overall stats**: sent, received, lost, duplicates, errors; RTT min, p50, avg, p75,
  p90, p95, p99 and max; jitter (mean |ΔRTT|, RFC 3550 style); mdev;
  loss episodes and the longest outage; hop distance estimated from the reply TTL.
- **First hops**: probes with TTL=1..N (default 2) run alongside the main ping.
  Each hop shows its address(es) and reverse DNS name, plus loss, RTT
  percentiles and jitter. Route changes are detected.
- **Timeline table**: one row per time bucket from start to exit, with
  loss, latency percentiles, jitter and per-hop latency/loss.
- **Terminal charts**: an RTT chart (p50 bars, p50→p90 band, max markers,
  fully lost columns), plus sparklines for loss, jitter and each hop.
- **Live dashboard** (`--tui`): a full-screen view that refreshes while the ping runs.
- **Exports**: `--csv` writes every probe; `--html` writes a self-contained
  report with SVG charts that show values on hover.

## Build

```sh
go build -o advp .
```

Supported on Linux and macOS.

## Privileges

`advp` first tries an unprivileged ICMP datagram socket:

- **macOS**: works out of the box, including the hop probes.
- **Linux**: needs your group in `net.ipv4.ping_group_range`, e.g.
  `sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"`. ICMP
  errors (TTL exceeded, unreachable) are read from the socket error
  queue (`IP_RECVERR`).

If the datagram socket fails, or you run as root (or with
`sudo setcap cap_net_raw+ep ./advp`), it uses a raw socket. Like iputils,
non-root users cannot use intervals below 2 ms or a preload above 3.

## Usage

```sh
advp 1.1.1.1                       # like ping, Ctrl-C prints summary + report
advp -c 100 -i 0.2 example.com     # 100 probes, 200 ms apart
advp -w 600 --tui example.com      # 10-minute live dashboard
advp -q -w 3600 --html net.html --csv net.csv 8.8.8.8   # 1-hour unattended capture
advp --hops 4 --bucket 1m gw.lan   # watch 4 hops, 1-minute timeline rows
```

Ctrl-C (SIGINT/SIGTERM) stops and prints the report. Ctrl-\ (SIGQUIT)
prints a one-line status, as in iputils.

### iputils options

| Option | Meaning | Notes |
|---|---|---|
| `-c count` | stop after *count* replies | |
| `-i interval` | seconds between packets | |
| `-W timeout` | time to wait for a response | linger after the last probe |
| `-w deadline` | stop after *deadline* seconds | |
| `-s size` | data bytes | |
| `-t ttl` | IP TTL / hop limit | |
| `-l preload` | send *preload* packets at once | |
| `-f` | flood ping | |
| `-A` | adaptive ping | |
| `-q` / `-v` | quiet / verbose | |
| `-n` / `-H` | numeric / force reverse DNS | |
| `-D` | timestamps | |
| `-a` | audible | |
| `-O` | report outstanding replies | |
| `-3` | full RTT precision | |
| `-p pattern` | padding pattern (hex) | |
| `-Q tos` | TOS / traffic class | |
| `-I iface\|addr` | interface or source address | `SO_BINDTODEVICE` on Linux, `IP_BOUND_IF` on macOS |
| `-m mark` | `SO_MARK` | Linux only |
| `-M do\|dont\|want\|probe` | PMTU discovery | macOS supports only `do` and `dont` |
| `-S size` | `SO_SNDBUF` | |
| `-e id` | ICMP identifier | |
| `-b` | allow broadcast | |
| `-r` | `SO_DONTROUTE` | |
| `-L` | no multicast loopback | |
| `-d` | `SO_DEBUG` | |
| `-C` | `connect()` the socket | |
| `-R` | record route | IPv4, needs a raw socket |
| `-4` / `-6` | force the IP family | |
| `-B`, `-U` | sticky source, user latency | accepted; no effect |
| `-T`, `-F`, `-N` | timestamp option, flow label, node info | accepted with a warning, not implemented |

Exit codes match iputils: `0` if replies were received, `1` if none were
(or fewer than `-c` before the `-w` deadline), `2` on other errors.

### advp extensions

| Option | Meaning |
|---|---|
| `--tui`, `--dashboard` | live full-screen dashboard |
| `--hops N` / `--no-hops` | number of first hops to probe (default 2) |
| `--hop-interval D` | hop probe interval (default `max(1s, -i)`) |
| `--bucket D` | timeline row width (default: auto, about 20 rows) |
| `--chart-height N` | RTT chart rows (default 10) |
| `--csv FILE` | per-probe CSV (target and hops) |
| `--html FILE` | HTML report with SVG charts |
| `--no-report` | classic ping summary only |
| `--color auto\|always\|never`, `--no-color` | ANSI colors (honours `NO_COLOR`) |

Durations accept Go syntax (`500ms`, `1m30s`) or seconds (`0.5`).

## Notes on the statistics

- A probe counts as **lost** once it has had no answer for `-W` (or
  ~2×max RTT, at least 1 s). Younger probes count as *in flight*, so the
  live view doesn't report false loss.
- **Jitter** is the mean absolute difference between consecutive RTTs.
  **mdev** is the standard deviation, as in iputils.
- Percentiles use linear interpolation between closest ranks.
- Many routers rate-limit ICMP Time Exceeded replies. Loss seen only
  on a hop does not mean forwarded traffic is being lost.

## Development

```sh
go test -race ./...
GOOS=linux go vet ./...
```

The tests drive the full engine (sending, replies, loss, duplicates,
ICMP errors, hop route changes, report and exports) with a simulated
network transport, so they need no privileges or network access.
