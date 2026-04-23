# X.25 Session Handling Analysis

Analysis of goxot's session lifecycle management against `docs/tech/linux_x25_and_tun.md` and `docs/tech/goxot_state_management.md`. Issues are identified with `SESSxxx` identifiers.

Source references are to the `x25-6.12.74+deb13+1-amd64` kernel module tree and the goxot `src/` directory.

---

## SESS001 — CLEAR_CONFIRMATION from kernel after remote-initiated clear is not forwarded

**Severity**: Medium (X.25 protocol deviation; harmless over RFC 1613 in most cases)

**Location**: `src/cmd/tun-gateway/main.go:436–458` (handleServerConn), `main.go:596–601` (handleTunRead)

**Description**: When the remote server sends a `CLEAR_REQUEST`, the following sequence occurs:

1. `handleServerConn` receives CLR_REQ from XOT peer.
2. It remaps the LCI, writes CLR_REQ to TUN, calls `sm.RemoveSession(s)`, and returns.
3. The kernel's `x25_state3_machine` receives the CLR_REQ, sends a `CLEAR_CONFIRMATION` back via TunHeaderData, and calls `x25_disconnect()`.
4. `handleTunRead` reads the CLR_CONF from TUN.
5. No session is found for the LCI (removed in step 2), so the CLR_CONF is silently discarded (falls into the `NO_SESSION` trace path).
6. The CLR_CONF is never forwarded to the remote XOT peer.

Per X.25 and RFC 1613, the clearing peer expects to receive a CLR_CONF to complete the three-way clearing handshake. In practice, the XOT TCP connection carries this implicitly (the TCP stream continues), but the X.25 state machine on the remote side may log a protocol error or timeout.

A secondary issue: `handleServerConn` returns immediately after forwarding the CLR_REQ, which closes `destConn`. This prevents the CLR_CONF from being sent even if the session were preserved.

**Reference**: `main.go:436–458`, `main.go:580–601`, `x25_in.c:229–235` (x25_state3_machine CLR_REQ handling)

**Suggested fix**: Preserve the session entry until the CLR_CONF is received from the kernel, then forward it before removing the session:
```
handleServerConn receives CLR_REQ
  → forward to TUN, mark session as P5 (do not remove yet)
  → do NOT return immediately
handleTunRead receives CLR_CONF
  → session found (still in P5)
  → forward CLR_CONF to ConnB
  → RemoveSession
```

---

## SESS002 — RESTART_REQUEST from kernel during active calls treated as decorative

**Severity**: Low (risk of stale sessions; intentional trade-off)

**Location**: `src/cmd/tun-gateway/main.go:527–540`

**Description**: When `handleTunRead` receives a `RESTART_REQUEST` from the kernel, it responds with `RESTART_CONFIRMATION` but does **not** clear active sessions. The comment explains:

> "We don't wipe sessions here because the Linux kernel often sends a decorative RESTART right as the interface comes up, even if it's currently in the middle of accepting a call (flapping)."

The risk is: if the kernel sends a genuine `RESTART_REQUEST` (e.g., state desynchronisation, T20 timeout), all its internal virtual circuits have been reset to state 0 (`X25_LINK_STATE_3` reached via `x25_link_control:X25_RESTART_REQUEST:X25_LINK_STATE_3` path, which calls `x25_kill_by_neigh`). The goxot session manager still contains the old entries, which now reference dead kernel sockets. Subsequent packets relayed for those LCIs will be silently discarded by the kernel.

This is a deliberate trade-off to avoid false session teardown on startup flapping. The correct long-term fix would be to distinguish a genuine restart from a startup restart, or to implement a heartbeat mechanism.

**Reference**: `main.go:527–540`, `x25_link.c:64–127` (x25_link_control RESTART handling)

---

## SESS003 — StateP3 (DCE Waiting) never set; documented state is unreachable

**Severity**: Low (documentation inconsistency)

**Location**: `src/session.go`, `src/cmd/tun-gateway/main.go`

**Description**: `goxot_state_management.md` and `x25_states.md` both document a `p3` state (DCE Waiting — after receiving a CALL_REQ, before sending CALL_ACCEPTED). In practice, tun-gateway never sets a session to `StateP3`. Incoming CALL_REQs received via XOT are immediately relayed to the TUN (kernel) via `handleServerConn`, where the kernel auto-accepts them (the `X25_ACCPT_APPRV_FLAG` is set by default on new sockets, causing immediate CALL_ACCEPTED in `x25_rx_call_request`, `af_x25.c:1078–1081`).

Sessions allocated in `getTunLCI` start in `StateP1` and transition directly to `StateP4` on CALL_CONNECTED. `StateP3` is a valid state concept but has no code path that sets it in the current relay model.

This is acceptable behaviour for a transparent relay but the state documentation implies a manual accept capability that does not exist.

**Reference**: `session.go:15`, `main.go:109–118`, `af_x25.c:1076–1083`

---

## SESS004 — `cleanupConn` sends CLR_REQ for sessions regardless of state

**Severity**: Low (spurious CLR_REQ packets; mostly harmless)

**Location**: `src/cmd/tun-gateway/main.go:120–132`

**Description**: `cleanupConn` is called via `defer` when a server connection drops. It iterates all sessions for the connection and sends a `CLEAR_REQUEST` (cause `CauseOutofOrder = 0x09`) to the kernel for each:

```go
clr := xot.CreateClearRequest(s.LciA, xot.CauseOutofOrder, 0)
WriteTun(tg.ifce, TunHeaderData, clr.Serialize())
tg.sm.RemoveSession(s)
```

This is sent regardless of session state:
- For sessions in `StateP5` (clearing already in progress), a duplicate CLR_REQ is sent. The kernel is already in `X25_STATE_2` (awaiting CLR_CONF), and receiving another CLR_REQ causes it to send a CLR_CONF and call `x25_disconnect()` — this is actually harmless and accelerates cleanup.
- For sessions in `StateP1` that were never confirmed (allocated via `getTunLCI` but no CALL_REQUEST yet written to TUN), the kernel has no socket for the LCI. The CLR_REQ will be discarded with a debug log ("unknown frame type").
- For sessions in `StateP2` (CALL_REQUEST sent to kernel, no CALL_ACCEPTED yet), the kernel socket is in `X25_STATE_1`. Receiving a CLR_REQ while in state 1 causes the kernel to send CLR_CONF and call `x25_disconnect()` with `ECONNREFUSED`. This is correct.

The only wasted packets are for `StateP1` sessions (no kernel state). The rest are handled correctly.

**Reference**: `main.go:120–132`, `x25_in.c:149–155` (state1_machine CLR_REQ handling)

---

## SESS005 — Race condition in `closeAllSessions`

**Severity**: Low (theoretical; requires concurrent connection close and link termination)

**Location**: `src/cmd/tun-gateway/main.go:140–151`, `src/session.go:136–145`

**Description**: `closeAllSessions` calls `sm.GetAllSessions()` (which takes a read lock, copies session pointers, and releases the lock), then iterates the copied slice and sends CLR_REQ. Between `GetAllSessions` returning and the iteration completing, another goroutine (e.g., `handleServerConn` completing a CLR_REQ relay) could call `sm.RemoveSession(s)`. The session pointer remains valid (Go GC), but the session is no longer in the manager. The CLR_REQ is still sent to the kernel for the removed session's LCI, which is harmless (kernel discards it or processes the duplicate).

A more serious race: if two goroutines both reach `closeAllSessions` concurrently (e.g., via SOCK004 fix + signal handler), sessions could receive duplicate CLR_REQs on both the kernel TUN and the remote TCP.

**Reference**: `main.go:140–151`, `session.go:136–145`

**Suggested fix**: Hold the mutex across the entire close operation, or use a "closing" atomic flag to prevent double execution.

---

## SESS006 — `x25_states.md` RESTART handling description contradicts implementation

**Severity**: Low (documentation inconsistency)

**Location**: `docs/tech/x25_states.md:37–38`

**Description**: The documentation states:

> "When the Linux kernel (DCE side of our TUN) sends a `RESTART_REQ`, the `tun-gateway` immediately responds with a `RESTART_CONF`. This effectively clears all active virtual circuits on that interface and returns them to the **p1** (Ready) state."

The implementation does **not** clear sessions on RESTART_REQUEST (see SESS002). The documentation overstates what the code does. The RESTART_CONF is sent but sessions are preserved.

**Reference**: `docs/tech/x25_states.md:37–38`, `main.go:527–540`

**Suggested fix**: Update `x25_states.md` to reflect that RESTART_CONFIRMATION is sent but sessions are **not** cleared by the gateway on RESTART_REQUEST, and explain the rationale (startup flapping avoidance).

---

## SESS007 — `handleGatewayRead` CLR_CONF path uses `s.ConnB` after `RemoveSession`

**Severity**: Low (no functional bug; fragile code pattern)

**Location**: `src/cmd/tun-gateway/main.go:725–734`

**Description**: In `handleGatewayRead`, when a CLR_REQ or CLR_CONF is received from the gateway:

```go
WriteTun(tg.ifce, TunHeaderData, pkt.Serialize())
tg.sm.RemoveSession(s)
return
```

`pkt.LCI` has already been remapped to `s.LciA` before this block (line 708), so `WriteTun` sends the correct TUN LCI. The `RemoveSession` call happens after the write, so there is no use-after-free. However, if a future refactor moves the `RemoveSession` call earlier or the packet serialisation is deferred, this ordering dependency could become a bug.

**Reference**: `main.go:700–734`

---

## SESS008 — `xot-server` session manager LCI range overlaps with kernel's range

**Severity**: Low (potential LCI collision when routing between TUN and XOT)

**Location**: `src/cmd/xot-server/main.go:35`

**Description**: `xot-server` creates its own `SessionManager` with LCI range 1–4095:

```go
sm = xot.NewSessionManager(1, 4095)
```

However, `xot-server` performs blind bidirectional relay and does not track sessions by LCI internally — it uses the `sm` variable only as a namespace but never calls `AllocateTunLCI` or maintains the LCI index. The `sm` object is unused after initialisation. This is dead code in `xot-server`.

In contrast, `tun-gateway` uses a `SessionManager` with a configurable range (default 1–255 from `TunConfig`). If the kernel also allocates LCIs in the same range (it starts from 1 and increments), LCI collisions between gateway-allocated LCIs and kernel-allocated LCIs are possible without coordination via `SIOCX25SSUBSCRIP`.

**Reference**: `src/cmd/xot-server/main.go:35`, `src/cmd/tun-gateway/main.go:329–332`, `af_x25.c:336–361` (x25_new_lci)

---

## SESS009 — Session cleanup on `TunHeaderDisconnect` does not await CLR_CONF before teardown

**Severity**: Low (correct for forced teardown; violates X.25 state machine)

**Location**: `src/cmd/tun-gateway/main.go:140–151` (`closeAllSessions`)

**Description**: `closeAllSessions` sends CLR_REQ to all remote XOT peers and immediately removes sessions without waiting for CLR_CONF. This is correct behaviour for a forced link teardown (the kernel has already killed all internal sockets), but it means remote peers never receive CLR_CONF for the CLR_REQ they were expecting if they too were in a clearing handshake.

For RFC 1613 over TCP, this is generally acceptable — the TCP connection closure itself signals the end of the XOT session. For applications that strictly enforce X.25 state machines, the missing CLR_CONF may trigger error logging.

**Reference**: `main.go:140–151`, RFC 1613 Section 3.1
