# Short Receive Analysis

Analysis of the `Short receive: expected N, got -1` error observed on server 2 during stress testing. Issues are identified with `SRxxx` identifiers.

---

## SR001 — Spurious CLR_REQ from `cleanupConn` kills a freshly-reused LCI

**Severity**: High (active calls are killed; receiver returns `read() = -1`)

**Symptom**

Under load, server 2 reports:
```
Thread 0: Short receive between 127001164200195/127800: expected 3342, got -1
```

`got -1` is a `read()` / `recv()` error on the AF_X25 socket — caused by the kernel receiving a CLR_REQ for a socket that is in `X25_STATE_3` (Data Transfer) or `X25_STATE_1` (Awaiting Call Accepted). The call is killed mid-transfer.

**Observed trace on server 2 tun-gateway (`/tmp/out.log`)**

```
TUN(3)>SVR(9)  CLR_CONF  20 02 17                    ← (A) CLR_CONF forwarded to SVR(9), LCI=2
TUN: Cleaning up LCI 1024 - sending CLEAR_REQ to kernel  ← (B) spurious CLR_REQ from cleanupConn
TUN: Clear Confirmation from kernel on LCI 2             ← (C) kernel responds to (B)
SVR(10)>TUN(3) CALL_REQ  20 02 0B ...                ← (D) new call from SVR(10) allocated LCI 1024
TUN(3)>SVR(10) CALL_CONN 20 02 0F ...                ← (E) CALL_CONNECTED received
TUN: Call connected on LCI 2
TUN(3)>SVR(10) CLR_CONF  20 02 17                    ← (F) CLR_CONF from (B) kills SVR(10)'s call
TUN: Clear Confirmation from kernel on LCI 2
```

Events (D)–(F) show a live call being created and immediately destroyed by the spurious clear initiated at (B).

**Root cause — Bug 1: Wrong `RemoveSession` ordering in `handleTunRead`**

The server-initiated clearing handshake (SVR sends CLR_REQ → kernel sends CLR_CONF) proceeds through two goroutines:

1. `handleServerConn` (SVR(9)): reads CLR_REQ, sets session to `StateP5`, writes CLR_REQ(LciA=1024) to TUN.
2. `handleTunRead`: reads CLR_CONF from kernel, then executes:

```go
// BEFORE fix:
xot.SendXot("unix", s.ConnB, oldData)  // ← forwards CLR_CONF to SVR(9)
tg.sm.RemoveSession(s)                 // ← removes session from map
```

`SendXot` to SVR(9) delivers the CLR_CONF to xot-server's `relay_dest_to_source` goroutine, which calls `closeRelay()` and closes `destConn`. This causes `handleServerConn` to return from `ReadXotInto` with EOF **immediately** — before `handleTunRead` has called `RemoveSession`.

`handleServerConn`'s `defer cleanupConn(conn)` fires. `cleanupConn` calls `GetSessionsForConn(SVR(9))`, which still finds the LCI-1024 / LciB=2 session in the map (state=P5, pointer unchanged). Both guards pass:

```go
if s.State != xot.StateP1 && tg.sm.GetByALCI(s.LciA) == s {
    WriteTun(..., CLR_REQ(LciA=1024))   // spurious
}
```

The CLR_REQ is written to the kernel for LciA=1024.

**Root cause — Bug 2: Linear LCI allocation always starts from `tunLciStart`**

`AllocateAndAddTunSession` scanned from `tunLciStart` (1024) on every call. After the CLR_CONF removes the session, LCI 1024 is immediately the lowest free LCI and is reallocated to the very next CALL_REQ (SVR(10) at event (D)). The spurious CLR_REQ written by `cleanupConn` (still in flight in the kernel's TUN queue) arrives while LCI 1024 is now owned by SVR(10)'s new call. The kernel kills it.

**Sequence of the combined bug**

```
handleTunRead                  handleServerConn (SVR(9))       handleServerConn (SVR(10))
─────────────────────────────  ──────────────────────────────  ──────────────────────────────
reads CLR_CONF (LciA=1024)
LogTraceRaw → log line (A)
SendXot CLR_CONF → SVR(9)  →  relay sees CLR_CONF
                           →  closeRelay / destConn.Close()
                           →  ReadXotInto = EOF
                           →  defer cleanupConn(SVR(9))
                           →    GetSessionsForConn → [s P5]
                           →    GetByALCI(1024) == s → TRUE
                           →    WriteTun CLR_REQ(1024)  ← log line (B)
                           →    RemoveSession(s)
RemoveSession(s) ← no-op
                                                                CALL_REQ received
                                                                AllocateTunLCI → 1024 ← log line (D)
reads CLR_CONF (LciA=1024) ← kernel response to (B)           ← kills SVR(10)'s call ← log line (F)
```

**Fix**

Two changes were made:

**Fix 1 — `src/cmd/tun-gateway/main.go`: call `RemoveSession` before `SendXot`**

In `handleTunRead`, for both the CLR_CONF path and the kernel-CLR_REQ path, `RemoveSession` is now called *before* forwarding the packet to the peer connection. Once the session is out of the map, any concurrent `cleanupConn` call via `GetSessionsForConn` returns an empty slice — the spurious CLR_REQ cannot be generated.

The local `s` pointer and `s.ConnB` remain valid (Go GC keeps the struct alive); `SendXot` still works correctly.

```go
// CLR_CONF path — was: SendXot then RemoveSession
tg.sm.RemoveSession(s)
xot.SendXot("unix", s.ConnB, oldData)

// CLR_REQ (kernel) path — was: WriteTun, SendXot, RemoveSession
WriteTun(tg.ifce, TunHeaderData, confBuf)  // CLR_CONF to kernel
tg.sm.RemoveSession(s)
xot.SendXot("unix", s.ConnB, oldData)      // CLR_REQ to peer
```

**Fix 2 — `src/session.go`: round-robin LCI allocation**

`AllocateAndAddTunSession` now uses a `nextLCI` cursor (persisted in `SessionManager`) that advances past the most recently allocated LCI. On the next allocation request, the scan starts from `nextLCI` rather than `tunLciStart`, cycling through the full range before wrapping. A just-freed LCI is therefore the *last* candidate rather than the first.

This is defence-in-depth: even if a late `cleanupConn` fires with a stale session pointer (e.g., due to an unrelated goroutine delay), the session's LciA will not yet have been reallocated to a new call.

```
Before: always scans 1024, 1025, 1026, ...  → 1024 reallocated immediately after free
After:  scans from nextLCI → wraps → avoids 1024 until rest of range is exhausted
```

**Status**: Resolved — both fixes applied. `go test ./...` passes.

---

## Summary

| ID | Severity | Component | Description |
|:---|:---|:---|:---|
| SR001 | High | `handleTunRead` / `cleanupConn` | Spurious CLR_REQ from deferred cleanup fires between `SendXot` and `RemoveSession`, killing a freshly-reallocated LCI |
