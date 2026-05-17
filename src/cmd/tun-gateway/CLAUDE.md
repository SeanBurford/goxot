# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Purpose

`tun-gateway` is the only privileged component in the goxot stack (requires root). It owns the Linux TUN interface (`ARPHRD_X25`) and bridges kernel X.25 sockets to the rest of the stack. It remaps LCIs between the kernel's namespace and the gateway's namespace, and intercepts outbound calls to route them through `xot-gateway`.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `-tun` | `tun0` | TUN interface name |
| `-config` | `config.json` | Config file path |
| `-trace` | false | Enable packet trace logging |
| `-stats-port` | 0 (off) | `/varz` stats HTTP port |

## Goroutine map

| Goroutine | Role |
|---|---|
| `main` | Setup, proactive CONNECT to TUN, accept loop on `/tmp/xot_tun.sock` |
| `watch_config` | inotify on config file → `cm.Reload()` + `SyncRoutes()` |
| `tun_read_handler` | Continuous read from TUN fd → `handleTunRead()` |
| `signal_handler` | SIGINT/SIGTERM → set `shuttingDown`, close sessions, send DISCONNECT to TUN |
| `server_conn_handler` (per conn) | Read from xot-server unix socket → remap LCI → write to TUN |
| `gateway_read_handler` (per conn) | Read from xot-gateway → remap LCI → write to TUN |

## TUN link state machine

`linkState` is an `int32` accessed with `atomic.CompareAndSwap`/`Store`/`Load`.

```
Down (0)
  → Connecting (1)   proactive TunHeaderConnect sent at startup (COMPAT003),
                     or kernel sends CONNECT
  → Operational (3)  on RESTART_REQ from kernel while Connecting;
                     we respond with RESTART_CONF
  → Down (0)         kernel sends DISCONNECT, or signal handler
```

Packets are dropped unless `linkState == 3` (Operational).

**Duplicate RESTART_REQ** (COMPAT004/COMPAT005): if we're already Operational with no active sessions, the RESTART_REQ is treated as a spurious duplicate and ignored.

## TUN packet framing

Every TUN read/write has a 5-byte PI header before the X.25 payload:

```
[flags 2B][proto 2B = 0x0805][header_byte]
```

`header_byte` values: `0x00` = Data, `0x01` = Connect, `0x02` = Disconnect, `0x03` = Param.

## LCI remapping

The kernel allocates LCIs starting at 1. The gateway uses a configurable range (default 1024–4095, set via `TunConfig.LciStart/LciEnd`). Sessions carry both sides:

```
Session.LciA  = TUN-side LCI  (kernel namespace)
Session.LciB  = gateway-side LCI
Session.ConnB = net.Conn to xot-server or xot-gateway
Session.ConnA = nil  (TUN is a file fd, not a net.Conn)
```

**In-place LCI substitution** (used in all four relay paths):
```go
data[0] = (data[0] & 0xF0) | byte((newLCI>>8)&0x0F)
data[1] = byte(newLCI & 0xFF)
```

**Server → TUN**: `GetByBConnLCI(conn, incomingLCI)` → use `session.LciA` for write to TUN.  
**TUN → Server**: `GetByALCI(pLCI)` → use `session.LciB` / `session.ConnB` for send via `SendXot`.

## Call routing

**Inbound call** (xot-server → TUN): `handleServerConn()` allocates a new session via `AllocateAndAddTunSession(conn, incomingLCI)`, remaps LCI, writes CALL_REQ to TUN.

**Outbound call** (TUN kernel → remote): `handleTunRead()` receives CALL_REQ on a TUN-side LCI. If the called address matches a configured server prefix, the call is **intercepted**:
- Copy packet bytes before goroutine spawn (RACE-A).
- Create session with `LciA = pLCI, LciB = pLCI, ConnB = new gateway conn`.
- Spawn `forwardToGateway()` which dials `/tmp/xot_gwy.sock` and relays.

If not intercepted, the packet passes through to an existing session.

## Config hot-reload

`watchConfig()` uses inotify (`IN_MODIFY | IN_CLOSE_WRITE`). On change:
1. `cm.Reload()` re-parses `config.json`.
2. `SyncRoutes()` diffs `tg.currentRoutes` against new config and issues `SIOCADDRT`/`SIOCDELRT` ioctls. Protected by `routeMu`.

LCI range changes take effect only on restart (no live reconfiguration of the TUN subscription).

## Race condition catalogue

| Tag | Location | Issue | Fix |
|---|---|---|---|
| RACE-A | `handleTunRead` CALL_REQ intercept | X25Packet.Payload aliases TUN read buffer; goroutine may see corrupted data | Copy full packet bytes before goroutine spawn |
| RACE-B | `handleTunRead` unknown-LCI path | Stale kernel socket lingers without CLR_REQ | Send CLR_REQ unconditionally for non-CLEAR packets on unknown LCIs |
| RACE-D | `SendXot` on stream sockets | Concurrent writes ≥4096 bytes can interleave | `sync.Mutex` on writes to the same stream conn |
| SESS004 | `cleanupConn` | Freed LCI may be reallocated; sending CLEAR for wrong session | Check `sm.GetByALCI(s.LciA) == s` before sending |
| SESS005 | shutdown | Multiple goroutines racing to remove sessions | `RemoveAllSessions()` atomically returns snapshot; iterate snapshot |

**Session removal before send**: when forwarding CLR_REQ or CLR_CONF to a peer, remove the session from the manager **before** the send. The peer may close its socket immediately, triggering `cleanupConn()`; if the session is still indexed, `cleanupConn()` will send a spurious CLEAR on the recycled LCI.

## Shutdown sequence (signal handler)

1. `atomic.Store(&shuttingDown, 1)` — new packets check this and return early.
2. Close unix socket listener — stops accepting new connections.
3. `closeAllSessions()` — `RemoveAllSessions()` snapshot, send CLR_REQ to TUN for each (SESS005).
4. Send `TunHeaderDisconnect` to TUN (COMPAT010).
5. Close TUN fd — triggers `NETDEV_DOWN` promptly.
