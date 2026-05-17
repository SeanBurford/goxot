# Linux X.25 Routing

## Local to Local

A local `AF_X25` `listen()` socket **cannot** receive a call from a local `AF_X25` `connect()` in the current kernel without external assistance. The two paths — transmit and receive — meet only at a network device, never inside the kernel.

### Why Local-to-Local Fails

#### The transmit path exits the kernel

Tracing `connect()` through the source:

1. `x25_connect()` (`af_x25.c:784`) calls `x25_get_route(&addr->sx25_addr)`, which does a longest-prefix match against the route table (`x25_route.c:141–148`). The route lookup returns a `struct x25_route` containing a `net_device` pointer. **It does not check whether the destination address is bound to any local socket.**

2. `x25_connect()` at line 795 calls `x25_new_lci(x25->neighbour)`, which scans the global socket list for an unused Logical Channel Identifier on that neighbor device (`af_x25.c:335–350`).

3. At line 814, `x25_connect()` calls `x25_write_internal(sk, X25_CALL_REQUEST)`.

4. `x25_write_internal()` (`x25_subr.c:255`) calls `x25_transmit_link(skb, x25->neighbour)`.

5. `x25_transmit_link()` (`x25_link.c:212–228`): if the link is in `X25_LINK_STATE_3` (operational), it calls `x25_send_frame(skb, nb)` immediately; otherwise it queues the frame until the link comes up.

6. `x25_send_frame()` (`x25_dev.c:173–194`) prepends the `X25_IFACE_DATA` PI byte and calls **`dev_queue_xmit(skb)`**. The frame is now in the device layer's transmit queue, heading for the driver. It will not return to the X.25 receive path.

#### The receive path only enters from a device

The sole entry point for incoming X.25 frames is `x25_lapb_receive_frame()` (`x25_dev.c:94`), registered as the `ETH_P_X25` packet type handler. It is called only when the device layer delivers a frame via `netif_receive_skb()`. There is no code anywhere in `net/x25/` that feeds a locally generated frame back into this path.

`x25_lapb_receive_frame()` strips the PI byte and calls `x25_receive_data()` (`x25_dev.c:26`), which for a `X25_CALL_REQUEST` frame with an unknown LCI calls `x25_rx_call_request(skb, nb, lci)` (`x25_dev.c:68`). Only there does `x25_find_listener()` search `x25_list` for a listening socket whose bound `source_addr` matches the called address in the CALL_REQUEST frame.

The gap is absolute: the CALL_REQUEST exits via `dev_queue_xmit` and `x25_lapb_receive_frame` is never invoked for it unless a device driver (or userspace via a TUN fd) feeds the frame back in.

### Why a Single TUN Device Does Not Bridge the Gap

It might seem that a TUN device could act as a loopback: userspace reads the outgoing CALL_REQUEST from the TUN fd and writes it back. This does not work because of an LCI collision.

When `x25_connect()` calls `x25_new_lci(nb)`, it allocates, say, LCI=1 on neighbor `nb` (the TUN device's neighbor). The caller's socket is now registered as `(LCI=1, nb)` in the global socket list.

When the CALL_REQUEST frame returns to the kernel via the same TUN device, `x25_receive_data()` decodes LCI=1 from the frame header and calls `x25_find_socket(1, nb)`. It **finds the caller's socket** (which is in `X25_STATE_1`, Awaiting Call Accepted). The frame is dispatched to `x25_process_rx_frame()` → `x25_state1_machine()`, which does not handle `X25_CALL_REQUEST` frames. The incoming call is dropped before `x25_rx_call_request()` is ever reached. The listener never sees it.

### What Works Today: Two TUN Devices with a Userspace Relay

Local-to-local X.25 is achievable without kernel changes by using **two** `ARPHRD_X25` TUN devices and a userspace relay that copies frames between their file descriptors.

Setup:
- Device `tunA`: the caller's side. Route: address-prefix-for-caller → `tunA`.
- Device `tunB`: the listener's side. Route: address-prefix-for-listener → `tunB`.
- Userspace relay: reads from `tunA`'s fd, writes to `tunB`'s fd, and vice versa.

Call flow:
1. Caller (bound to "1234") calls `connect({AF_X25, "5678"})`. CALL_REQUEST exits via `tunA`. The caller socket holds `(LCI=1, nb_A)`.
2. Relay reads the CALL_REQUEST from `tunA` and writes it to `tunB`.
3. `x25_lapb_receive_frame()` fires for `tunB`. LCI=1 is unknown on `nb_B`, so `x25_rx_call_request(skb, nb_B, 1)` is called.
4. `x25_find_listener("5678", skb)` finds the listening socket bound to "5678".
5. A new accepted socket is created with `(LCI=1, nb_B)`. `x25_write_internal(make, X25_CALL_ACCEPTED)` sends the reply via `tunB`.
6. Relay reads the CALL_ACCEPTED from `tunB` and writes it to `tunA`.
7. `x25_lapb_receive_frame()` fires for `tunA`. LCI=1 on `nb_A` matches the caller socket. `x25_state1_machine()` processes `X25_CALL_ACCEPTED` and transitions the caller to `X25_STATE_3`.
8. Data transfer proceeds with the relay copying frames in both directions.

Because the two sockets use different neighbors (`nb_A` and `nb_B`), there is no LCI collision. The relay must also handle `X25_IFACE_CONNECT` and `X25_IFACE_DISCONNECT` PI-byte frames in addition to `X25_IFACE_DATA` frames.

### What Kernel Changes Would Be Needed for True In-Kernel Loopback

Supporting local-to-local without any userspace relay requires changes in three areas.

#### 1. Local destination detection in x25_connect()

`x25_connect()` must detect that the destination address is locally bound before issuing a route lookup. After the address validation at line 782 and before the route lookup at line 784, add a search of `x25_list`:

```c
/* Check for local destination before routing */
listener = x25_find_listener_for_addr(&addr->sx25_addr);
if (listener) {
    rc = x25_local_connect(sk, listener, addr);
    sock_put(listener);
    goto out;
}
```

`x25_find_listener_for_addr()` would be a stripped-down variant of the existing `x25_find_listener()` that searches only on address (no CUD matching needed at this stage, or CUD can be passed in).

#### 2. A loopback neighbor

Both sides of a local call must share a `struct x25_neigh` so that `x25_find_socket(lci, nb)` can route replies correctly. A singleton loopback neighbor with `state = X25_LINK_STATE_3` (always up) and `dev = NULL` (or a dedicated dummy device) serves this purpose:

```c
static struct x25_neigh x25_loopback_neigh = {
    .state    = X25_LINK_STATE_3,
    .extended = 0,
    .dev      = NULL,          /* must guard against dev_queue_xmit calls */
    ...
};
```

`x25_send_frame()` silently drops frames when `nb->dev->type` is not `ARPHRD_X25` (`x25_dev.c:185–188`). Setting `dev = NULL` would crash there; the loopback path must bypass `x25_transmit_link()` / `x25_send_frame()` entirely and deliver frames directly.

#### 3. LCI assignment and bidirectional state machine coordination

In real X.25 the two ends of a virtual circuit use the same LCI value. The kernel's `x25_find_socket(lci, nb)` lookup requires `(LCI, neighbour)` to be unique. With both sockets on the loopback neighbor, they must use **different** LCIs:

- Caller socket: LCI=N, allocated by `x25_new_lci(&x25_loopback_neigh)`.
- Accepted socket: LCI=M (M ≠ N), allocated from the same pool.

The mapping `(N ↔ M)` must be stored so that frames written by one socket can be routed to the other. A per-loopback-call structure holding the peer's LCI would suffice.

The `x25_local_connect()` function would then:

1. Allocate LCI=N for the caller socket; set its state to `X25_STATE_1`.
2. Build a synthetic CALL_REQUEST `sk_buff` (same layout as `x25_write_internal` would produce).
3. Call `x25_rx_call_request()` directly, passing the loopback neighbor and LCI=M (a second allocated LCI).
4. `x25_rx_call_request()` creates the accepted socket, wakes the listener's `sk->sk_data_ready`, and (if `X25_ACCPT_APPRV_FLAG` is set) calls `x25_write_internal(make, X25_CALL_ACCEPTED)`. This call must be intercepted: instead of going to `dev_queue_xmit`, the CALL_ACCEPTED frame must be delivered to the caller's state machine via `x25_process_rx_frame()` → `x25_state1_machine()`.
5. Intercepting `x25_write_internal()` output requires either a new code path that skips `x25_transmit_link()` entirely, or hooking at the `x25_transmit_link()` level to detect the loopback neighbor and redirect.

After step 4, the caller socket transitions from `X25_STATE_1` to `X25_STATE_3`, and `x25_wait_for_connection_establishment()` (`af_x25.c:824`) returns. Data-phase frames follow the same per-call LCI-swap logic, which must be implemented in a loopback-aware fast path alongside `x25_transmit_link()`.

#### Summary of changes required

| Area | Change |
|------|--------|
| `af_x25.c` | Add local-destination check before route lookup in `x25_connect()` |
| `af_x25.c` | New `x25_local_connect()` function to synthesize the handshake |
| `net/x25/` | New `x25_loopback.c` (or extension of `x25_link.c`) implementing the loopback neighbor, LCI-pair table, and frame redirect |
| `x25_subr.c` | `x25_write_internal()` or `x25_transmit_link()` must detect the loopback neighbor and call the redirect path instead of `x25_send_frame()` |
| `include/net/x25.h` | Declarations for the loopback neighbor and peer-LCI lookup |

---

## Local to Interface

A local `AF_X25` socket calling `connect()` to an address reachable via a network device follows a straightforward path through the kernel. Every frame crosses the device boundary in both directions.

### Call Setup

`x25_connect()` (`af_x25.c:746`) prepares the outgoing call:

1. **Route lookup** (`af_x25.c:784`): `x25_get_route(&addr->sx25_addr)` performs a longest-prefix match over the route table. It returns the `struct x25_route` whose `address` prefix matches the destination, along with the associated `net_device`.

2. **Neighbor acquisition** (`af_x25.c:789`): `x25_get_neigh(rt->dev)` retrieves the `struct x25_neigh` for that device — the object that tracks link state, the outbound queue, and extended-mode negotiation.

3. **LCI allocation** (`af_x25.c:795`): `x25_new_lci(x25->neighbour)` scans the global socket list for the first LCI value (1–4095) not already assigned to another socket on this neighbor. The caller's socket is registered under `(LCI, neighbour)` from this point on.

4. **State transition** (`af_x25.c:809–812`): The socket moves to `TCP_SYN_SENT` / `X25_STATE_1` (Awaiting Call Accepted), and `x25_write_internal(sk, X25_CALL_REQUEST)` (`af_x25.c:814`) builds and enqueues the frame. `x25_transmit_link()` (`x25_link.c:212`) either sends it immediately if the link is in `X25_LINK_STATE_3`, or queues it until the link comes up. `x25_send_frame()` (`x25_dev.c:173`) prepends the `X25_IFACE_DATA` PI byte and calls `dev_queue_xmit()`.

5. **Wait** (`af_x25.c:824`): Unless `O_NONBLOCK` is set, `x25_wait_for_connection_establishment()` sleeps until the socket reaches `TCP_ESTABLISHED` or an error occurs.

### CALL_ACCEPTED Reception

The remote peer's CALL_ACCEPTED arrives via `x25_lapb_receive_frame()` → `x25_receive_data()`. The LCI in the frame matches the caller's socket, so `x25_find_socket(lci, nb)` (`x25_dev.c:50`) returns it and dispatches the frame to `x25_process_rx_frame()` → `x25_state1_machine()` (`x25_in.c:90`).

`x25_state1_machine()` at case `X25_CALL_ACCEPTED` (`x25_in.c:97`):

- Cancels the T21 timer (`x25_in.c:99`).
- Resets all sequence variables: `vs`, `va`, `vr`, `vl` → 0; `condition` → 0 (`x25_in.c:100–104`).
- Transitions to `X25_STATE_3` / `TCP_ESTABLISHED` (`x25_in.c:105–106`).
- Parses any address block, facilities, and Call User Data from the CALL_ACCEPTED frame (`x25_in.c:110–138`).
- Calls `sk->sk_state_change(sk)` (`x25_in.c:140`) to wake the sleeping `x25_wait_for_connection_establishment()`, which returns 0 and allows `connect()` to complete.

If instead the remote sends a `X25_CLEAR_REQUEST` while the call is in STATE_1 (call rejected), `x25_state1_machine()` at lines 152–158 sends a `X25_CLEAR_CONFIRMATION` and calls `x25_disconnect(sk, ECONNREFUSED, ...)`. `connect()` returns `-ECONNREFUSED`.

### Data Transfer

In STATE_3, `x25_sendmsg()` (`af_x25.c`) calls `x25_output()` (`x25_out.c`) to packetize the userspace buffer into frames no larger than the negotiated `pacsize_out` (stored as a log₂ value; converted by `x25_pacsize_to_bytes()`). Frames are enqueued on `sk->sk_write_queue`.

`x25_kick()` (`x25_out.c`) dequeues up to `winsize_out` frames, stamps each with the current `vs` (send sequence) and `vr` (piggybacked receive acknowledgement), calls `x25_transmit_link()`, and moves the frame to `ack_queue` pending acknowledgement. The window closes when `vs` reaches `(va + winsize_out) % modulus`.

Incoming data frames are handled by `x25_state3_machine()` at case `X25_DATA` (`x25_in.c:262`). The frame's NS value is checked against `vr`; if it matches, the frame is queued via `x25_queue_rx_frame()` and `vr` is incremented. When the receive window fills (or after the T2 holdback timer fires), an RR frame is sent via `x25_enquiry_response()` to advance the remote's window. If the receive buffer exceeds half of `sk_rcvbuf`, the `X25_COND_OWN_RX_BUSY` flag is set and an RNR is sent instead.

RR and RNR frames from the peer are handled at `x25_in.c:240–259`: a valid NR advances `va` via `x25_frames_acked()` and clears frames from `ack_queue`; an invalid NR triggers a `RESET_REQUEST` and transition to `X25_STATE_4`.

### Teardown

When the application calls `close()`, `x25_release()` (`af_x25.c:625`) handles the state:

- **STATE_1, 3, or 4** (`af_x25.c:645–657`): Clears the send queue, sends `X25_CLEAR_REQUEST` via `x25_write_internal()`, starts the T23 timer, and moves to `X25_STATE_2` (Awaiting Clear Confirmation). The socket is marked `SOCK_DEAD` and `SOCK_DESTROY` so the heartbeat timer can reap it after the handshake completes.

- **STATE_2** (`af_x25.c:639–643`): Already clearing; `x25_disconnect()` is called immediately and the socket is destroyed.

The peer's `x25_state3_machine()` receives the `CLEAR_REQUEST` and responds with `X25_CLEAR_CONFIRMATION` + `x25_disconnect()` (`x25_in.c:232–238`). When the local side receives the `CLEAR_CONFIRMATION`, `x25_state2_machine()` (`x25_in.c:190–191`) calls `x25_disconnect(sk, 0, 0, 0)`, which clears all queues, stops timers, zeroes the LCI, sets the socket to STATE_0 / `TCP_CLOSE`, and wakes any blocked waiters (`x25_subr.c:339–367`).

### Device Failure

If the interface goes down while a call is active, `x25_device_event()` (`af_x25.c:196`) receives `NETDEV_DOWN`, calls `x25_link_terminated(nb)`, which calls `x25_kill_by_neigh(nb)` (`af_x25.c:1771`). Every socket whose `neighbour` matches `nb` is disconnected with `x25_disconnect(s, ENETUNREACH, 0, 0)` (`af_x25.c:1781`). The blocked `connect()` or any sleeping `recvmsg()` wakes with `-ENETUNREACH`.

---

## Interface to Local

An incoming call from a remote peer reaches a local listening socket through the packet handler registered at module init.

### Frame Entry and Demultiplexing

`x25_lapb_receive_frame()` (`x25_dev.c:94`) is registered as the `ETH_P_X25` packet type handler (`af_x25.c:1762–1764`). When the device layer delivers a frame, `x25_lapb_receive_frame()` checks that the device belongs to `init_net` (`x25_dev.c:100`), copies the skb, looks up the `struct x25_neigh` for the device, and dispatches on the PI byte. For `X25_IFACE_DATA`, the PI byte is stripped and `x25_receive_data(skb, nb)` is called (`x25_dev.c:126–130`).

`x25_receive_data()` (`x25_dev.c:26`) decodes the LCI from bytes 0–1 of the X.25 header and the frame type from byte 2. If LCI is zero the frame is a link-layer control frame and goes to `x25_link_control()`. Otherwise `x25_find_socket(lci, nb)` searches the global socket list for a connected socket with that (LCI, neighbour) pair. For a fresh incoming call, no such socket exists.

For a `X25_CALL_REQUEST` frame with an unknown LCI, `x25_rx_call_request(skb, nb, lci)` is called (`x25_dev.c:68–69`).

### Listener Matching

`x25_rx_call_request()` (`af_x25.c:940`) strips the 3-byte header, then calls `x25_parse_address_block()` to extract the called and calling X.121 addresses. The called address (which will become the accepted socket's local `source_addr`) is passed to `x25_find_listener()` (`af_x25.c:997`).

`x25_find_listener()` (`af_x25.c:264`) iterates `x25_list` under `x25_list_lock` and checks each socket for:

1. `sk_state == TCP_LISTEN`, and
2. The socket's bound `source_addr` matches the called address from the frame, **or** the socket is bound to `null_x25_address` (wildcard, accepts all destinations).

If a matching listener has `cudmatchlength > 0`, the first `cudmatchlength` bytes of Call User Data in the frame must match the socket's stored CUD prefix exactly — a direct CUD match takes priority over a plain address match (`af_x25.c:283–292`). A listener with `cudmatchlength == 0` serves as the fallback for any call addressed to its X.121 address.

If no listener is found, the call is either forwarded (see Interface to Interface below) or cleared with a `CLEAR_REQUEST`.

If the listener's accept queue is full (`sk_acceptq_is_full`), the call is also cleared (`af_x25.c:1000–1001`).

### Accepted Socket Creation

`x25_make_new(sk)` (`af_x25.c:1040`) allocates a child socket inheriting the listener's options. The child is populated (`af_x25.c:1049–1065`):

- `makex25->lci = lci` — the LCI from the incoming frame becomes the accepted socket's LCI.
- `makex25->source_addr = source_addr` — the called address (local address for the new connection).
- `makex25->dest_addr = dest_addr` — the calling address (remote peer's address).
- `makex25->neighbour = nb` — the interface the call arrived on.
- `makex25->facilities` — the result of `x25_negotiate_facilities()` (`af_x25.c:1026`), which reconciles what the remote requested with what the listener's socket options permit.

### Automatic vs. Manual Accept

If `X25_ACCPT_APPRV_FLAG` is **not** set on the listening socket (the default), the call is accepted immediately (`af_x25.c:1068–1070`):

```c
x25_write_internal(make, X25_CALL_ACCEPTED);
makex25->state = X25_STATE_3;
```

The CALL_ACCEPTED frame exits via `x25_transmit_link()` → `x25_send_frame()` → `dev_queue_xmit()` back to the peer.

If `X25_ACCPT_APPRV_FLAG` **is** set (enabled via `SIOCX25CALLACCPTAPPRV`), the child socket is placed in `X25_STATE_5` (`af_x25.c:1072`). The application must issue the `SIOCX25CALLACCPTAPPRV` ioctl on the accepted socket descriptor to trigger the CALL_ACCEPTED transmission and transition to STATE_3.

In either case, the child socket is inserted into the global list via `x25_insert_socket(make)` (`af_x25.c:1083`), and the incoming skb is queued on the listening socket's receive queue (`af_x25.c:1085`). `sk->sk_data_ready(sk)` (`af_x25.c:1090`) wakes any thread sleeping in `accept()`, which dequeues the child socket and returns the new file descriptor to userspace.

### Data Transfer and Teardown

Once in STATE_3, incoming data frames are dispatched by `x25_receive_data()` to the accepted socket via `x25_find_socket(lci, nb)` and processed by `x25_state3_machine()` (`x25_in.c:211`). The data path is symmetric with the Local to Interface case above.

When the remote peer initiates a clear, `x25_state3_machine()` at case `X25_CLEAR_REQUEST` (`x25_in.c:232–238`) sends a `X25_CLEAR_CONFIRMATION` frame and calls `x25_disconnect()`, which delivers the close to the application as a zero-length `recvmsg()` return (EOF).

The local application may close the accepted socket at any time, which follows the same `x25_release()` → `X25_CLEAR_REQUEST` → `X25_STATE_2` path described in the Local to Interface section.

### Device Failure

If the interface that the accepted socket arrived on goes down, `NETDEV_DOWN` → `x25_link_terminated(nb)` → `x25_kill_by_neigh(nb)` (`af_x25.c:1771`) disconnects every socket bound to that neighbor with `ENETUNREACH`, exactly as in the Local to Interface case. Any forwarding table entries involving the device are simultaneously purged by `x25_clear_forward_by_dev(nb->dev)` (`af_x25.c:1789`).

---

## Interface to Interface

X.25 packet forwarding allows the kernel to relay calls and data between two network interfaces without involving a local socket. It is disabled by default and gated by a sysctl.

### Enabling Forwarding

Forwarding is controlled by `sysctl_x25_forward` in `net/x25/sysctl_net_x25.c`. It is checked in exactly one place: `x25_rx_call_request()` at `af_x25.c:1010`, after `x25_find_listener()` returns NULL (no local listener for the called address):

```c
if (sysctl_x25_forward &&
        x25_forward_call(&dest_addr, nb, skb, lci) > 0)
```

If forwarding is disabled or `x25_forward_call()` returns ≤ 0, a `CLEAR_REQUEST` is sent and the call is dropped.

### Call Request Forwarding — x25_forward_call()

`x25_forward_call()` (`x25_forward.c:17`) handles the initial CALL_REQUEST:

1. **Route lookup** (`x25_forward.c:27`): `x25_get_route(dest_addr)` finds the outbound device for the called address — the same prefix-match used by `x25_connect()`.

2. **Same-device check** (`x25_forward.c:40–42`): If the outbound device is the same device the CALL_REQUEST arrived on, forwarding is aborted. This prevents a trivial routing loop when there is only one X.25 interface and a default route:
   ```c
   if (rt->dev == from->dev)
       goto out_put_nb;
   ```

3. **Duplicate LCI check** (`x25_forward.c:47–54`): If an entry for the same LCI already exists in the forward table, the CALL_REQUEST is still transmitted (in case of retransmission) but no new table entry is created.

4. **Forward table entry** (`x25_forward.c:57–68`): A `struct x25_forward` is allocated and added to `x25_forward_list`:
   ```c
   new_frwd->lci  = lci;        /* LCI from the arriving frame */
   new_frwd->dev1 = rt->dev;    /* outbound (destination) device */
   new_frwd->dev2 = from->dev;  /* inbound (source) device */
   ```

5. **Transmission** (`x25_forward.c:71–74`): The CALL_REQUEST skb is cloned and sent via `x25_transmit_link(skbn, neigh_new)`, which queues it if the outbound link is not yet in STATE_3 or sends immediately via `x25_send_frame()` → `dev_queue_xmit()`.

The LCI is passed through to the outbound interface **without modification**. There is no LCI translation; both sides of the forwarded call use the same LCI value.

### Data and Control Frame Forwarding — x25_forward_data()

After the CALL_REQUEST, all subsequent frames on that LCI arrive with an unknown socket (there is no local socket holding that LCI) and a non-CALL_REQUEST frame type. `x25_receive_data()` (`x25_dev.c:76`) calls `x25_forward_data(lci, nb, skb)`.

`x25_forward_data()` (`x25_forward.c:89`):

1. **Table lookup** (`x25_forward.c:97–109`): Scans `x25_forward_list` for an entry with the matching LCI. The direction is determined by which device the frame arrived on:
   ```c
   if (from->dev == frwd->dev1)
       peer = frwd->dev2;   /* arrived on outbound side, send to inbound */
   else
       peer = frwd->dev1;   /* arrived on inbound side, send to outbound */
   ```
   The table entry is bidirectional: either device can send and the other receives.

2. **Copy and forward** (`x25_forward.c:114–118`): The skb is deep-copied with `pskb_copy()` (preserving non-linear data) and the copy is sent via `x25_transmit_link(skbn, nb)` → `x25_send_frame()` → `dev_queue_xmit()` on the peer's device.

This applies to DATA frames, RR/RNR supervisory frames, RESET_REQUEST and RESET_CONFIRMATION frames, CLEAR_REQUEST frames, and all other control frames — everything except the initial CALL_REQUEST.

### Forward Table Cleanup

When a `CLEAR_CONFIRMATION` frame is forwarded, `x25_receive_data()` (`x25_dev.c:77–80`) detects it and purges the table entry:

```c
if (x25_forward_data(lci, nb, skb)) {
    if (frametype == X25_CLEAR_CONFIRMATION)
        x25_clear_forward_by_lci(lci);
```

`x25_clear_forward_by_lci()` (`x25_forward.c:127`) removes and frees every entry with the matching LCI under the write lock. Subsequent frames on that LCI will find no forward entry and be silently discarded (or trigger a CLEAR_REQUEST if a new call arrives).

### Device Failure

When either of the two devices in a forwarding pair goes down, `x25_kill_by_neigh(nb)` (`af_x25.c:1771`) calls `x25_clear_forward_by_dev(nb->dev)` (`af_x25.c:1789`). `x25_clear_forward_by_dev()` (`x25_forward.c:143`) removes every forward entry where `dev1` or `dev2` matches the failed device:

```c
if ((fwd->dev1 == dev) || (fwd->dev2 == dev)) {
    list_del(&fwd->node);
    kfree(fwd);
}
```

No CLEAR_REQUEST is sent to the surviving side; the remote peer will eventually time out or detect the loss through LAPB layer mechanisms.
