# Stress Test Analysis

Analysis of `stress_test/stress_test.c` and `stress_test/tun_close.c` against `docs/tech/linux_x25_and_tun.md` and the kernel source in `x25-6.12.74+deb13+1-amd64/`. Issues are identified with `STRESSxxx` identifiers.

---

## STRESS001 — `write()` on AF_X25 sockets fails; `MSG_EOR` is mandatory

**Severity**: Critical (data transfer is completely non-functional)

**Location**: `stress_test.c:238` (sender), `stress_test.c:308` (receiver echo)

**Description**: The `x25_sendmsg()` kernel implementation (`af_x25.c:1130`) explicitly rejects sends that do not set `MSG_EOR` or `MSG_OOB`:

```c
/* we currently don't support segmented records at the user interface */
if (!(msg->msg_flags & (MSG_EOR|MSG_OOB)))
    goto out;   /* returns -EINVAL */
```

The `write(2)` syscall invokes `sendmsg()` with `msg_flags = 0`, so neither `MSG_EOR` nor `MSG_OOB` is set. Every call to `write()` on an AF_X25 socket returns `-EINVAL`. This affects both the sender:

```c
ssize_t sent = write(sock, send_buf, data_len + 1);   // always -EINVAL
```

and the echo server:

```c
ssize_t sent = write(client_sock, buf, n);             // always -EINVAL
```

**Consequence**: The test establishes X.25 connections correctly (socket, bind, SFACILITIES, connect, accept all work), but no data is ever transmitted. Every iteration increments `write_error`. The `short_receive` and `data_mismatches` counters record failures caused by this root error. The test exercises connection setup/teardown throughput only, not data throughput.

**Reference**: `af_x25.c:1126–1131`, `stress_test.c:238`, `stress_test.c:308`

**Fix**: Replace `write()` with `send()` using `MSG_EOR`:
```c
/* sender */
ssize_t sent = send(sock, send_buf, data_len + 1, MSG_EOR);

/* receiver echo */
ssize_t sent = send(client_sock, buf, n, MSG_EOR);
```

**Status**: Resolved — `write()` replaced with `send(..., MSG_EOR)` for both the sender and the echo server.

---

## STRESS002 — Leading byte misidentified as an X.25 control byte; `X25_QBITINCL` not enabled

**Severity**: High (incorrect byte accounting and misleading comments; data check skips byte 0)

**Location**: `stress_test.c:235–236`, `stress_test.c:260–268`, `stress_test.c:298–302`

**Description**: The sender prepends `send_buf[0] = 0x00` and labels it "X.25 control byte: Data":

```c
send_buf[0] = 0x00; // X.25 control byte: Data
fill_buffer(send_buf + 1, data_len, thread_id, call_id);
ssize_t sent = write(sock, send_buf, data_len + 1);
```

This pattern is only meaningful when the `X25_QBITINCL` socket option is enabled (`SOL_X25 / X25_QBITINCL`). With `X25_QBITINCL` set, `x25_sendmsg()` strips the first byte and uses it as the Q-bit value; `x25_recvmsg()` prepends a Q-bit byte to every received record. Without it (the default), the kernel treats all bytes as plain user data — the `0x00` is transmitted verbatim as the first byte of the X.25 Data payload.

The stress test never calls `setsockopt(sock, SOL_X25, X25_QBITINCL, &one, sizeof(one))`, so:
- The `0x00` byte is sent as user data, not consumed as a control byte.
- The receiver's `user_data_len = n - 1` undercounts by one byte in every received record.
- The sender's data-comparison loop starts at `i = 1` (`send_buf[i] != recv_buf[i]`), so a mismatch at byte 0 would go undetected.

**Reference**: `af_x25.c:1212–1219` (Q-bit strip on send), `af_x25.c:1346–1354` (Q-bit prepend on recv), `stress_test.c:235–236`, `stress_test.c:261`

**Fix**: Either enable `X25_QBITINCL` explicitly:
```c
int one = 1;
setsockopt(sock, SOL_X25, X25_QBITINCL, &one, sizeof(one));
```
or remove the leading control byte and treat all buffer bytes as user data, starting the mismatch check at `i = 0`.

**Status**: Resolved — `X25_QBITINCL` enabled on both sender and accepted client sockets via `setsockopt`; comments updated from "X.25 control byte" to "Q-bit byte"; byte accounting is now correct.

---

## STRESS003 — Read accumulation loop treats a record-oriented socket as a byte stream

**Severity**: Medium (logic error; harmless when STRESS001 is fixed and data fits in one packet)

**Location**: `stress_test.c:243–248`

**Description**:

```c
int received = 0;
while (received < sent) {
    ssize_t n = read(sock, recv_buf + received, sent - received);
    if (n <= 0) break;
    received += n;
}
```

`AF_X25 SOCK_SEQPACKET` is a record-oriented socket. Each `read()` delivers one complete reassembled message (the kernel performs M-bit fragment reassembly before delivering to user space). When the echo arrives as a single record, the first `read()` returns all `sent` bytes at once; the loop exits immediately and works correctly.

The problem arises if the echo server (after STRESS001 is fixed) returns the data as multiple separate packets — for example, if packet-size negotiation results in fragmentation and the server echoes each fragment individually rather than as one contiguous record. In that case, each `read()` returns one fragment, but `recv_buf + received` advances the pointer and `sent - received` shrinks the requested size. The final `read()` requesting the last fragment could be given a buffer window smaller than the fragment, causing `MSG_TRUNC` and a silent short return.

For the typical case (single echo record, buffer >= data), the loop works. For the general case it is semantically incorrect: the loop should call `recvmsg()` and check for `MSG_EOR` to detect record boundaries, or use a fixed-size single `read()`.

**Reference**: `af_x25.c:1287–1385` (`x25_recvmsg`), `x25_in.c:32–80` (M-bit reassembly), `stress_test.c:243–248`

**Fix**: Receive each record with a single `read()` into the full buffer:
```c
ssize_t received = read(sock, recv_buf, cfg.buffer_size + 1);
```
If multi-packet echoes are expected, use `recvmsg()` and loop on `MSG_EOR`.

**Status**: Resolved — read accumulation loop replaced with a single `read()` call into the full buffer.

---

## STRESS004 — Receiver threads block indefinitely; no read timeout on accepted sockets

**Severity**: Medium (resource leak under the STRESS001 failure mode; latent under correct operation)

**Location**: `stress_test.c:295–315` (`handle_client`)

**Description**: The `handle_client` goroutine blocks on `read(client_sock, ...)` with no timeout:

```c
while (1) {
    ssize_t n = read(client_sock, buf, cfg.buffer_size + 1);
    if (n <= 0) break;
    ...
}
```

The sender sets `SO_RCVTIMEO = 5 s` only on its own socket (`stress_test.c:180`). Accepted client sockets inherit no timeout from the listening socket. When the sender times out (STRESS001 prevents data from flowing), it calls `close(sock)`, which causes the kernel to send a `CLEAR_REQUEST`. The kernel delivers `EOF` (`n = 0`) to the receiver's `read()`, breaking the loop.

This means the thread *does* eventually exit once the sender closes. However:
1. With `pthread_detach(tid)` (line 388), threads are never joined; a backlog of slow-draining threads accumulates across the test duration.
2. If the sender never closes cleanly (e.g., a crash), the receiver thread blocks indefinitely and the client socket fd leaks.
3. Under a corrected STRESS001, a slow or unresponsive sender could hold the thread and fd open for the lifetime of the test.

**Reference**: `stress_test.c:295–315`, `stress_test.c:388`

**Fix**: Set `SO_RCVTIMEO` on accepted client sockets in `handle_client`:
```c
struct timeval tv = { .tv_sec = 30, .tv_usec = 0 };
setsockopt(client_sock, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
```

**Status**: Resolved — `SO_RCVTIMEO` set to 30 s on accepted client sockets at the start of `handle_client`.

---

## STRESS005 — `rand()` is not thread-safe; data race on global PRNG state

**Severity**: Low (benign statistical skew; no crash or incorrect protocol behaviour)

**Location**: `stress_test.c:211`

**Description**:

```c
long target_val = start_addr_val + (rand() % range);
```

`rand(3)` uses a global state variable that is not protected by a mutex. Concurrent calls from multiple `sender_thread` instances create a data race, which is undefined behaviour per C11. In practice this produces a slightly non-uniform address distribution but does not cause crashes or incorrect X.25 behaviour.

**Reference**: `stress_test.c:211`, `man 3 rand`

**Fix**: Use `rand_r()` with a per-thread seed stored in thread-local storage, or `random_r()`:
```c
// Thread-local seed initialised once per thread
unsigned int seed = (unsigned int)(time(NULL) ^ pthread_self());
long target_val = start_addr_val + (rand_r(&seed) % range);
```

**Status**: Resolved — `rand()` replaced with `rand_r()` using a `_Thread_local` per-thread seed initialised from `time(NULL) ^ (unsigned long)pthread_self()` at thread start.

---

## STRESS006 — `calls_made` counter races; total connections may exceed `max_calls`

**Severity**: Low (minor statistical overshoot; no protocol or data-integrity impact)

**Location**: `stress_test.c:159–160`, `stress_test.c:220`

**Description**: The stop condition is checked before incrementing:

```c
if (cfg.max_calls > 0 && atomic_load(&global_stats.calls_made) >= cfg.max_calls) break;
...
atomic_fetch_add(&global_stats.calls_made, 1);   /* incremented later */
if (connect(sock, ...) < 0) { ... }
```

With `N` threads, all `N` can simultaneously read `calls_made < max_calls`, all increment past `max_calls`, and all proceed to `connect()`. The actual number of connections can overshoot by up to `N - 1`. For small thread counts this is acceptable, but it means `max_calls` is not a hard upper bound.

**Reference**: `stress_test.c:159–160`, `stress_test.c:220`

**Fix**: Use a compare-and-swap to atomically claim a call slot:
```c
long slot = atomic_fetch_add(&global_stats.calls_made, 1);
if (cfg.max_calls > 0 && slot >= cfg.max_calls) break;
```

**Status**: Resolved — `calls_made` is now incremented atomically before `connect()`; the returned slot index is the stop condition, making `max_calls` a hard upper bound per-thread.

---

## STRESS007 — tun_close.c cannot attach to a TUN device already held open by tun-gateway

**Severity**: High (primary use case fails silently when tun-gateway is running)

**Location**: `tun_close.c:71–87`

**Description**: `tun_close.c` opens `/dev/net/tun` and calls `TUNSETIFF` to attach to the named interface. For a single-queue TUN device (the default, without `IFF_MULTI_QUEUE`), the kernel allows only one open file descriptor per device at a time. If tun-gateway is running and holds the TUN fd open, `tun_close.c`'s `TUNSETIFF` call returns `EBUSY`:

```c
if (ioctl(fd, TUNSETIFF, (void *)&ifr) < 0) {
    perror("ioctl(TUNSETIFF)");
    close(fd);
    return 1;
}
```

The error is reported but the user may not understand why. The intended use case — clearing a stuck LCI on a live tun-gateway — requires sharing the TUN fd, which is not possible with standard single-queue TUN.

The tool is only usable after tun-gateway has exited (leaving a stuck kernel socket), not while it is running.

**Reference**: `tun_close.c:83–87`, Linux `drivers/net/tun.c` (TUNSETIFF EBUSY logic)

**Recommendation**: Document this limitation clearly in the usage message. To clear a stuck LCI on a running tun-gateway, the correct approach is to send a `CLEAR_REQUEST` via the tun-gateway's own write path (e.g., via the Unix domain socket protocol), not by injecting directly into the TUN fd. Alternatively, build tun-gateway with a management API to inject arbitrary TUN frames.

**Status**: Resolved — `usage()` updated to document the `EBUSY` limitation and recommend using tun-gateway's management socket instead. Dead `local_addr`/`remote_addr` parameters (left over after STRESS008 fix) also removed.

---

## STRESS008 — tun_close.c injects a non-standard address block into CLEAR REQUEST

**Severity**: Low (extra bytes silently ignored by kernel; misleading comment)

**Location**: `tun_close.c:107–125`

**Description**: The code optionally appends an address block to a CLEAR REQUEST:

```c
if (!use_reset && (local_addr || remote_addr)) {
    // Address Facility (Optional in Clear, but allowed)
    ...
}
```

X.25 CLEAR REQUEST packets (`0x13`) do not carry an address block. The format is:
```
GFI | LCI_H | LCI_L | 0x13 | Cause | Diagnostic
```
Optional DTE facilities and called/calling addresses appear only in CALL REQUEST (`0x0B`) and CALL ACCEPTED (`0x0F`). The Linux kernel ignores bytes beyond the cause and diagnostic in `x25_state3_machine`:

```c
case X25_CLEAR_REQUEST:
    if (!pskb_may_pull(skb, X25_STD_MIN_LEN + 2))
        goto out_clear;
    x25_write_internal(sk, X25_CLEAR_CONFIRMATION);
    x25_disconnect(sk, 0, skb->data[3], skb->data[4]);
    break;
```

Only `skb->data[3]` (cause) and `skb->data[4]` (diagnostic) are read; additional bytes are not validated or used. The injection is therefore harmless in practice but the comment ("Address Facility (Optional in Clear, but allowed)") is incorrect per the X.25 specification.

**Reference**: `tun_close.c:107–125`, `x25_in.c:229–235`, ITU-T X.25 §3.5 (Clear Request packet format)

**Fix**: Remove the address-injection block and its misleading comment, or add a note that the bytes are non-standard and ignored by the Linux kernel.

**Status**: Resolved — non-standard address injection block and its misleading comment removed.

---

## STRESS009 — tun_close.c injection silently discarded for sockets in X25_STATE_0 or X25_STATE_2

**Severity**: Low (informational; affects usefulness of the tool)

**Location**: `tun_close.c` (overall), `x25_in.c:419–422`, `x25_in.c:176–200`

**Description**: `x25_process_rx_frame()` returns immediately for sockets in `X25_STATE_0`:

```c
if (x25->state == X25_STATE_0)
    return 0;
```

A CLEAR REQUEST injected for an LCI whose socket is already in state 0 (i.e., already disconnected but not yet freed) is discarded silently. The tool prints a success message even though no state change occurred.

Similarly, `x25_state2_machine` (Awaiting Clear Confirmation) handles a second CLEAR REQUEST by sending CLEAR CONFIRMATION and calling `x25_disconnect()`, which is the desired outcome. However, the T23 timer is already running in state 2 and the socket will be freed anyway — the injection only accelerates the process.

For sockets stuck in `X25_STATE_1` (Awaiting Call Accepted) or `X25_STATE_3` (Data Transfer), the CLEAR REQUEST is fully processed and the socket is freed promptly.

**Reference**: `x25_in.c:419–422`, `x25_in.c:175–200`, `x25_in.c:87–167`

**Recommendation**: Add a diagnostic note in the usage output indicating which kernel states the tool is effective against, and that a "successfully injected" message only confirms the write to the TUN fd, not that the kernel processed it.

**Status**: Resolved — `usage()` updated with a note that "Successfully injected" confirms only the TUN fd write, not kernel processing; the usage message now documents which kernel socket states the injection is effective against.

---

## Summary

| ID | Severity | Component | Description |
|:---|:---|:---|:---|
| STRESS001 | Critical | stress_test.c | `write()` fails on AF_X25; `MSG_EOR` required for all sends |
| STRESS002 | High | stress_test.c | Leading byte treated as "control byte" without `X25_QBITINCL`; statistics off by one |
| STRESS003 | Medium | stress_test.c | Read loop uses byte-stream semantics on a record-oriented socket |
| STRESS004 | Medium | stress_test.c | `handle_client` threads block indefinitely; no `SO_RCVTIMEO` on accepted sockets |
| STRESS005 | Low | stress_test.c | `rand()` is not thread-safe; data race on global PRNG |
| STRESS006 | Low | stress_test.c | `max_calls` not a hard limit; up to `N-1` extra connections per thread race |
| STRESS007 | High | tun_close.c | Cannot attach to TUN fd already held by tun-gateway (EBUSY); primary use case limited |
| STRESS008 | Low | tun_close.c | Address block injected into CLEAR REQUEST is non-standard; silently ignored by kernel |
| STRESS009 | Low | tun_close.c | "Successfully injected" printed even when kernel discards the packet (STATE_0 socket) |
