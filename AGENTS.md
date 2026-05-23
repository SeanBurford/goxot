# AGENTS.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

All Go commands run from `src/`:

```bash
# Build individual binaries
go build -o tun-gateway ./cmd/tun-gateway
go build -o tun-loopback ./cmd/tun-loopback
go build -o xot-server ./cmd/xot-server
go build -o xot-gateway ./cmd/xot-gateway

# Test
go test ./...
go test -v -run TestParseCallRequest ./...   # run a single test

# Stress test (C, requires make in stress_test/)
cd stress_test && make
```

No linter is configured; `go vet ./...` is the closest available check.

Format source code with `gofmt -w ./...`.

Use Unix newlines (\r\n), not DOS newlines (\r).

## Architecture

GoXOT is an X.25-over-TCP (XOT, RFC 1613) relay stack. It bridges Linux TUN interfaces with remote X.25 networks over TCP. The system runs as four cooperating processes connected by Unix domain sockets:

```
TCP clients
    │ RFC 1613 (TCP)
    ▼
xot-server          ← routes inbound XOT calls by X.121 destination prefix
    │ unixpacket
    ├──► xot-gateway ← resolves destination (static IP or DNS pattern), dials remote XOT server
    │
    └──► tun-gateway ← (root) reads/writes Linux TUN interface (ARPHRD_X25), remaps LCIs
             │ unixpacket
             └──► xot-gateway  (for outbound calls from the TUN side)

tun-loopback        ← (root) multi-TUN local-to-local relay; one TUN per configured address
    tunlb0 ──relay── tunlb1   (frames copied between TUN fds; LCIs remapped to avoid conflicts)
```

**Privilege boundary**: `tun-gateway` and `tun-loopback` run as root. All other processes are unprivileged.

**Config file** (`config.json`): watched via inotify for hot reload. Defines server prefixes, IP/DNS routing, LCI ranges, keepalive settings, and per-destination facility overrides.

**Stats**: each process exports metrics via `expvar` HTTP at `/varz` on a configurable port.

### Core library packages (all under `src/`)

| Package | Responsibility |
|---|---|
| `x25.go` | Parse/serialize X.25 packets; X.121 address encoding; facility injection |
| `xot.go` | RFC 1613 framing (4-byte header); buffer pool; TCP keepalive helpers |
| `session.go` | `SessionManager` — triple-indexed sessions (by LCI-A, conn+LCI-B, ID); round-robin LCI allocation to prevent reuse races |
| `config.go` | JSON config loading with inotify hot-reload; prefix matching; LCI/port defaults |
| `dns.go` | Regex-pattern DNS resolution with 60-second cache and group substitution |
| `stats.go` | `expvar`-based stats server |

### Key data flow

**Inbound call** (remote → local TUN):  
TCP → xot-server parses CALL_REQ, resolves destination → forwards raw XOT packet over Unix socket to tun-gateway → tun-gateway writes to TUN fd → Linux kernel delivers to X.25 socket.

**Outbound call** (local TUN → remote):  
tun-gateway intercepts CALL_REQ from TUN → forwards over Unix socket to xot-gateway → xot-gateway resolves destination, dials TCP, injects facilities → bidirectional relay loop runs until CLR_REQ/CLR_CONF exchange.

### LCI partitioning

The Linux kernel uses LCI 1–256 for its own X.25 sockets. The gateway uses a configurable range (default 1024–4095). When packets cross the TUN boundary, `tun-gateway` remaps LCIs between the two namespaces and maintains the mapping in `SessionManager`.

### Named race-condition protections in the code

- **RACE-A**: TUN read buffer must be copied before spawning a goroutine (aliasing)
- **RACE-B**: CLR_REQ sent unconditionally to flush stale kernel socket state
- **RACE-D**: `sync.Mutex` guards concurrent writes ≥4096 bytes on stream sockets
- **SESS003–005**: Session state checks before sending CLEAR; identity verification; atomic bulk remove

### Diagnostics

- `tun-listener`: binds an X.121 address, accepts calls, prints caller/facilities
- `x25_ping`: measures round-trip time for X.25 circuits
- `stress_test/`: C program for load testing the full stack
