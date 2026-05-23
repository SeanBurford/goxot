# AGENTS.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Purpose

`tun-loopback` solves the kernel limitation that prevents a local `AF_X25 connect()` from reaching a local `AF_X25 listen()` socket. It creates one `ARPHRD_X25` TUN interface per address in `tun-loopback.routes`, adds an X.25 route for each address, and relays frames between the TUN file descriptors in userspace. See `docs/tech/linux_x25_routing.md` for the full design rationale.

Requires root.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `-config` | `config.json` | Config file path |
| `-tun-prefix` | `tunlb` | Name prefix for generated TUNs (`tunlb0`, `tunlb1`, …) |
| `-trace` | false | Log every forwarded packet |
| `-stats-port` | 0 (off) | `/varz` HTTP port |

## Config section

```json
"tun-loopback": {
  "lci_start": 1024,
  "lci_end":   4095,
  "stats-port": 8004,
  "routes": ["127100", "127200"]
}
```

Each address in `routes` gets its own TUN interface (e.g. `tunlb0` for `routes[0]`). An exact-match X.25 route (`sigdigits = len(address)`) is added pointing to that TUN.

`lci_start`/`lci_end` define the relay's LCI namespace on destination TUNs. Keep `lci_start` above the kernel's allocation range (usually > 255) to prevent collisions.

## Key types

**`tunNode`** — one TUN in the relay:
- `ifce *tun.Interface` — TUN fd
- `name`, `address` — interface name and routed X.121 address
- `linkState int32` — atomic: 0=Down, 1=Connecting, 3=Operational
- `wmu sync.Mutex` + `wbuf []byte` — serialises and pre-allocates writes to avoid hot-path allocation
- `lciMu`, `nextLCI`, `lciStart`, `lciEnd` — round-robin relay LCI allocator for calls arriving at this node as destination

**`loopSession`** — one forwarded call:
- `(tunA, lciA)` ↔ `(tunB, lciB)` — source/destination node indices and LCIs

**`sessionManager`** — bidirectional session index:
- `slots [][]atomic.Pointer[loopSession]` — `slots[nodeIdx][lci]`; both A-side (kernel LCI on tunA) and B-side (relay LCI on tunB) entries point to the same session. Direct array indexing — no hash, no mutex on reads.
- `usedLCI []map[uint16]bool` — per-node tracking of relay LCIs in use on the B side; guarded by `mu`
- `mu sync.Mutex` — guards writes only (CALL_REQ setup, CLR_CONF teardown); not held on the data-packet read path

## Goroutine model

One `handleTunRead` goroutine per TUN. Session lookups on the hot path (data packets) are lock-free atomic loads. Writes to the session table (CALL_REQ and CLR_CONF) are serialised by `sm.mu`.

## Packet relay hot path

`forwardPacket` is called for every non-CALL_REQ packet:
1. `sm.get(nodeIdx, lci)` — single `atomic.Pointer.Load()`, no lock
2. Two byte writes to remap LCI in-place (payload aliases the read buffer — write completes before next `ReadFrame`)
3. Data fast-path: `(pktType & 0x01) == 0` → `peerNode.writeFrame()` immediately, skipping the control-packet switch
4. `peerNode.writeFrame()` — holds `wmu`, writes using pre-allocated `wbuf` via `tun.WriteFrameBuf`

## LCI conflict avoidance

When a CALL_REQ arrives on `src` destined for `dst`:
1. `sm.mu.Lock()` is held while calling `dst.allocRelayLCI(inUse)` and inserting the session — ensures the "is LCI in use?" check and the reservation are atomic.
2. `allocRelayLCI` scans round-robin from `nextLCI` within `[lciStart, lciEnd]`, skipping any LCI already in `sm.usedLCI[dst.idx]`.
3. The forwarded CALL_REQ has its LCI remapped to the allocated relay LCI before being written to `dst`.

## L2 handshake

Each TUN's reader goroutine handles the handshake independently:
- Proactive `HeaderConnect` sent at startup (COMPAT003)
- `HeaderConnect` echo → `CompareAndSwap(Down → Connecting)`
- `RESTART_REQUEST` → send `RESTART_CONF`, `Store(Operational)` (COMPAT004/COMPAT005 handled)
- `HeaderDisconnect` → `clearAllForNode`, `Store(Down)`

## Session teardown order

`forwardPacket` removes sessions **only on CLR_CONF**, not on CLR_REQ. This differs from tun-gateway: the peer kernel auto-generates CLR_CONF in response to receiving CLR_REQ, and the relay needs the session alive to forward that CLR_CONF back. On CLR_CONF, an ABA check (`sm.get(src.idx, lci) == s`) is done under `sm.mu` before calling `sm.remove` to avoid removing a replacement session that reused the same LCI slot.
