# Interfacing with Linux X.25 and TUN Interfaces

This document describes how GoXOT interfaces with the Linux kernel's X.25 implementation via the AF_X25 socket family and TUN network devices.

## The Linux X.25 Stack

The Linux kernel provides an AF_X25 socket family that implements the X.25 Packet Layer Protocol (PLP). It can run over various link layers, including LAPB (standard serial) and TUN (virtual encapsulation).

### Socket API
Standard POSIX socket calls are used:
*   **Socket Creation**: `socket(AF_X25, SOCK_SEQPACKET, 0)`. This is the only supported socket type for AF_X25. The protocol argument must be 0.
*   **Addressing**: Uses `struct sockaddr_x25`.
*   **Constraint**: A socket **must** be bound before `connect()` is called. Autobinding is not supported (`af_x25.c:810`).

## X.25 over TUN (ARPHRD_X25)

The `tun-gateway` interfaces with the kernel by creating a TUN device and setting its link type to `ARPHRD_X25` (value 271). This tells the kernel to treat the interface as a native X.25 packet device.

### Encapsulation and Handshake
Packets exchanged with the TUN device include a 4-byte PI header (`[0x00, 0x00, 0x08, 0x05]`) followed by a 1-byte control header. The TUN device must be opened **without** `IFF_NO_PI` so that the 4-byte Protocol Information header is included in every frame.

#### Control Headers
The following headers are defined (source: `net/x25/x25_dev.c`, constants from `include/net/x25device.h`):

| Value | Name | Purpose |
| :--- | :--- | :--- |
| `0x00` | `TunHeaderData` | Standard X.25 PLP packet data follows. |
| `0x01` | `TunHeaderConnect` | Link Layer (L2) connection request/ack. |
| `0x02` | `TunHeaderDisconnect` | Link Layer (L2) disconnection. |
| `0x03` | `TunHeaderParam` | Exchange of link parameters. Not used in practice for ARPHRD_X25. |

#### The Connect Handshake
When the kernel's X.25 stack needs to transmit a frame and the link is down (`X25_LINK_STATE_0`), it sends a `TunHeaderConnect (0x01)` frame with an empty payload (`x25_dev.c:x25_establish_link`). The gateway **must** respond with an identical `TunHeaderConnect (0x01)` frame. On receiving the echo, the kernel calls `x25_link_established()`, transitions the link to `X25_LINK_STATE_2`, and immediately sends a `RESTART_REQUEST` packet (LCI=0, type `0xFB`) as a `TunHeaderData` frame. The gateway must respond to the `RESTART_REQUEST` with a `RESTART_CONFIRMATION` (LCI=0, type `0xFF`). Only then does the kernel transition to `X25_LINK_STATE_3` and begin forwarding queued packets.

#### The Disconnect Handshake
The kernel sends `TunHeaderDisconnect (0x02)` with an **empty payload** when the link is terminated (`x25_dev.c:x25_terminate_link`). On receipt, the gateway must immediately clean up all active sessions. No echo or response is sent back to the kernel. The kernel has already called `x25_kill_by_neigh()` internally, which disconnects every AF_X25 socket on that interface with `ENETUNREACH`. Sending CLR_REQ packets back to the kernel after this point is unnecessary.

#### Kernel Link State Machine
The kernel maintains an internal link state for each neighbor device (`x25_link.c`):

| State | Name | Description |
| :--- | :--- | :--- |
| `X25_LINK_STATE_0` | Down | No link. Frame transmission triggers link establishment. |
| `X25_LINK_STATE_1` | Connect Sent | Kernel sent TunHeaderConnect, awaiting echo. |
| `X25_LINK_STATE_2` | Restart Sent | Echo received; RESTART_REQUEST sent, awaiting RESTART_CONFIRMATION. |
| `X25_LINK_STATE_3` | Operational | RESTART_CONFIRMATION received; ready for data. |

#### Observed Behaviours

Additionally, the gateway must manage session state based on X.25 Control Packets:
*   **Session Cleanup**: Upon receiving a `PktTypeClearRequest` from the TUN interface, the gateway must remove the associated LCI mapping to free resources and prevent state desynchronization.
*   **Interface Shutdown**: Receipt of a `TunHeaderDisconnect (0x02)` (with empty payload) signals a link-layer teardown, and the gateway should immediately close all active sessions associated with that interface. The kernel has already terminated all sockets internally; no CLR_REQ echo to the kernel is needed.

## Linux X.25 IOCTLs

The module supports several IOCTLs for management. All X.25-specific IOCTLs are in the `SIOCPROTOPRIVATE` range starting at `0x89E0`.

### Complete IOCTL Table

| IOCTL | Value | Structure | Description |
| :--- | :--- | :--- | :--- |
| `SIOCX25GSUBSCRIP` | `0x89E0` | `x25_subscrip_struct` | Get interface LCI ranges and facility masks. |
| `SIOCX25SSUBSCRIP` | `0x89E1` | `x25_subscrip_struct` | Set LCI ranges and global facility masks. Requires `CAP_NET_ADMIN`. |
| `SIOCX25GFACILITIES` | `0x89E2` | `x25_facilities` | Get the negotiated facilities on a connected socket. |
| `SIOCX25SFACILITIES` | `0x89E3` | `x25_facilities` | Set requested facilities. Socket must be in `TCP_LISTEN` or `TCP_CLOSE` state (`af_x25.c:1465`). |
| `SIOCX25GDTEFACILITIES` | `0x89E4` | `x25_dte_facilities` | Get DTE (OSI network address extension) facilities. |
| `SIOCX25SDTEFACILITIES` | `0x89E5` | `x25_dte_facilities` | Set DTE facilities. Socket must be in `TCP_LISTEN` or `TCP_CLOSE` state. |
| `SIOCX25GCALLUSERDATA` | `0x89E6` | `x25_calluserdata` | Get the Call User Data from an incoming call. |
| `SIOCX25SCALLUSERDATA` | `0x89E7` | `x25_calluserdata` | Set Call User Data for an outgoing Call Request. |
| `SIOCX25GCAUSEDIAG` | `0x89E8` | `x25_causediag` | Get the last received Cause/Diagnostic codes. |
| `SIOCX25SCAUSEDIAG` | `0x89E9` | `x25_causediag` | Set the Cause/Diagnostic for an outgoing Clear packet. |
| `SIOCX25SCUDMATCHLEN` | `0x89EA` | `x25_subaddr` | Set how many CUD bytes a listening socket matches on. Socket must be in `TCP_CLOSE`. |
| `SIOCX25CALLACCPTAPPRV` | `0x89EB` | (none) | Enable manual call acceptance mode (clears `X25_ACCPT_APPRV_FLAG`). Socket must be in `TCP_CLOSE`. |
| `SIOCX25SENDCALLACCPT` | `0x89EC` | (none) | Send a Call Accepted for a manually-held incoming call. Socket must be `TCP_ESTABLISHED`. Requires `SIOCX25CALLACCPTAPPRV` to have been called first. |

Standard routing IOCTLs used with AF_X25 sockets:

| IOCTL | Structure | Description |
| :--- | :--- | :--- |
| `SIOCADDRT` | `x25_route_struct` | Add a prefix-based route to an interface. Requires `CAP_NET_ADMIN`. |
| `SIOCDELRT` | `x25_route_struct` | Remove a route. Requires `CAP_NET_ADMIN`. |

### Used by GoXOT
| IOCTL | Description |
| :--- | :--- |
| `SIOCADDRT` | Add a prefix-based route to an interface (tun-gateway). |
| `SIOCDELRT` | Remove a route (tun-gateway). |
| `SIOCX25GFACILITIES` | Get negotiated facilities (tun-listener diagnostic tool only). |

### Available but Unused
All other IOCTLs listed in the complete table above are available but not currently called by any goxot component.

## Data Structures

### `struct sockaddr_x25`
```c
struct sockaddr_x25 {
    sa_family_t sx25_family;      /* Must be AF_X25 */
    struct x25_address sx25_addr; /* X.121 address */
};
```

### `struct x25_address`
```c
struct x25_address {
    char x25_addr[16]; /* NUL-terminated ASCII string of digits */
};
```

### `struct x25_facilities`
```c
struct x25_facilities {
    unsigned int winsize_in, winsize_out;
    unsigned int pacsize_in, pacsize_out;
    unsigned int throughput;
    unsigned int reverse;
};
```
Note: Packet sizes in `pacsize_in`/`pacsize_out` are log2 values (e.g., `9` for 512 bytes). Window sizes are in packets (1–127).

### `struct x25_causediag`
```c
struct x25_causediag {
    unsigned char cause;
    unsigned char diagnostic;
};
```

### `struct x25_calluserdata`
```c
struct x25_calluserdata {
    unsigned int   cudlength;
    unsigned char  cuddata[128];
};
```

### `struct x25_subscrip_struct`
```c
struct x25_subscrip_struct {
    char          device[200-sizeof(unsigned long)]; /* 192 bytes on x86_64 */
    unsigned long global_facil_mask;
    unsigned int  extended;
};
```

### `struct x25_route_struct`
```c
struct x25_route_struct {
    struct x25_address address;
    unsigned int       sigdigits;
    char               device[200-sizeof(unsigned long)]; /* 192 bytes on x86_64 */
};
```

---

## Connection Operations

This section provides step-by-step procedures for common X.25 connection management tasks, including the required control header handshakes with the kernel. All TUN frames use the 4-byte PI header `[0x00, 0x00, 0x08, 0x05]` as prefix.

### 1. Open an X25 Packet Socket in PI Mode

This establishes a TUN interface ready for X.25 traffic. "PI mode" means the TUN device includes the 4-byte Protocol Information header in every frame (i.e., `IFF_NO_PI` is **not** set).

1. Open the TUN character device:
   ```
   fd = open("/dev/net/tun", O_RDWR)
   ```

2. Configure TUN mode with PI headers (do NOT include `IFF_NO_PI`):
   ```
   ioctl(fd, TUNSETIFF, ifr)  /* ifr.ifr_flags = IFF_TUN */
   ```

3. Set link type to ARPHRD_X25 (271):
   ```
   ioctl(fd, TUNSETLINK, 271)
   ```
   The kernel registers a new neighbor object in `X25_LINK_STATE_0`.

4. Bring the interface UP (opens a temporary INET socket for the IOCTL):
   ```
   ioctl(sock, SIOCSIFFLAGS, ifr)  /* flags |= IFF_UP | IFF_RUNNING */
   ```

5. Optionally add X.25 routes (requires `CAP_NET_ADMIN`; uses a temporary AF_X25 socket):
   ```
   ioctl(x25_sock, SIOCADDRT, &x25_route_struct)
   ```

6. **L2 Connect Handshake** — triggered the first time the kernel needs to transmit (e.g., on the first incoming CALL_REQ written to TUN, or on a socket `connect()` call):

   Kernel → Gateway: `[PI][0x01]` (TunHeaderConnect, empty payload)

   Gateway → Kernel: `[PI][0x01]` (echo TunHeaderConnect back)

   Kernel transitions to `X25_LINK_STATE_2` and sends RESTART_REQUEST.

7. **L3 Restart Handshake**:

   Kernel → Gateway (via TunHeaderData): `[PI][0x00][0x10, 0x00, 0xFB, 0x00, 0x00]`
   *(GFI=0x10, LCI=0, Type=RESTART_REQUEST, cause=0x00, diag=0x00)*

   Gateway → Kernel (via TunHeaderData): `[PI][0x00][0x10, 0x00, 0xFF]`
   *(GFI=0x10, LCI=0, Type=RESTART_CONFIRMATION)*

   Kernel transitions to `X25_LINK_STATE_3`. The socket is now operational.

---

### 2. Open an X.25 Connection

This describes the steps for a DTE application opening an outbound X.25 SVC via an AF_X25 socket.

1. Create the socket:
   ```c
   fd = socket(AF_X25, SOCK_SEQPACKET, 0);
   ```
   Socket is in `X25_STATE_0` / `TCP_CLOSE`.

2. Optionally configure facilities (must be done before connect, while socket is in `TCP_CLOSE`):
   ```c
   ioctl(fd, SIOCX25SFACILITIES, &fac);
   ```

3. Bind a source X.121 address:
   ```c
   bind(fd, &src_sockaddr_x25, sizeof(src_sockaddr_x25));
   ```
   Binding is **mandatory** before `connect()`; autobinding is not supported.

4. Connect to the remote address:
   ```c
   connect(fd, &dst_sockaddr_x25, sizeof(dst_sockaddr_x25));
   ```
   Kernel allocates an LCI, sets state to `X25_STATE_1` (`TCP_SYN_SENT`), and sends a CALL_REQUEST.

5. **CALL_REQUEST** — Kernel → TUN Gateway (TunHeaderData):
   ```
   [PI][0x00][GFI|LCI_H, LCI_L, 0x0B, addr_block, fac_block, CUD...]
   ```
   Gateway relays to the remote DCE over XOT.

6. **CALL_ACCEPTED** — Remote DCE → TUN Gateway (via XOT) → Kernel (TunHeaderData):
   ```
   [PI][0x00][GFI|LCI_H, LCI_L, 0x0F, addr_block, fac_block]
   ```
   Kernel state machine (`x25_state1_machine`) transitions to `X25_STATE_3` / `TCP_ESTABLISHED`.
   `connect()` returns 0 (or the socket becomes readable for non-blocking callers).

---

### 3. Close an X.25 Connection

This describes the DTE-initiated close sequence.

1. Application calls `close(fd)` or the gateway decides to clear.
   Kernel `x25_release()` runs.

2. If socket is in `X25_STATE_3` (data transfer):
   Kernel clears queues, sends CLEAR_REQUEST, enters `X25_STATE_2` (`TCP_CLOSE`), starts T23 timer.

3. **CLEAR_REQUEST** — Kernel → TUN Gateway (TunHeaderData):
   ```
   [PI][0x00][GFI|LCI_H, LCI_L, 0x13, cause, diag]
   ```
   Gateway relays to remote DCE over XOT.

4. **CLEAR_CONFIRMATION** — Remote DCE → TUN Gateway → Kernel (TunHeaderData):
   ```
   [PI][0x00][GFI|LCI_H, LCI_L, 0x17]
   ```
   Kernel `x25_state2_machine` calls `x25_disconnect()`, moves to `X25_STATE_0`, socket is freed.

5. If T23 expires (180 s default) with no confirmation: kernel destroys socket unconditionally.

---

### 4. Receive a Notification that an X.25 Connection Was Closed Remotely and Clean Up

The remote DCE initiates clearing.

1. **CLEAR_REQUEST** — Remote DCE → TUN Gateway (via XOT):
   Gateway writes to TUN as TunHeaderData:
   ```
   [PI][0x00][GFI|LCI_H, LCI_L, 0x13, cause, diag]
   ```

2. Kernel `x25_state3_machine` receives CLEAR_REQUEST:
   - Sends **CLEAR_CONFIRMATION** back via TunHeaderData: `[PI][0x00][GFI|LCI_H, LCI_L, 0x17]`
   - Calls `x25_disconnect(sk, 0, cause, diag)` → socket moves to `X25_STATE_0`, `sk_state = TCP_CLOSE`
   - Wakes any blocked `recv()` with EOF or error.

3. Gateway reads **CLEAR_CONFIRMATION** from TUN (TunHeaderData). Gateway forwards CLR_CONF to remote, removes the LCI mapping from the session manager.

4. Application on the socket receives EOF or error from `recv()`/`recvmsg()`, then calls `close(fd)`.

---

### 5. Receive a Notification that an X.25 Packet Socket Was Disconnected Remotely and Clean Up

The link layer (L2) is terminated by the kernel. This affects all connections on the interface.

1. Kernel sends **TunHeaderDisconnect** with empty payload to the TUN device:
   ```
   [PI][0x02]
   ```
   This is generated by `x25_terminate_link()`, which is called on `NETDEV_DOWN` or when `X25_IFACE_DISCONNECT` is received from the device.

2. Gateway reads the frame. The payload is empty, only the control byte `0x02` is present.

3. Gateway calls `closeAllSessions()`:
   - For each active session: send CLEAR_REQUEST to the remote XOT peer (cause: `NetworkCongestion` or `OutOfOrder`).
   - Remove all sessions from the session manager.

4. **No response is sent back to the kernel.** The kernel has already called `x25_kill_by_neigh()` internally, disconnecting all AF_X25 sockets on that interface with `ENETUNREACH`. Any further writes to those sockets will fail.

5. The TUN gateway may continue running and await a new L2 connect handshake (step 6–7 in Use Case 1) before accepting further calls.

---

### 6. Clear All Connections on a Packet Socket and Shut Down

Gateway-initiated graceful shutdown.

1. For each active session in the session manager:
   a. Send CLEAR_REQUEST to the remote XOT peer (over TCP) with an appropriate cause code.
   b. Remove the session from the session manager.

2. Send **TunHeaderDisconnect** to the kernel to instruct it to close all connections on the packet socket:
   ```
   write(tun_fd, [0x00, 0x00, 0x08, 0x05, 0x02])
   ```
   The kernel calls `x25_link_terminated()` → `x25_kill_by_neigh()` → disconnects all remaining AF_X25 sockets on this interface with `ENETUNREACH`.

3. Close the TUN file descriptor:
   ```
   close(tun_fd)
   ```
   The kernel fires `NETDEV_UNREGISTER`, cleaning up neighbor and route entries.

---

## Low Level Operations

This section describes the meaning and side effects of each atomic operation referenced in the Connection Operations section.

### `open("/dev/net/tun", O_RDWR)`
Opens the TUN/TAP control file. Returns a file descriptor that is used for all subsequent configuration and I/O on the virtual interface. The interface does not yet exist.

### `ioctl(fd, TUNSETIFF, ifr)` with `IFF_TUN`
Creates or attaches to a named TUN interface. `IFF_TUN` selects layer-3 (IP-like) framing, as opposed to `IFF_TAP` (Ethernet). Omitting `IFF_NO_PI` causes the kernel to prepend a 4-byte Protocol Information header `[0x00, 0x00, type_hi, type_lo]` to every frame, where the type field is `ETH_P_X25` (0x0805) for X.25.

### `ioctl(fd, TUNSETLINK, ARPHRD_X25)`
Sets the hardware type of the TUN interface to `ARPHRD_X25` (271). This causes the kernel's AF_X25 packet handler (`x25_lapb_receive_frame` in `x25_dev.c`) to recognise frames written to this TUN interface as X.25 LAPB frames. It also triggers `NETDEV_POST_TYPE_CHANGE`, which calls `x25_link_device_up()` to register a neighbor object for the device in `X25_LINK_STATE_0`.

### `ioctl(sock, SIOCSIFFLAGS, ifr)` with `IFF_UP | IFF_RUNNING`
Brings the network interface up. The kernel fires `NETDEV_UP`, which for `ARPHRD_X25` devices re-registers the neighbor (if not already present). The interface is now visible to the X.25 routing layer but the L2 link is still in `X25_LINK_STATE_0`.

### `ioctl(x25_sock, SIOCADDRT, &x25_route_struct)`
Adds an X.25 routing entry. The kernel (in `x25_route.c:x25_add_route`) stores a prefix+sigdigits→device mapping. When an AF_X25 socket `connect()` is called to a matching address, the kernel uses this route to determine which TUN interface to use. Requires an open AF_X25 socket (for the IOCTL dispatcher) and `CAP_NET_ADMIN`.

### `ioctl(x25_sock, SIOCDELRT, &x25_route_struct)`
Removes an X.25 routing entry. Existing connected sockets are not affected.

### Write `TunHeaderConnect (0x01)` to TUN
Echo to the kernel's `X25_IFACE_CONNECT` signal. In `x25_link.c:x25_link_established()`, when this echo is received, the kernel transitions the neighbor from `X25_LINK_STATE_0/1` to `X25_LINK_STATE_2` and immediately sends an X.25 `RESTART_REQUEST` (LCI=0) as a `TunHeaderData` frame. Any frames queued while the link was down remain queued until `X25_LINK_STATE_3`.

### Respond to `RESTART_REQUEST` with `RESTART_CONFIRMATION`
Sent as `TunHeaderData` with LCI=0 and packet type `0xFF`. When the kernel's `x25_link_control()` receives this in `X25_LINK_STATE_2`, it transitions to `X25_LINK_STATE_3` and flushes all queued outbound frames to the device. Failure to send RESTART_CONFIRMATION leaves the link in `X25_LINK_STATE_2` and the T20 restart timer (default 180 s) retransmits the RESTART_REQUEST repeatedly.

### `socket(AF_X25, SOCK_SEQPACKET, 0)`
Creates an AF_X25 socket in `X25_STATE_0` / `TCP_CLOSE`. Initialises internal queues (ack_queue, fragment_queue, interrupt queues), default facilities (window size 2, packet size 128), and timers T21/T22/T23/T2. The socket is marked `SOCK_ZAPPED` until bound.

### `ioctl(fd, SIOCX25SFACILITIES, &fac)`
Sets the facilities (window size, packet size, throughput, reverse charging) to be requested in the outgoing CALL_REQUEST. Only callable when the socket is in `TCP_LISTEN` or `TCP_CLOSE` state; returns `EINVAL` otherwise. Values are validated against allowed ranges (`af_x25.c:1468–1494`).

### `bind(fd, &sockaddr_x25, len)`
Registers the socket's source X.121 address. Adds the socket to the global `x25_list` (protected by `x25_list_lock`), clears `SOCK_ZAPPED`. Must be called before `connect()`. The address must consist only of ASCII digit characters.

### `connect(fd, &sockaddr_x25, len)` (blocking)
Looks up the route for the destination address, acquires a neighbour, allocates a unique LCI via `x25_new_lci()`, sets state to `X25_STATE_1` / `TCP_SYN_SENT`, and sends a CALL_REQUEST via `x25_write_internal()`. Starts T21 timer. Blocks in `x25_wait_for_connection_establishment()` until a CALL_ACCEPTED or CLEAR_REQUEST is received, or T21 fires. Side effect: if the link is in `X25_LINK_STATE_0`, this triggers the L2 connect handshake.

### Kernel sends `CLEAR_REQUEST` via `TunHeaderData`
Generated by `x25_write_internal(sk, X25_CLEAR_REQUEST)` in response to `close(fd)` when the socket is in a connected state. The cause and diagnostic bytes come from `x25->causediag`. The socket transitions to `X25_STATE_2` and starts T23. The gateway must relay this to the remote peer.

### Kernel sends `CLEAR_CONFIRMATION` via `TunHeaderData`
Generated in response to receiving a CLEAR_REQUEST from the gateway (remote-initiated clear). The socket's state machine (`x25_state3_machine`) calls `x25_write_internal(sk, X25_CLEAR_CONFIRMATION)` and then `x25_disconnect()`. `x25_disconnect()` clears queues, stops timers, sets LCI to 0, sets state to `X25_STATE_0`, and wakes waiting processes.

### Gateway sends `CLEAR_CONFIRMATION` via `TunHeaderData`
The gateway sends CLR_CONF back to the kernel in response to a kernel-originated CLR_REQ (i.e., when the kernel clears a connection locally and the gateway is notified). This unblocks any call in `X25_STATE_2` and allows the kernel to destroy the socket. Without this confirmation, the kernel waits for T23 to fire.

### Write `TunHeaderDisconnect (0x02)` to TUN
Instructs the kernel to terminate the L2 link. `x25_lapb_receive_frame()` calls `x25_link_terminated(nb)` which: sets neighbor state to `X25_LINK_STATE_0`, purges the neighbor's outbound queue, stops the T20 timer, and calls `x25_kill_by_neigh(nb)`. `x25_kill_by_neigh` iterates all sockets and calls `x25_disconnect(s, ENETUNREACH, 0, 0)` for every socket associated with this neighbor. This effectively clears all connections on the packet socket. Any pending `connect()` or `recv()` call on those sockets returns immediately with `ENETUNREACH`.

### Receive `TunHeaderDisconnect (0x02)` from TUN
The kernel sends this (via `x25_terminate_link()`) when the link is administratively terminated. The frame has an empty payload; only the 5-byte `[PI][0x02]` sequence is written to the TUN fd. On receipt, the gateway must clean up all sessions. The kernel has already killed all associated AF_X25 sockets internally; no acknowledgement or CLR_REQ to the kernel is required.

---

## References
* `man 7 x25`: Linux X.25 protocol implementation.
* Linux Kernel: `net/x25/af_x25.c`
* Linux Kernel: `net/x25/x25_dev.c`
* Linux Kernel: `net/x25/x25_link.c`
* Linux Kernel: `net/x25/x25_in.c`
* Linux Kernel: `include/uapi/linux/x25.h`
* Linux Kernel: `include/net/x25device.h`
