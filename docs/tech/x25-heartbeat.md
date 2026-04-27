# X.25 Heartbeat Mechanisms

This document surveys mechanisms that can be used to determine whether an X.25 virtual circuit (or the link carrying it) is still alive. Each mechanism is evaluated against four factors:

| Factor | Question |
|--------|----------|
| **Resilient** | Is this a reliable, self-contained method, or is it a side-effect of current code that could silently disappear if the implementation changes? |
| **Accessible** | Can this be used both locally (via the Linux TUN/AF\_X25 interface) and remotely (over the XOT/TCP path)? |
| **Lightweight** | Does the mechanism consume minimal bandwidth, CPU, and memory? |
| **Portable** | Is this likely to work against an alternate XOT implementation such as Cisco IOS or jh-xotd? |

### jh-xotd reference implementation

[jh-xotd](https://github.com/BAN-AI-X25/jh-xotd/) is a C implementation of RFC 1613 (XOT) that bridges a Linux TUN device to remote XOT TCP connections. Each entry below includes a compatibility note covering two deployment roles:

- **jh-xotd as XOT server (remote):** goxot connects to jh-xotd over TCP/1998; jh-xotd relays to its own local TUN device backed by a Linux X.25 kernel stack.
- **jh-xotd as TUN server (local):** jh-xotd runs on the local host, opens the TUN device itself, and forwards to a remote XOT peer. Local AF\_X25 applications (e.g. the stress test) communicate with the Linux kernel, which forwards through jh-xotd.

jh-xotd defines only six X.25 packet type constants: `CALL_REQUEST` (0x0B), `CALL_ACCEPT` (0x0F), `CLEAR_REQUEST` (0x13), `CLEAR_CONFIRMATION` (0x17), `RESTART_REQUEST` (0xFB), and `RESTART_CONFIRMATION` (0xFF). All other packet types — including INTERRUPT (0x23), INTERRUPT_CONFIRMATION (0x27), RESET_REQUEST (0x1B), and RESET_CONFIRMATION (0x1F) — have no specific handler. These unknown-type packets are relayed transparently by LCI lookup in jh-xotd's inbound/outbound threads, reaching the Linux kernel at the far end, which does respond to them correctly.

jh-xotd sets no TCP socket options beyond `SO_REUSEADDR`. Neither `SO_KEEPALIVE`, `TCP_NODELAY`, nor any receive/send timeout is set on the XOT TCP socket.

---

## HEARTBEAT001 — TCP Keepalive on XOT Connections

### How it arises

RFC 1613 encapsulates X.25 packets inside a TCP stream. TCP provides `SO_KEEPALIVE`, a socket option that causes the kernel to send probe segments on an otherwise idle connection. After a configurable idle period (`tcp_keepalive_time`, default 2 hours), the kernel sends up to `tcp_keepalive_probes` probes (default 9) at `tcp_keepalive_intvl` intervals (default 75 s). If no acknowledgement arrives, the kernel tears down the connection and returns `ECONNRESET` or `ETIMEDOUT` to any pending read or write.

### Why it works

The XOT connection is a plain TCP socket. Linux's TCP stack monitors the connection independently of the application. No X.25 packets need to be exchanged; the keepalive probes are pure TCP ACK segments. When the probe fails, `net.Conn.Read` / `ReadXot` returns an error, which goxot's relay goroutines already detect (`io.EOF` or a network error) and convert into `cleanupConn`. Setting `SO_KEEPALIVE` requires no changes to X.25 logic, only a call to `(*net.TCPConn).SetKeepAlive(true)` and optionally `SetKeepAlivePeriod`.

**GoXOT status:** GoXOT currently sets `TCP_NODELAY` via `SetNoDelay` (`xot.go:43–45`) but does not set `SO_KEEPALIVE`. The mechanism is available but unactivated.

**jh-xotd as XOT server (remote):** jh-xotd sets no TCP socket options on the accepted connection; `SO_KEEPALIVE` is absent from its source. However, TCP keepalive is unilateral: setting it on goxot's end of the socket is sufficient. The Linux TCP stack on goxot's host sends the probes; jh-xotd's TCP stack ACKs them at the OS level without any application involvement. The mechanism therefore works normally from goxot's perspective regardless of jh-xotd's configuration.

**jh-xotd as TUN server (local):** Not applicable. The TUN interface is a kernel pseudo-device; TCP keepalive operates on TCP connections only.

### Evaluation

| Factor | Assessment |
|--------|-----------|
| **Resilient** | High. TCP keepalive is a kernel mechanism; it operates regardless of application-level protocol changes. |
| **Accessible** | XOT path only. The local TUN interface is not TCP, so this does not apply to circuits seen from the kernel side. |
| **Lightweight** | Very high. Keepalive probes are pure TCP ACKs with no X.25 payload. Overhead is negligible. |
| **Portable** | High. TCP keepalive is standard. Cisco routers and jh-xotd both honour it on XOT connections without any special support. |

---

## HEARTBEAT002 — RFC 1613 TCP Stream EOF / Error Detection

### How it arises

RFC 1613 §3.2 states: "If the TCP connection is closed, the X.25 virtual circuits mapped over it are implicitly cleared." When the TCP peer closes the connection normally (FIN) or abnormally (RST), `read()` on the socket returns 0 (EOF) or an error. In Go, `ReadXot` returns `io.EOF` or a net error. GoXOT relay goroutines already propagate this to `cleanupConn`, which sends `CLEAR_REQUEST` to any surviving peer and removes all sessions for that connection.

### Why it works

This is the only mechanism that is both completely free (zero polling) and covers the case where the TCP connection is half-closed or the remote process crashes. It is passive and event-driven: the goroutine blocks on `ReadXot`; the OS delivers the error synchronously when the TCP state machine detects the loss. Because the X.25 state is fully coupled to the TCP connection in XOT, detecting TCP death is equivalent to detecting X.25 circuit death for any LCI on that connection.

**GoXOT status:** Already implemented in every relay goroutine (`handleServerConn`, `handleGatewayRead`, etc.).

**jh-xotd as XOT server (remote):** jh-xotd calls `shutdown(xot->sock, SHUT_RDWR)` after sending CLEAR_CONFIRMATION, followed by `close()`. The `SHUT_RDWR` generates a FIN, which GoXOT's `ReadXot` correctly receives as `io.EOF`. A jh-xotd thread crash or the process exiting also closes the socket, which the OS promotes to a FIN or RST. EOF detection is therefore reliable for all normal and crash-close scenarios in jh-xotd. The one gap is a silent network partition (dead TCP connection with no FIN/RST delivered), which requires HEARTBEAT001 to close.

**jh-xotd as TUN server (local):** Not applicable. TUN is a local device.

### Evaluation

| Factor | Assessment |
|--------|-----------|
| **Resilient** | Moderate. Reliable for clean closes and RST. Fails to detect half-open connections (silent network partition) without a complementary active probe (see HEARTBEAT001). |
| **Accessible** | XOT path only. Irrelevant to local TUN circuits. |
| **Lightweight** | Zero overhead when alive; one event per connection failure. |
| **Portable** | High. Any RFC 1613 implementation uses TCP; a closed TCP connection is universally visible. |

---

## HEARTBEAT003 — X.25 INTERRUPT / INTERRUPT_CONFIRMATION (Circuit-Level Probe)

### How it arises

ITU-T X.25 §5.3.4 defines the INTERRUPT procedure. In state p4 (data transfer), a DTE may send a one-byte INTERRUPT packet (type `0x23`) containing a single octet of user data. The remote DTE must immediately respond with an INTERRUPT_CONFIRMATION (type `0x27`). Only one INTERRUPT may be outstanding at a time; the sender must not send another until the INTERRUPT_CONFIRMATION is received.

In the Linux kernel (`x25_in.c:x25_state3_machine`), when an INTERRUPT is received it is queued to `interrupt_in_queue` and `x25_write_internal(sk, X25_INTERRUPT_CONFIRMATION)` is called immediately. The sending side regulates outstanding INTERRUPTs via the `X25_INTERRUPT_FLAG` bit in `x25_sock.flags` (`x25_out.c:149–154`).

### Why it works

INTERRUPT is explicitly out-of-band: it bypasses flow control windows and does not consume sequence numbers. A successful INTERRUPT/INTERRUPT_CONFIRMATION round trip confirms that:
1. The X.25 circuit (LCI) is in state p4 on both ends.
2. Both ends are processing packets (kernel or application layer).
3. The path between them is passing X.25 packets in both directions.

The round trip can be initiated from an AF\_X25 socket using `send()` with `MSG_OOB`, which enqueues to `interrupt_out_queue`. Over XOT the INTERRUPT travels as a normal XOT-framed X.25 packet, so the probe is accessible to any party that can inject packets into the circuit.

**GoXOT status:** GoXOT does not define constants or generate INTERRUPT/INTERRUPT_CONFIRMATION packets. As a transparent relay, it passes them through unchanged. Generating probes would require adding `PktTypeInterrupt = 0x23` / `PktTypeInterruptConfirm = 0x27` to `x25.go` and injecting them from a monitoring goroutine.

**jh-xotd as XOT server (remote):** jh-xotd has no `INTERRUPT` or `INTERRUPT_CONFIRMATION` constant or handler. A packet with type `0x23` arrives in jh-xotd's inbound thread, which routes it by LCI to the correct circuit entry and writes it to the TUN device. The Linux kernel at jh-xotd's local end receives the INTERRUPT in `x25_state3_machine`, queues it, and immediately sends INTERRUPT_CONFIRMATION (`0x27`) back to the TUN device. jh-xotd's outbound thread reads this from TUN and forwards it back over TCP to goxot. The round trip therefore succeeds end-to-end through jh-xotd. jh-xotd is transparent; it does not terminate the probe itself.

**jh-xotd as TUN server (local):** The Linux kernel's AF\_X25 stack sits above jh-xotd's TUN device. An `MSG_OOB` send on a local AF\_X25 socket causes the kernel to emit an INTERRUPT packet to the TUN device. jh-xotd's outbound thread reads it, routes it by LCI, and forwards it to the remote XOT peer over TCP. The remote kernel responds with INTERRUPT_CONFIRMATION, which the remote XOT peer forwards back. jh-xotd's inbound thread receives it, remaps the LCI, and writes it to TUN. The local kernel delivers it to the AF\_X25 socket as out-of-band data. The complete probe traverses the full path.

**AF\_X25 socket API:** From a C client (e.g. the stress test):
```c
/* Send INTERRUPT — one byte of data, MSG_OOB flag */
unsigned char probe_byte = 0x01;
send(sock, &probe_byte, 1, MSG_OOB);

/* Receive INTERRUPT_CONFIRMATION */
unsigned char confirm_byte;
recv(sock, &confirm_byte, 1, MSG_OOB);
```
`SIGURG` is delivered to the socket owner when an INTERRUPT arrives. The kernel enforces the single-outstanding rule via `X25_INTERRUPT_FLAG`, so only one probe can be in flight per circuit at a time.

### Evaluation

| Factor | Assessment |
|--------|-----------|
| **Resilient** | High. INTERRUPT is a first-class ITU X.25 procedure; its semantics are independent of data flow and unaffected by window size or credit. |
| **Accessible** | High. Works over both the local AF\_X25/TUN path (via `MSG_OOB` on an AF\_X25 socket) and over XOT (relayed as any other packet type). |
| **Lightweight** | Very high. The packet is 4 bytes (3-byte header + 1 byte data) over XOT. The kernel processes it in a single lock cycle. |
| **Portable** | High. Cisco IOS supports X.25 INTERRUPT fully; it is a mandatory ITU facility for SVCs. jh-xotd relays it transparently. |

---

## HEARTBEAT004 — X.25 RESET_REQUEST / RESET_CONFIRMATION (Circuit-Level Reset Probe)

### How it arises

ITU-T X.25 §5.5 defines the RESET procedure. A RESET_REQUEST (type `0x1B`, with cause and diagnostic bytes) resets a single logical channel: sequence counters VR, VS, and VA return to zero, any data in flight is discarded, and the peer must respond with a RESET_CONFIRMATION (type `0x1F`). If the peer does not respond within T22 (default 180 s), the sender issues a CLEAR_REQUEST.

In the Linux kernel (`x25_timer.c:x25_do_timer_expiry`), T22 expiry in `X25_STATE_4` (awaiting reset confirmation) escalates to a CLEAR_REQUEST and then starts T23. A received RESET_CONFIRMATION in state 4 (`x25_state4_machine`) returns the socket to `X25_STATE_3`.

### Why it works

Sending a RESET and receiving RESET_CONFIRMATION proves the circuit is alive and the peer is processing control packets. The round trip is entirely within the X.25 packet layer: no TCP-layer inspection is needed. Unlike INTERRUPT, RESET *does* disrupt the data stream (all in-flight data is lost and sequence numbers are zeroed), making it unsuitable for circuits carrying continuous traffic.

**GoXOT status:** RESET_REQUEST and RESET_CONFIRM are parsed by `x25.go` (constants defined at lines 55–56) but handling in the gateways "often triggers session termination" per `x25_packets.md`. As a heartbeat, the RESET probe would need controlled handling to avoid session teardown.

**jh-xotd as XOT server (remote):** jh-xotd defines no RESET constants and has no RESET handler. A RESET_REQUEST arriving from goxot over TCP is routed by LCI in jh-xotd's inbound thread and written to TUN. The Linux kernel at jh-xotd's local end receives it in `x25_state3_machine`, transitions the socket to `X25_STATE_4`, and sends RESET_CONFIRMATION back to TUN. jh-xotd's outbound thread forwards this to goxot. The probe succeeds end-to-end through jh-xotd, for the same reason as INTERRUPT: jh-xotd is a transparent relay. The response comes from the kernel behind jh-xotd, not from jh-xotd itself.

**jh-xotd as TUN server (local):** Symmetric to the XOT server case: RESET from a local AF\_X25 socket passes through the kernel, through jh-xotd's TUN outbound path, to the remote XOT peer. The remote kernel responds; the RESET_CONFIRMATION travels back through the same path. The AF\_X25 socket re-enters `X25_STATE_3`.

### Evaluation

| Factor | Assessment |
|--------|-----------|
| **Resilient** | Moderate. Defined in ITU X.25; the mechanism is reliable. However, existing GoXOT handling converts RESET to session termination, so this path requires a code change to be usable as a probe. |
| **Accessible** | High. Travels over both TUN and XOT paths. |
| **Lightweight** | Low. Zeroing sequence counters and discarding queued data has application-visible side effects. Unsuitable for circuits carrying active transfers. |
| **Portable** | Moderate. Cisco IOS supports the RESET procedure. jh-xotd relays it transparently, but GoXOT's current session-termination behaviour on receipt is a practical obstacle. |

---

## HEARTBEAT005 — X.25 RESTART_REQUEST / RESTART_CONFIRMATION (Link-Level Probe)

### How it arises

ITU-T X.25 §5.5.3 defines the RESTART procedure. A RESTART_REQUEST (type `0xFB`, LCI=0) resets the entire X.25 packet layer on an interface: all active virtual circuits are cleared simultaneously and the peer must respond with a RESTART_CONFIRMATION (type `0xFF`, LCI=0).

In the Linux kernel, the T20 timer (`x25_neigh.t20timer`, default 180 s) is started when a RESTART_REQUEST is sent. If RESTART_CONFIRMATION does not arrive before T20 fires, `x25_link.c` retransmits the RESTART_REQUEST. An arriving RESTART_CONFIRMATION in link state `X25_LINK_STATE_2` transitions the interface to `X25_LINK_STATE_3` (operational) and flushes queued outbound frames.

GoXOT's `handleTunRead` in `tun-gateway` responds to RESTART_REQUEST from the kernel with RESTART_CONFIRMATION, and it monitors T20 indirectly via link state tracking (COMPAT004/COMPAT005).

### Why it works

RESTART_REQUEST/CONFIRMATION tests whether the remote X.25 packet layer is reachable and operational. A successful exchange guarantees the link is in `X25_LINK_STATE_3`. T20 provides a built-in timeout: the absence of RESTART_CONFIRMATION within 180 s is formally equivalent to declaring the link dead.

Sending RESTART from the goxot side can be done by writing a RESTART_REQUEST frame to the TUN fd (as TunHeaderData with LCI=0). The kernel will respond, confirming the local path. Sending RESTART over XOT tests the remote XOT peer's X.25 packet layer.

**GoXOT status:** GoXOT's `handleTunRead` responds to RESTART_REQUEST from the local kernel and tracks link state to prevent duplicate confirmations (COMPAT004). It does not generate RESTART probes outbound.

**jh-xotd as XOT server (remote):** jh-xotd has a specific handler for RESTART_REQUEST on LCI 0: it generates RESTART_CONFIRMATION immediately. This is the **only** X.25 heartbeat that jh-xotd natively terminates (generates a response itself, rather than forwarding to its local kernel). However, jh-xotd's RESTART handling is partial: it acknowledges the request but does **not** clear its active circuit table. The full ITU X.25 requirement is that all virtual circuits are cleared on RESTART; jh-xotd skips this step. Sending RESTART to jh-xotd will therefore:
  - Return RESTART_CONFIRMATION (link-level liveness confirmed).
  - Leave all active circuits intact in jh-xotd's state (circuit state may desynchronise).

jh-xotd also silently drops RESTART_REQUEST packets received on LCI ≠ 0, so the LCI must be 0 to get any response.

**jh-xotd as TUN server (local):** On startup, the Linux kernel sends RESTART_REQUEST to jh-xotd via TUN; jh-xotd responds with RESTART_CONFIRMATION, completing the link establishment handshake. The kernel's T20 timer monitors jh-xotd's responsiveness: if jh-xotd hangs or dies, the kernel will retransmit RESTART_REQUEST every 180 s. The continued absence of RESTART_CONFIRMATION eventually drives the kernel link state back to `X25_LINK_STATE_2`, causing subsequent connect() calls from AF\_X25 sockets to block until the link re-establishes.

### Evaluation

| Factor | Assessment |
|--------|-----------|
| **Resilient** | High. RESTART is a first-class ITU mechanism with a built-in retransmit timer (T20) and a well-defined dead-link verdict. |
| **Accessible** | High. Works on both TUN (kernel responds) and XOT (jh-xotd responds directly; Cisco responds). |
| **Lightweight** | Low. ITU X.25 requires all active circuits to be cleared on RESTART. As a probe on an active link it is destructive in practice, even though jh-xotd omits the circuit-clearing step. |
| **Portable** | Moderate. Cisco IOS handles RESTART on XOT connections. jh-xotd responds to RESTART_REQUEST but does not clear circuits, meaning the link-level ACK is received but the circuit-state side-effect is unpredictable. |

---

## HEARTBEAT006 — X.25 RR Supervisory Frame Flow (Passive ACK Monitoring)

### How it arises

In X.25 state p4 (data transfer), every received data packet must be acknowledged with a Receive Ready (RR, type `0x01`) frame containing the current receive sequence number V(R). The Linux kernel manages this with the T2 "ack holdback" timer (default 3 s): on receiving a data packet it sets `X25_COND_ACK_PENDING` and starts T2. When T2 fires in `X25_STATE_3`, `x25_timer.c:x25_do_timer_expiry` calls `x25_enquiry_response()`, which sends an RR (or RNR if the buffer is congested). If the receive window fills before T2 fires, an immediate RR is sent (`x25_in.c:299–302`).

Additionally, the kernel's 5-second "heartbeat" timer (`x25_heartbeat_expiry`, `sk_timer`) runs on every X.25 socket in `X25_STATE_3`. If `X25_COND_OWN_RX_BUSY` is cleared by the heartbeat (because the receive buffer has drained), it sends an unsolicited RR to resume flow (`x25_subr.c:x25_check_rbuf`).

### Why it works

Observing that RR frames continue to arrive from the peer (with incrementing N(R) values) confirms that the peer is alive, receiving data, and processing the X.25 packet layer. This is a passive technique: no probe is injected; liveness is inferred from normal protocol traffic. The monitoring party watches the sequence of N(R) values carried in incoming RR frames. If RR stops arriving while data is still being sent (and the window is not full), the connection has likely failed.

**GoXOT status:** GoXOT relays RR frames transparently (`x25_packets.md`). It does not parse N(R) values or monitor the ACK stream. Adding this monitoring would require inspecting bytes 2–3 of each relayed RR/RNR/REJ frame.

**jh-xotd as XOT server (remote):** jh-xotd defines `RR`, `RNR`, and `REJ` as macros and passes all three through its relay path without modification. RR frames generated by the Linux kernel behind jh-xotd's TUN interface are forwarded over TCP to goxot, and vice versa. The passive monitoring technique works across jh-xotd unimpeded.

**jh-xotd as TUN server (local):** jh-xotd relays RR frames in both directions between TUN and TCP. The AF\_X25 kernel stack generates RR frames on the TUN-side per the T2 timer; jh-xotd forwards them to the remote XOT peer. Monitoring the N(R) sequence in the incoming RR stream is a valid liveness signal across this path.

### Evaluation

| Factor | Assessment |
|--------|-----------|
| **Resilient** | Moderate. Reliable as long as data is flowing. On an idle circuit (no data exchange) no RR frames are generated, so this cannot distinguish an idle-but-alive circuit from a dead one. |
| **Accessible** | High. RR frames traverse both TUN and XOT paths. GoXOT and jh-xotd both relay them. |
| **Lightweight** | Very high. Zero additional bandwidth. RR frames are generated as a natural by-product of data transfer. |
| **Portable** | High. RR is mandatory in every X.25 implementation. Cisco and jh-xotd both generate and relay RR frames identically. |

---

## HEARTBEAT007 — Linux Kernel X.25 Heartbeat Timer (Socket-Level Internal Signal)

### How it arises

The Linux AF\_X25 module installs a per-socket recurring timer called the "heartbeat" (`x25_timer.c:x25_start_heartbeat`), which fires every 5 seconds via `sk->sk_timer`. In socket state `X25_STATE_3` (data transfer), the handler calls `x25_check_rbuf()`. In `X25_STATE_0`, it garbage-collects destroyed or dead listening sockets.

The heartbeat is started by `x25_start_heartbeat()` and runs continuously while the socket exists. It is independent of data flow. Its primary function is buffer management (clearing `X25_COND_OWN_RX_BUSY` when buffer space recovers), but its continued execution is an implicit proof that:
1. The socket struct is live in kernel memory.
2. The socket is still in `X25_STATE_3` (not cleared or disconnected).
3. The kernel timer subsystem is scheduling work for this socket.

### Why it works

This is a local-only mechanism. There is no packet sent to the peer. But from the perspective of a local monitoring agent (e.g., `tun-gateway` or `tun-listener`), the continued absence of `x25_disconnect()` being called (which stops the timer and sets state to `X25_STATE_0`) means the socket is alive. Practically, this can be observed indirectly by:
- Reading `/proc/net/x25` and checking socket state.
- Calling `SIOCX25GFACILITIES` on the AF\_X25 socket and verifying it succeeds (an active socket does not return `ENOTCONN`).

**GoXOT status:** `tun-listener` uses `SIOCX25GFACILITIES` (`x25_socket_handling.md`, SOCK003). This IOCTL succeeds only on connected sockets, making it an indirect probe of the heartbeat-guarded state.

**jh-xotd as XOT server (remote):** Not applicable. The heartbeat timer runs in the local Linux kernel; it has no interaction with jh-xotd.

**jh-xotd as TUN server (local):** The heartbeat timer runs in the Linux kernel above jh-xotd's TUN device. It is unaffected by jh-xotd's operation. Reading `/proc/net/x25` or calling `SIOCX25GFACILITIES` on a local AF\_X25 socket gives socket state regardless of what jh-xotd is doing.

### Evaluation

| Factor | Assessment |
|--------|-----------|
| **Resilient** | High. The heartbeat timer is hardwired into `x25_init_timers()` and cannot be removed without modifying the kernel module. |
| **Accessible** | Local only. The timer is a kernel object; it is not visible over XOT and provides no signal to a remote peer. |
| **Lightweight** | Very high. The timer runs every 5 s with minimal work (a conditional RR or a dead-socket GC). |
| **Portable** | Not portable. Linux-specific kernel mechanism. |

---

## AF\_X25 Client and Stress Test Usage

The stress test (`stress_test/stress_test.c`) uses `socket(AF_X25, SOCK_SEQPACKET, 0)` sockets and performs connect → send(MSG_EOR) → read() with a 1-second `SO_RCVTIMEO`. A freshly written or modified X.25 client built on the same AF\_X25 socket API has direct access to the following heartbeat mechanisms.

### Available mechanisms from AF\_X25 sockets

#### HEARTBEAT003 — INTERRUPT via MSG\_OOB

This is the most practical mechanism for the stress test to adopt. The kernel API is:

```c
/* Send an INTERRUPT probe (1 byte) */
unsigned char probe = 0x01;
ssize_t r = send(sock, &probe, 1, MSG_OOB);

/* Receive the INTERRUPT_CONFIRMATION */
unsigned char confirm;
r = recv(sock, &confirm, 1, MSG_OOB);
```

`MSG_OOB` maps directly to `interrupt_out_queue` in the kernel. The kernel enforces the one-outstanding rule, so only one INTERRUPT can be in flight per circuit at a time. The INTERRUPT_CONFIRMATION is delivered to the same socket as an OOB event; `SIGURG` is raised on the socket owner.

**Three concrete use patterns for the stress test:**

1. **Pre-flight probe before data (replaces or shortens the connect wait):**
   After `connect()` succeeds, send an INTERRUPT before the data payload. If INTERRUPT_CONFIRMATION arrives within a short timeout (e.g. 200 ms), the circuit is proven end-to-end. If it does not arrive, clear the circuit immediately rather than investing in a data send. This is particularly useful when the circuit traverses jh-xotd (which relays to its local kernel) or goxot's relay stack, because it exercises the full forwarding path before committing data.

2. **Failure disambiguation after read() timeout:**
   The stress test currently waits 1 second for an echo response. If `read()` returns 0 (EAGAIN / timeout), it is ambiguous whether the echo was lost or the circuit died. After a timeout, send an INTERRUPT. If INTERRUPT_CONFIRMATION arrives, the circuit is alive and the remote end is slow or lost the data. If INTERRUPT_CONFIRMATION does not arrive within, say, 200 ms, the circuit is presumed dead and the socket should be closed immediately rather than left open.

3. **Concurrent liveness check from a monitoring thread:**
   A separate timer thread can send an INTERRUPT once per second on all open circuits. If any circuit stops responding within 2 consecutive probe intervals (2 s), it is flagged as dead. This decouples liveness detection from data transfer timing, removes the dependency on the echo server's round-trip time, and allows detection of one-way path failures (data goes through but confirmations do not).

#### HEARTBEAT006 — RR flow (implicit, no code change needed)

When the stress test sends data with `send(sock, buf, len, MSG_EOR)` and the remote side echoes it, the kernel generates RR frames automatically as it processes incoming data (T2 holdback timer, 3 s). The stress test already observes this indirectly: a successful `read()` of the echo implies that the circuit was alive long enough to carry both directions. No code change is needed to use this; it is already captured by whether `read()` returns data.

#### HEARTBEAT007 — SIOCX25GFACILITIES (socket health check)

Before calling `connect()`, or at any time on an open socket, the IOCTL can verify that the kernel socket is in a healthy state:

```c
struct x25_facilities fac;
if (ioctl(sock, SIOCX25GFACILITIES, &fac) == 0) {
    /* Socket is connected and in X25_STATE_3 */
}
```

This returns `ENOTCONN` if the socket has been disconnected by the kernel (e.g. due to RESTART, T23 expiry, or network error) even if the application has not yet observed the disconnection via `read()`. Useful as a pre-send check to avoid writing to a kernel-disconnected socket.

### Connect-phase timeout (currently unbounded)

The stress test calls `connect()` at line 232 with no explicit application-level timeout. The kernel's T21 timer (default 200 s in `x25.h: X25_DEFAULT_T21`) governs this. `SO_RCVTIMEO` set before connect does **not** affect the `connect()` call on AF\_X25 sockets.

To impose a shorter timeout, the socket must be put into non-blocking mode first:

```c
int flags = fcntl(sock, F_GETFL, 0);
fcntl(sock, F_SETFL, flags | O_NONBLOCK);

int r = connect(sock, (struct sockaddr *)&raddr, sizeof(raddr));
/* Returns EINPROGRESS immediately */

struct pollfd pfd = { .fd = sock, .events = POLLOUT };
r = poll(&pfd, 1, connect_timeout_ms);
if (r == 0) {
    /* Timed out — close and retry */
}
/* Restore blocking mode */
fcntl(sock, F_SETFL, flags);
```

Combined with the INTERRUPT pre-flight probe (above), this allows a two-phase call setup: connect with an explicit timeout, then verify end-to-end connectivity with INTERRUPT before committing to data transfer.

### Inaccessible mechanisms from AF\_X25 sockets

- **TCP Keepalive (HEARTBEAT001) / EOF (HEARTBEAT002):** These operate on the XOT TCP connection inside goxot's relay layer. An AF\_X25 socket application has no visibility into the underlying TCP connections; these are managed entirely by goxot and the kernel.
- **RESTART (HEARTBEAT005):** RESTART is a link-level operation on LCI 0, which is reserved for the kernel's use. An AF\_X25 application cannot send RESTART; it is generated only by the kernel or the TUN gateway.
- **RESET (HEARTBEAT004):** The Linux kernel does not expose a way for AF\_X25 applications to send RESET_REQUEST directly via the socket API; RESET is generated internally in response to detected errors. The `tun_close.c` utility in the stress test directory demonstrates injecting RESET via the TUN device directly, but that requires access to the TUN fd (not available to unprivileged AF\_X25 clients).

---

## Summary

| ID | Name | Granularity | Resilient | Accessible | Lightweight | Portable | jh-xotd remote | jh-xotd local |
|----|------|-------------|-----------|------------|-------------|----------|-----------------|----------------|
| HEARTBEAT001 | TCP Keepalive | XOT link | High | XOT only | Very High | High | Transparent (unilateral) | N/A |
| HEARTBEAT002 | TCP EOF Detection | XOT link | Moderate | XOT only | Zero | High | Works (shutdown→FIN) | N/A |
| HEARTBEAT003 | X.25 INTERRUPT | Per circuit | High | Both | Very High | High | Relayed; kernel responds | Relayed both ways |
| HEARTBEAT004 | X.25 RESET | Per circuit | Moderate | Both | Low | Moderate | Relayed; kernel responds | Relayed both ways |
| HEARTBEAT005 | X.25 RESTART | Entire link | High | Both | Low | Moderate | jh-xotd responds directly (no circuit clear) | Kernel T20 monitors |
| HEARTBEAT006 | RR ACK Flow | Per circuit | Moderate | Both | Zero | High | Relayed transparently | Relayed transparently |
| HEARTBEAT007 | Kernel Heartbeat Timer | Per socket | High | Local only | Very High | None | N/A | N/A (kernel-internal) |

**Recommended combination for production use:**

- HEARTBEAT001 (TCP Keepalive) — catches dead XOT TCP connections within minutes rather than hours with appropriate sysctl tuning. Set on goxot's side; works against jh-xotd without any configuration change on jh-xotd.
- HEARTBEAT003 (INTERRUPT) — non-disruptive per-circuit probe usable from either end of an XOT path, including against Cisco peers and through jh-xotd. Directly accessible from AF\_X25 client sockets via `MSG_OOB`.
- HEARTBEAT002 (EOF Detection) — already implemented; ensure relay goroutines propagate errors promptly.

**Recommended additions to the stress test:**

- Add an INTERRUPT pre-flight probe after `connect()` to verify end-to-end circuit health before sending test data.
- Add non-blocking `connect()` with `poll()` to bound the call-setup wait to a configurable timeout instead of relying on the kernel's T21 (200 s).
- After a `read()` timeout, send an INTERRUPT to disambiguate dead circuit from slow echo, then close immediately if no confirmation arrives within 200 ms.

Avoid HEARTBEAT004 and HEARTBEAT005 on circuits carrying live traffic; both destroy in-flight data and reset sequence state.

## References

- ITU-T Recommendation X.25 (10/96) — §5.3.4 (INTERRUPT), §5.5 (RESET), §5.5.3 (RESTART)
- Linux kernel `net/x25/x25_timer.c` — heartbeat, T2, T21, T22, T23 implementation
- Linux kernel `net/x25/x25_out.c` — `x25_enquiry_response()`, INTERRUPT transmit logic
- Linux kernel `net/x25/x25_in.c` — state 3 machine, INTERRUPT_CONFIRMATION auto-response
- Linux kernel `include/net/x25.h` — timer defaults, socket state constants
- RFC 1613 — X.25 over TCP encapsulation, virtual circuit lifecycle
- [jh-xotd](https://github.com/BAN-AI-X25/jh-xotd/) — single-file C XOT daemon; RESTART handled at LCI 0; all other non-call packet types relayed transparently
- `docs/tech/linux_x25_and_tun.md` — TUN/AF\_X25 interface, kernel link state machine
- `docs/tech/x25_states.md` — p1–p5 state definitions
- `docs/tech/x25_operation_compatibility.md` — COMPAT004/COMPAT005 RESTART handling
- `stress_test/stress_test.c` — AF\_X25 client implementation; connect/send/read pattern; `MSG_OOB` API available
