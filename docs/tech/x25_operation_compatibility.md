# X.25 Connection Operation Compatibility

Analysis of the six Connection Operations defined in `linux_x25_and_tun.md` for internal self-consistency and for conflicts that arise when operations run concurrently — whether on the same connection, on different connections sharing a TUN interface, or across processes sharing the same machine's X.25 routing table.

Issues are identified with `COMPATxxx` identifiers.

Source references are to the `x25-6.12.74+deb13+1-amd64` kernel module tree and the goxot `src/` directory.

---

## Operations Reference

| # | Name | Key side effects |
|:--|:-----|:----------------|
| Op1 | Open X25 Packet Socket in PI mode | Creates TUN neighbor in STATE\_0; link transitions to STATE\_3 after handshake |
| Op2 | Open an X.25 Connection | Allocates LCI; queues CALL\_REQUEST; blocks in connect() until CALL\_ACCEPTED |
| Op3 | Close an X.25 Connection | Kernel sends CLR\_REQ to TUN; socket enters STATE\_2; T23 starts |
| Op4 | Receive remote close notification | Kernel sends CLR\_CONF to TUN; socket moves to STATE\_0 |
| Op5 | Receive remote disconnect notification | `x25_kill_by_neigh()` has already run; gateway calls `closeAllSessions()` |
| Op6 | Clear all connections and shut down | Sends CLR\_REQs to XOT peers; writes TunHeaderDisconnect; closes fd |

---

## Concurrency Model Requirements

Before discussing conflicts, two mandatory concurrency constraints apply to all operations:

### COMPAT001 — TUN fd must have exactly one reader goroutine

**Affects**: All operations.

**Mechanism**: `read(tun_fd, ...)` is not a pub-sub queue; each frame is delivered to exactly one caller. With multiple goroutines reading concurrently, frames are distributed non-deterministically. The goroutine that reads a `TunHeaderConnect` frame must also echo it; if that goroutine is not the one responsible for echoing, the handshake is missed and the link never reaches `X25_LINK_STATE_3`. Similarly, CLR\_REQ and CLR\_CONF frames for active sessions must be correlated with session state — which is only possible if there is a single reader that dispatches frames by type.

**Classification**: User-mode architectural constraint. Not a kernel bug.

**Required pattern**: Dedicate one goroutine to reading from the TUN fd. That goroutine dispatches all frame types: TunHeaderConnect, TunHeaderDisconnect, RESTART\_REQUEST, CALL\_REQUEST, CALL\_ACCEPTED, CLR\_REQ, CLR\_CONF, and data.

---

### COMPAT002 — `connect()` caller and TUN reader must be separate goroutines

**Affects**: Op2 × Op1.

**Mechanism**: When `connect()` is called and the link is in `X25_LINK_STATE_0`, `x25_transmit_link()` (`x25_link.c:214–218`) queues the CALL\_REQUEST, sets state to `X25_LINK_STATE_1`, and calls `x25_establish_link()`, which writes `TunHeaderConnect (0x01)` to the TUN device. The `connect()` call then blocks in `x25_wait_for_connection_establishment()`, waiting for a CALL\_ACCEPTED or CLEAR\_REQUEST to be written back to the kernel via the TUN fd.

The TUN reader goroutine must read the `TunHeaderConnect`, echo it, read the `RESTART_REQUEST`, send `RESTART_CONFIRMATION`, and relay the eventual `CALL_ACCEPTED` — all before `connect()` can return. If `connect()` and the TUN reader share the same goroutine, the goroutine blocks inside `connect()` and never reads from TUN. T21 (default 180 s) fires and `connect()` returns `ETIMEDOUT`.

**Classification**: User-mode architectural constraint. Not a kernel bug.

**Required pattern**: The TUN reader goroutine must be started and reading before any goroutine calls `connect()`.

---

## Compatibility Matrix

The table below summarises which pairs of concurrent operations have known conflicts. "Safe" means no conflict was identified.

|          | Op1 | Op2 | Op3 | Op4 | Op5 | Op6 |
|:---------|:----|:----|:----|:----|:----|:----|
| **Op1**  | —   | COMPAT003, COMPAT004 | COMPAT004 | COMPAT004 | COMPAT005 | COMPAT004 |
| **Op2**  |     | COMPAT006 | COMPAT007 | COMPAT007 | COMPAT008 | COMPAT009 |
| **Op3**  |     |     | Safe | COMPAT007 | Safe | Safe |
| **Op4**  |     |     |     | Safe | Safe | Safe |
| **Op5**  |     |     |     |     | Safe | COMPAT009 |
| **Op6**  |     |     |     |     |     | — |

Cross-process (same machine):

| Op pair | Issue |
|:--------|:------|
| Op1 (two processes) | COMPAT010 |
| Op1 + any Op (route modification) | COMPAT011 |

---

## Detailed Findings

---

### COMPAT003 — Kernel queues all outbound frames until STATE\_3; incoming CALL\_REQs are accepted immediately

**Severity**: High (silent data loss; sessions appear established but no kernel traffic flows)

**Affects**: Op1 × Op2, Op1 × Op4

**Mechanism**: The kernel's outbound path, `x25_transmit_link()` (`x25_link.c:212–228`), checks the link state before sending:

```c
case X25_LINK_STATE_0:
    skb_queue_tail(&nb->queue, skb);
    nb->state = X25_LINK_STATE_1;
    x25_establish_link(nb);     /* sends TunHeaderConnect to TUN */
    break;
case X25_LINK_STATE_1:
case X25_LINK_STATE_2:
    skb_queue_tail(&nb->queue, skb);  /* queue, do not send */
    break;
case X25_LINK_STATE_3:
    x25_send_frame(skb, nb);   /* send immediately */
    break;
```

All CALL\_REQUESTs (from `connect()`), CALL\_ACCEPTEDs (for inbound calls), CLR\_REQs, CLR\_CONFs, and data frames are queued until STATE\_3. They are flushed by `x25_link_control()` (`x25_link.c:124–126`) when STATE\_3 is entered.

**Asymmetry**: The inbound path (`x25_lapb_receive_frame` → `x25_receive_data`) has **no link state check**. CALL\_REQs written to the TUN fd by the gateway are processed by the kernel regardless of the current link state. The kernel can accept an incoming CALL\_REQ and create a connected socket in STATE\_3, but it cannot send the resulting CALL\_ACCEPTED back to the TUN until the link is in STATE\_3.

**Consequence**: If the gateway writes a CALL\_REQ to TUN before the RESTART handshake is complete (Op4 triggered concurrently with Op1 steps 6–7), the kernel accepts it and tries to send CALL\_ACCEPTED — but CALL\_ACCEPTED is queued. The queue is flushed when RESTART\_CONF completes the handshake. If RESTART\_CONF is never sent (gateway crash, Op5 teardown), CALL\_ACCEPTED is never delivered, the remote DTE times out, and the kernel socket leaks until T21 fires.

**Classification**: Expected kernel behavior. User-mode code must track whether STATE\_3 has been reached and either defer writing CALL\_REQs to TUN or be prepared to complete the handshake promptly.

**Workaround**: Send `TunHeaderConnect` proactively to TUN immediately after Op1 step 4 (SIOCSIFFLAGS). The kernel receives `X25_IFACE_CONNECT` → `x25_link_established()` → STATE\_2 → sends RESTART\_REQUEST. Complete the RESTART handshake before forwarding any XOT CALL\_REQs to TUN. This pre-establishes STATE\_3 without waiting for an AF\_X25 socket to trigger it.

**Status**: Partially resolved — tun-gateway proactively writes `TunHeaderConnect` on startup and tracks `linkState` atomically. Packets from XOT peers are rejected with a CLR\_REQ when `linkState != LinkStateOperational`, preventing data from being forwarded before STATE\_3 is established. The kernel-side asymmetry (inbound CALL\_REQs written to TUN are accepted regardless of link state) remains; the gateway's proactive connect ensures STATE\_3 is reached quickly on startup.

---

### COMPAT004 — Duplicate RESTART\_CONFIRMATION in STATE\_3 kills all active sockets

**Severity**: Medium (all active connections destroyed; link forced back to STATE\_2)

**Affects**: Op1 × Op2, Op1 × Op3, Op1 × Op4, Op1 × Op6

**Mechanism**: `x25_link_control()` handles `RESTART_CONFIRMATION` in each link state (`x25_link.c:92–107`):

```c
case X25_RESTART_CONFIRMATION:
    switch (nb->state) {
    case X25_LINK_STATE_2:
        x25_stop_t20timer(nb);
        nb->state = X25_LINK_STATE_3;   /* normal path */
        break;
    case X25_LINK_STATE_3:
        x25_kill_by_neigh(nb);          /* kills all sockets */
        x25_transmit_restart_request(nb);
        nb->state = X25_LINK_STATE_2;
        x25_start_t20timer(nb);
        break;
    }
```

If the kernel receives a `RESTART_CONFIRMATION` while already in `STATE_3`, it kills all active sockets with `ENETUNREACH`, sends a new `RESTART_REQUEST`, and returns to `STATE_2`.

**How a duplicate can arise**: The T20 timer (`x25_t20timer_expiry`, default 180 s) retransmits `RESTART_REQUEST` while the link is in `STATE_2`. If the gateway is slow to respond, multiple `RESTART_REQUEST` frames accumulate in the TUN fd's receive buffer. When the gateway eventually sends `RESTART_CONFIRMATION` for the first frame (transitioning the kernel to STATE\_3 and stopping T20), the remaining buffered `RESTART_REQUEST` frames are still pending in the TUN fd. The TUN reader reads the next buffered `RESTART_REQUEST` and sends another `RESTART_CONFIRMATION`. The kernel (now in STATE\_3) receives it, destroys all active sockets, and goes back to STATE\_2.

**Classification**: Kernel behavior is correct per X.25. The user-mode bug is the gateway blindly responding to every RESTART\_REQUEST with RESTART\_CONFIRMATION without tracking link state.

**Workaround**: Maintain a gateway-side link state variable. Accept only one `RESTART_REQUEST` → `RESTART_CONFIRMATION` exchange per link establishment cycle. After the link reaches STATE\_3 (gateway's view), treat any subsequent `RESTART_REQUEST` read from TUN as a genuine restart event (COMPAT005 path) rather than a continuation of the current handshake.

**Status**: Resolved — `handleTunRead` tracks `linkState`. When a `RESTART_REQUEST` is received while `linkState == LinkStateOperational` and no active sessions exist, it is silently discarded rather than generating a `RESTART_CONFIRMATION`. This prevents the duplicate-CONF → kill-all-sockets → back-to-STATE\_2 cycle.

---

### COMPAT005 — RESTART\_REQUEST during STATE\_3 kills all active sockets without gateway session cleanup

**Severity**: Medium (stale sessions in gateway; packets to dead LCIs silently discarded)

**Affects**: Op1 × Op2, Op1 × Op3, Op1 × Op4

**Mechanism**: When the kernel receives a `RESTART_REQUEST` while in `X25_LINK_STATE_3` (`x25_link.c:83–89`):

```c
case X25_LINK_STATE_3:
    x25_kill_by_neigh(nb);
    x25_transmit_restart_confirmation(nb);
    break;   /* state remains X25_LINK_STATE_3 */
```

All AF\_X25 sockets are killed (`ENETUNREACH`), but the link state stays at STATE\_3 and the kernel immediately sends `RESTART_CONFIRMATION`. The kernel also remains in STATE\_3 after this (it does not transition back to STATE\_2). The gateway reads the resulting `RESTART_CONFIRMATION` from TUN.

The gateway's session manager still contains all the old session entries, but the sockets they refer to are now in STATE\_0. Any CALL\_REQ, data, or CLR frames subsequently written to TUN for those LCIs are either discarded (no socket found, `frametype != X25_CALL_REQUEST`) or routed to whatever new socket next acquires the same LCI — which is a different connection.

This is documented as SESS002 but has a direct compatibility impact: Op2/Op3/Op4 operations running concurrently with this event are aborted by the kernel without the gateway's knowledge.

**Classification**: Kernel behavior is correct per X.25. The user-mode issue (SESS002) is the gateway not clearing sessions on RESTART.

**Workaround**: In the TUN reader, when a `RESTART_REQUEST` is received while the gateway considers the link already operational (STATE\_3 from its perspective), treat it as a full restart: clear all active sessions (sending CLR\_REQ to each remote XOT peer), then send `RESTART_CONFIRMATION`. Distinguish startup flapping (RESTART\_REQUEST received immediately after link came up, before any sessions were established) from a genuine mid-session restart by checking whether any sessions are active.

**Status**: Partially resolved — when a `RESTART_REQUEST` is received in `LinkStateOperational` with active sessions, `closeAllSessions()` is called before sending `RESTART_CONFIRMATION`. When no sessions are active, the `RESTART_REQUEST` is discarded (COMPAT004 path). The heuristic is imperfect: a genuine restart that arrives before any sessions are established is still treated as decorative. See SESS002 for details.

---

### COMPAT006 — LCI collision between gateway SessionManager and kernel `x25_new_lci()`

**Severity**: High (CALL\_REQUEST silently discarded; session stranded in StateP2)

**Affects**: Op2 running in parallel (multiple concurrent connects or mixed inbound/outbound)

**Mechanism**: `x25_new_lci()` (`af_x25.c:336–361`) scans the `x25_list` (all bound AF\_X25 sockets) for a free LCI on the given neighbor:

```c
while (lci < 4096) {
    bool in_use = false;
    sk_for_each(s, &x25_list) {
        if (s != owner &&
            READ_ONCE(x25_sk(s)->lci) == lci &&
            x25_sk(s)->neighbour == nb) {
            in_use = true;
            break;
        }
    }
    if (!in_use) {
        WRITE_ONCE(x25_sk(owner)->lci, lci);
        break;
    }
    lci++;
}
```

The kernel has no knowledge of the gateway's `SessionManager` LCI allocations. The gateway allocates a TUN LCI in `getTunLCI()` for an incoming XOT call, then writes a `CALL_REQUEST` with that LCI to TUN. If an AF\_X25 socket calls `connect()` in the window between the `SessionManager` allocation and the TUN write, `x25_new_lci()` may assign the same LCI to the outbound socket (since no AF\_X25 socket yet uses it).

When the gateway then writes the CALL\_REQ with that LCI, `x25_receive_data()` finds the outbound AF\_X25 socket (in `X25_STATE_1`, waiting for CALL\_ACCEPTED), routes the CALL\_REQ to it via `x25_process_rx_frame()`. The state machine in `x25_state1_machine()` does not handle CALL\_REQUEST frames (only CALL\_ACCEPTED and CLEAR\_REQUEST), so the frame is dropped. The incoming XOT call's session remains in StateP2 indefinitely; T21 fires after 180 s and the outbound AF\_X25 socket is cleared.

**Classification**: User-mode LCI coordination bug. The kernel has no mechanism to reserve LCIs for user-space use.

**Workaround**: Use `SIOCX25SSUBSCRIP` to set a `global_facil_mask` that limits which LCIs the kernel uses for socket operations, and partition the LCI space. Alternatively, design the gateway so that either all calls are kernel-side (AF\_X25 sockets only) or all calls are gateway-injected (CALL\_REQs written to TUN), never both on the same interface.

**Status**: Partially resolved — default `TunConfig` LCI range changed from 1–255 to 1024–4095. `SIOCX25SSUBSCRIP` is called on startup with `Extended=1` to allow the extended LCI range. The kernel's AF\_X25 `x25_new_lci()` allocator starts from LCI 1 and is unlikely to reach 1024 in practice, giving an effective LCI partition. True kernel-enforced reservation of the 1024–4095 range for gateway use is not supported by the Linux X.25 subsystem.

---

### COMPAT007 — Simultaneous local close and remote close produce an unforwarded CLR\_CONF

**Severity**: Low (X.25 protocol deviation; remote peer may log an error or timeout)

**Affects**: Op3 × Op4 on the same LCI, Op2 × Op3

**Mechanism**: If both the local application and the remote DCE initiate clearing simultaneously:

1. Local: `close(fd)` → kernel sends CLR\_REQ to TUN, enters `X25_STATE_2`, T23 starts.
2. Remote: gateway writes CLR\_REQ to TUN for the same LCI.
3. Kernel (in `X25_STATE_2`) receives the remote CLR\_REQ via `x25_state2_machine` (`x25_in.c`): sends CLR\_CONF, calls `x25_disconnect()`.

The kernel's CLR\_CONF (step 3) appears in the TUN output. The gateway reads it, but the session has already been removed from the session manager (SESS001: the gateway removes the session as soon as it forwards the remote's CLR\_REQ in step 2). The CLR\_CONF is silently dropped in the `NO_SESSION` trace path; the remote peer never receives it.

Per X.25, the remote peer sent CLR\_REQ and is waiting for CLR\_CONF (now in X25\_STATE\_2 on its side). Not receiving CLR\_CONF means the remote's T23 timer (180 s) must expire before it considers the circuit free. Under heavy load with many simultaneous clears this accumulates stale remote state.

**Classification**: SESS001, a user-mode code bug in `handleServerConn` and `handleTunRead`.

**Workaround**: Do not remove the session immediately when forwarding a remote CLR\_REQ to TUN. Instead, mark it as "awaiting CLR\_CONF" (StateP5 equivalent) and keep it in the manager until the corresponding CLR\_CONF is read from TUN and forwarded to the remote peer. Only then call `RemoveSession`.

**Status**: Resolved — via SESS001 fix. Sessions are retained in `StateP5` after forwarding a remote CLR\_REQ; `RemoveSession` is only called after `handleTunRead` receives the kernel's CLR\_CONF, forwards it to the remote peer, and then removes the session. The remote peer receives CLR\_CONF and completes its three-way clearing handshake.

---

### COMPAT008 — Link teardown during call setup leaves StateP2 sessions unmanaged

**Severity**: Medium (resource leak; stranded session if timing is unfavorable)

**Affects**: Op5 × Op2

**Mechanism**: Op2's `connect()` blocks in `X25_STATE_1` waiting for CALL\_ACCEPTED. Op5 fires concurrently: the gateway reads `TunHeaderDisconnect` (or, pending SOCK004 fix, it should), calls `closeAllSessions()`. `x25_kill_by_neigh()` has already run in the kernel; `connect()` returns `ENETUNREACH`.

The problem arises if the gateway's session entry for the pending call was in the session manager at the time `closeAllSessions()` ran: it is cleaned up correctly. However, there is a window during Op2 between LCI allocation in `getTunLCI()` and CALL\_REQUEST being written to TUN. If Op5 fires in this window, `closeAllSessions()` finds no record of the session (it was not yet inserted) and sends no CLR\_REQ to the remote XOT peer. The XOT peer is left with its CALL\_REQUEST waiting for a CALL\_ACCEPTED that will never arrive.

Additionally: `x25_release()` (`af_x25.c:656–668`) sends CLR\_REQ for sockets in `X25_STATE_1`. If Op5's `x25_kill_by_neigh()` fires before `connect()` has returned, the CLR\_REQ from `x25_release` is never written to TUN (the link is now STATE\_0 and the neighbor is gone). This CLR\_REQ is silently dropped.

**Classification**: Race condition in user-mode session management. Not a kernel bug.

**Workaround**: Insert the session into the manager before writing the CALL\_REQ to TUN. This ensures `closeAllSessions()` always sees the session and sends CLR\_REQ to the remote peer, even if the kernel side is already dead. In the XOT handler, add session-null guard after `closeAllSessions()` returns to handle the race where the session was removed mid-flight.

**Status**: Partially resolved — a session-null guard is now present in `handleServerConn` to handle the race where a session is removed mid-flight. The pre-manager window race (session allocated in `getTunLCI` but not yet written to TUN before `closeAllSessions` runs) is not fully closed; a remote XOT peer's CALL\_REQUEST in this window will not receive a CLR\_REQ notification.

---

### COMPAT009 — Shutdown (Op6) races with new sessions arriving concurrently

**Severity**: Low (one or more sessions may survive shutdown; XOT connections left open)

**Affects**: Op6 × Op2

**Mechanism**: Op6 step 1 iterates `closeAllSessions()`, which calls `sm.GetAllSessions()` (takes read lock, copies the session slice, releases lock) then sends CLR\_REQ to each. Between the lock release and completion of the iteration, XOT handler goroutines may call `sm.AllocateTunLCI()` and add new sessions.

These newly-added sessions are not in the copied slice; they receive no CLR\_REQ. After Op6 step 2 writes `TunHeaderDisconnect`, the kernel kills all AF\_X25 sockets — but the gateway's session manager still contains the new entries, and the associated XOT TCP connections are never explicitly notified.

A second instance of the same race: if the signal handler and `handleTunRead`'s disconnect path both call `closeAllSessions()` concurrently (a consequence of SOCK004 being fixed), sessions could receive duplicate CLR\_REQs over both TUN and XOT TCP. This is harmless per the kernel's state machine but produces unnecessary traffic.

**Classification**: User-mode code race. SESS005 documents the concurrent-close variant.

**Workaround**: Set a shutdown flag (atomic boolean) before entering Op6 step 1. XOT handler goroutines check this flag before allocating new sessions and refuse new calls when it is set. Additionally, close the XOT listening socket before calling `closeAllSessions()` to prevent new XOT connections from being accepted during the shutdown window.

**Status**: Resolved — the signal handler atomically sets `shuttingDown = 1` and closes the XOT listener before calling `closeAllSessions()`. `handleServerConn` checks `shuttingDown` before processing new packets. `closeAllSessions` uses `RemoveAllSessions()` which atomically removes all sessions under the mutex, preventing new additions racing with the snapshot (see SESS005).

---

### COMPAT010 — Closing the TUN fd also triggers `x25_kill_by_neigh` via NETDEV\_DOWN

**Severity**: Low (informs SOCK006 severity; relevant to Op6 ordering)

**Affects**: Op6

**Mechanism**: `af_x25.c:220–226`:

```c
case NETDEV_DOWN:
    nb = x25_get_neigh(dev);
    if (nb) {
        x25_link_terminated(nb);  /* → x25_kill_by_neigh */
        x25_neigh_put(nb);
    }
    x25_route_device_down(dev);
    break;
```

When the TUN fd is closed in Op6 step 3, the kernel fires `NETDEV_DOWN` synchronously during the `close()` call's execution path. `x25_link_terminated()` is called, which calls `x25_kill_by_neigh()`, disconnecting all remaining AF\_X25 sockets with `ENETUNREACH`. This is the same cleanup that writing `TunHeaderDisconnect` in Op6 step 2 achieves.

The practical difference between the two paths:

| | Explicit TunHeaderDisconnect (step 2) | Implicit NETDEV\_DOWN (step 3) |
|:--|:------|:------|
| Timing | Before fd close; gateway controls when cleanup happens | During fd close; interleaved with process teardown |
| Gateway awareness | Gateway can log and sequence cleanup | Cleanup is invisible to gateway code |
| Session manager state | Gateway can act on ENETUNREACH returns before calling `close()` | Sessions may not be cleared before `close()` returns if gateway is exiting |

SOCK006 recommends writing `TunHeaderDisconnect` before `close()`. The NETDEV\_DOWN path means the fd close alone is not unsafe — sockets are cleaned up — but the explicit write provides a deterministic point at which the gateway can complete session teardown before handing off to the process exit path.

**Classification**: Kernel behavior that partially mitigates SOCK006. SOCK006 remains a good practice recommendation.

**Status**: Informational — SOCK006 fix (explicit `TunHeaderDisconnect` before exit) provides a deterministic, ordered cleanup point. The `NETDEV_DOWN` path remains as a kernel-enforced safety net.

---

### COMPAT011 — Multiple gateways on the same machine share the X.25 route table

**Severity**: Low (misconfiguration causes routing failures for AF\_X25 sockets)

**Affects**: Op1 step 5 from two different processes, Op2 on both

**Mechanism**: `SIOCADDRT` modifies `x25_route_list` (`x25_route.c`), which is a machine-wide list protected by `x25_route_list_lock`. Two gateway processes running on the same machine (e.g., one on `tun0`, one on `tun1`) each call `SIOCADDRT` during Op1 step 5. If their address prefixes overlap, the route table holds two matching entries. When an AF\_X25 socket calls `connect()`, the routing lookup returns the first matching entry, which may be either gateway's interface. This can silently route outbound CALL\_REQs to the wrong TUN device.

Additionally, Op1 step 5 and Op2 step 4 in separate processes are not sequenced. A `SIOCDELRT` by one process during another process's `connect()` can remove the route after `connect()` has already looked it up but before the CALL\_REQUEST is sent — though this window is very small since `x25_connect()` holds references to the route and neighbor during call setup.

**Classification**: Configuration and deployment issue. Not a kernel bug.

**Workaround**: Assign non-overlapping X.25 address prefixes to different gateway instances. Use `SIOCX25SSUBSCRIP` on each interface to set distinct LCI ranges, preventing cross-contamination if both interfaces happen to be used by the same AF\_X25 application. Document the address partitioning scheme in operational runbooks.

**Status**: Configuration issue — no code change required. Operational deployments should assign non-overlapping X.25 address prefixes to each gateway instance.

---

## Summary Table

| ID | Severity | Operations | Description | Classification |
|:---|:---------|:-----------|:------------|:---------------|
| COMPAT001 | High | All | TUN fd must have exactly one reader goroutine | User-mode architectural constraint |
| COMPAT002 | High | Op1 × Op2 | `connect()` and TUN reader must be separate goroutines or deadlock occurs | User-mode architectural constraint |
| COMPAT003 | High | Op1 × Op2, Op1 × Op4 | Kernel queues all outbound frames until STATE\_3; inbound CALL\_REQs accepted immediately | Expected kernel behavior; user-mode must track STATE\_3 |
| COMPAT004 | Medium | Op1 × Op2/3/4/6 | Duplicate RESTART\_CONF in STATE\_3 kills all sockets and forces STATE\_2 | Kernel behavior correct; user-mode code bug (blind RESTART\_CONF response) |
| COMPAT005 | Medium | Op1 × Op2/3/4 | RESTART\_REQUEST in STATE\_3 kills all sockets; gateway sessions go stale (SESS002) | Kernel behavior correct; user-mode code bug |
| COMPAT006 | High | Op2 parallel | LCI collision: gateway SessionManager and kernel `x25_new_lci()` may allocate same LCI | User-mode code bug; fix via SIOCX25SSUBSCRIP partitioning |
| COMPAT007 | Low | Op3 × Op4 | Simultaneous close: kernel's CLR\_CONF not forwarded to remote; T23 timeout on remote side (SESS001) | User-mode code bug |
| COMPAT008 | Medium | Op5 × Op2 | Link teardown during call setup: session in pre-manager window not cleaned up | User-mode race condition |
| COMPAT009 | Low | Op6 × Op2 | Shutdown races new sessions: sessions added after `closeAllSessions()` snapshot survive | User-mode race condition (SESS005) |
| COMPAT010 | Low | Op6 | `close(tun_fd)` also triggers `x25_kill_by_neigh` via NETDEV\_DOWN; mitigates SOCK006 | Kernel behavior (informs SOCK006 severity) |
| COMPAT011 | Low | Op1 × Op1 (multi-process) | Two gateways on same machine share x25\_route\_list; overlapping prefixes cause misrouting | Configuration issue |
