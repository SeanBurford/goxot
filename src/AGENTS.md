# AGENTS.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Module

`github.com/SeanBurford/goxot`, Go 1.21+

## Commands

```bash
go build ./cmd/...            # build all binaries
go test ./...                 # run all tests (includes tun/ subpackage)
go test -v -run TestFoo ./... # run a single test
go vet ./...                  # static analysis
```

## Package layout

| Path | Package | Purpose |
|---|---|---|
| `.` | `xot` | X.25/XOT protocol, session management, config, stats |
| `tun/` | `tun` | Linux ARPHRD_X25 TUN interface operations (shared by tun-gateway and tun-loopback) |
| `cmd/tun-gateway/` | `main` | Privileged TUN↔XOT relay |
| `cmd/tun-loopback/` | `main` | Privileged multi-TUN local-to-local relay |
| `cmd/tun-listener/` | `main` | Diagnostic AF_X25 listener |
| `cmd/xot-server/` | `main` | Inbound XOT TCP server |
| `cmd/xot-gateway/` | `main` | Outbound XOT TCP dialer |

## Library packages

### tun/ — Linux ARPHRD_X25 TUN operations

Shared between `tun-gateway` and `tun-loopback`. Both commands import `github.com/SeanBurford/goxot/tun`.

**Key exports**:
- `Interface` — wraps a TUN fd; implements `io.Reader`, `io.Writer`, `io.Closer`, plus `Name()` and `Fd()`
- `Setup(name string) (*Interface, error)` — opens `/dev/net/tun`, sets `ARPHRD_X25`, brings up
- `BringUp(name string) error` — `IFF_UP | IFF_RUNNING` via `SIOCSIFFLAGS`
- `AddRoute(iface, prefix string, digits int) error` / `DeleteRoute(...)` — `SIOCADDRT`/`SIOCDELRT`
- `SetSubscription(iface string, lciStart, lciEnd int) error` — `SIOCX25SSUBSCRIP`; enables extended mode when `lciEnd > 255`
- `ReadFrame(r io.Reader, ifname string, buf []byte) (hdr byte, payload []byte, err error)` — reads one PI-framed packet; skips non-X25 EtherType frames; payload aliases buf
- `WriteFrame(w io.Writer, ifname string, hdr byte, data []byte) error` — allocates internally
- `WriteFrameBuf(w io.Writer, ifname string, hdr byte, data, buf []byte) error` — zero-alloc hot path; `cap(buf) >= len(data)+5`
- `HeaderData`, `HeaderConnect`, `HeaderDisconnect`, `HeaderParam` — PI control byte constants
- `MaxPacketSize` — `xot.MaxX25PacketSize + 5` (read buffer size)

**Tests** in `tun/tun_test.go` use a `perFrameReader` that returns one complete frame per `Read` call, simulating TUN semantics.

---

### x25.go — X.25 packet encoding

**Key types and constants:**

- `X25Packet{GFI byte, LCI uint16, Type byte, Payload []byte}`
- Size limits: `MaxUserData=4096`, `MaxX25PacketSize=4107`, `MaxXOTPacketSize=4111`, `MaxCallRequestSize=260`
- LCI range: `LCIMin=1`, `LCIMax=4095` (LCI 0 is reserved for link-level)
- Packet type constants: `PktTypeCallRequest=0x0B`, `PktTypeData=0x00`, `PktTypeRR/RNR/REJ=0x01/0x05/0x09`, `PktTypeClearRequest=0x13`, etc.

**Non-obvious invariants:**

- **LCI encoding**: 12-bit LCI is packed into bytes 0–1 of the header; GFI occupies the upper nibble of byte 0. Direct in-place remapping: `data[0] = (data[0] & 0xF0) | byte((lci>>8)&0x0F)`, `data[1] = byte(lci & 0xFF)`.
- **Data packet detection**: `(Type & 0x01) == 0`. S-frames (RR/RNR/REJ) encode type in bits 3–1; use `GetBaseType()` to normalize.
- **Address encoding**: X.121 addresses are BCD-packed (two hex digits per byte). `EncodeBCD` handles this; `ParseCallRequest`/`ParseCallConnected` decode it.
- **Facility encoding**: variable-length by class (bits 7–6 of the code byte). Class 0→1 byte value, class 1→2 bytes, class 2→3 bytes, class 3→variable with length prefix. Always validate class before reading values.
- **CALL_REQ size limit** is 260 bytes, stricter than the general packet limit.

**Key functions**: `ParseX25`, `(p) Serialize`, `(p) ParseCallRequest`, `(p) InjectFacilities`, `CreateClearRequest`, `CreateInterrupt`, `EncodeBCD`, `LogTrace`.

---

### xot.go — RFC 1613 framing

XOT header: 2-byte big-endian version (0) + 2-byte big-endian length + X.25 payload.

**Buffer pool**: `GetBuffer() []byte` / `PutBuffer(buf []byte)` — pre-allocated to `MaxXOTPacketSize`. `PutBuffer` is a no-op if capacity is too small; only correctly-sized buffers are recycled.

**Write strategy**:
- Packet-oriented sockets (unixpacket): always a single `Write()`.
- Stream sockets < 4096 bytes: also a single `Write()`.
- Larger stream packets: split header + data writes, but counted as one packet in stats.

**Key functions**: `SendXot`, `ReadXot`, `ReadXotInto`, `GetFd`, `SetNoDelay`, `SetTCPKeepalive`, `GetBuffer`, `PutBuffer`.

---

### session.go — virtual circuit management

**Session** holds two sides: side A (TUN-facing, `ConnA` is always nil since the TUN is a physical fd, not a `net.Conn`) and side B (remote-facing, `ConnB`). Both sides have their own LCI.

**X.25 states**: `StateP1`="p1" (ready), `StateP2`="p2" (DTE waiting), `StateP3`="p3" (DCE waiting), `StateP4`="p4" (data transfer), `StateP5`="p5" (clearing).

**SessionManager** maintains three indices:
- `byALCI[lci]` — lookup by TUN-side LCI (one session per LCI)
- `byBConnLCI[conn][lci]` — lookup by (gateway connection, gateway LCI)
- `sessions[id]` — by generated ID

**LCI allocation**: round-robin cursor (`nextLCI`) is advanced past just-freed LCIs to prevent immediate reuse — defence against ABA races with residual cleanup goroutines.

**Session ID format** includes connection memory addresses, making it unique even when LCIs are recycled.

**Key methods**: `NewSessionManager(lciStart, lciEnd)`, `AllocateAndAddTunSession`, `GetByALCI`, `GetByBConnLCI`, `GetSessionsForConn`, `RemoveSession`, `RemoveAllSessions`.

---

### config.go — configuration management

**`TunLoopbackConfig`**: embeds `TunConfig` (`lci_start`/`lci_end`) and `ServiceConfig` (`stats-port`); adds `Routes []string` — one X.121 address per TUN created by `tun-loopback`.

**`XotServerConfig`**: `Prefix string` (e.g., `"123/3"`), `IP string`, `Port int`, `DNSPattern string`, `DNSName string`, `TCPKeepaliveInterval *int` (nil→30s, 0→disabled), `X25KeepaliveInterval int`.

- Exactly one of `IP` or (`DNSPattern` + `DNSName`) must be set per server entry.
- `DNSPattern` defaults to `"^(...)(...)"` if `DNSName` is set but `DNSPattern` is omitted.
- `GetServer(addr)` performs longest-prefix matching on the `"NUM/LEN"` prefix field and auto-reloads config before lookup.
- LCI ranges are clamped to 1–4095 at load time and again in `SessionManager` constructor.

**Defaults**: `LciStart=1024`, `LciEnd=2048`, `Port=1998`, TCP keepalive 30s.

---

### dns.go — DNS resolution

`ResolveXotDestination(addr string, srv *XotServerConfig) ([]string, error)`: regex-matches `addr` against `srv.DNSPattern`, substitutes capture groups into `srv.DNSName`, resolves, caches for 60 seconds. Returns immediately if `srv.IP` is set. Thread-safe via `dnsCacheMu`.

---

### stats.go — expvar metrics

`StartStatsServer(port int)` — no-op if `port == 0`. Serves `/varz` with CORS (`Access-Control-Allow-Origin: *`) and `/debug/vars` (standard expvar).

Interface-level counters (all `expvar.Map`, keyed by interface name): `InterfaceSessionsOpened/Closed`, `InterfaceCallRequest/Connected`, `InterfaceClearRequest/Confirm`, `InterfacePacketsSent/Received`, `InterfaceBytesSent/Received`. Updated inline in send/receive paths; expvar.Map is internally thread-safe.

---

### listener.go — utilities for X.25 ioctls

`X25AddrFromBytes(addr []byte) string` — extracts null-terminated X.121 address from a kernel byte array.

`FormatX25FacilitiesRaw(winIn, winOut, psizeIn, psizeOut uint32) string` — formats `SIOCX25GFACILITIES` ioctl results; `psizeIn/Out` are log₂ exponents (e.g., 7 → 128 bytes).
