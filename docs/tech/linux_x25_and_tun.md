# Interfacing with Linux X.25 and TUN Interfaces

This technical document describes interfacing with the Linux kernel's X.25 implementations via either the AF\_X25 socket family or the TUN network devices.  Most projects will only need to select and implement one of these two methods:

*  AF\_X25 sockets: Present a familiar socket interface to get up and running quickly without delving into X.25 packets and handshaking.  Imposes limitations (e.g. on available X.25 Facilities).
*  TUN: Provides for raw packet creation and handling, but requires the user space code to carefully manage connection state and packet encoding.

## The Linux X.25 Stack

The Linux kernel supports X.25 over various link layers, including LAPB (standard serial) and TUN (virtual encapsulation).

The AF\_X25 socket family that implements the X.25 Packet Layer Protocol (PLP) is the simpler option.  The alternative TUN interface requires that the application implement X.25 packet and protocol handling.

## AF\_X25 Sockets

Standard POSIX socket calls are used:
*   **Socket Creation**: `socket(AF_X25, SOCK_SEQPACKET, 0)`. This is the only supported socket type for AF\_X25. The protocol argument must be 0.
*   **Addressing**: Uses `struct sockaddr_x25`.

#### Open a Connection

This describes the steps for a DTE application opening an outbound X.25 SVC via an AF\_X25 socket.

1. Create the socket:
   ```c
   sockfd = socket(AF_X25, SOCK_SEQPACKET, 0);
   ```
   Creates an AF\_X25 socket in `X25_STATE_0` / `TCP_CLOSE`. Initialises internal queues (ack\_queue, fragment\_queue, interrupt queues), default facilities (window size 2, packet size 128), and timers T21/T22/T23/T2. The socket is marked `SOCK_ZAPPED` until bound.

2. Optionally configure facilities using IOCTLs (detailed later); (`SIOCX25SFACILITIES`), DTE facilities (`SIOCX25SDTEFACILITIES`), Accept Approval (`SIOCX25CALLACCPTAPPRV`) and Call User Data (`SIOCX25SCALLUSERDATA`, `SIOCX25SCUDMATCHLEN`).  These must be set before connect, while socket is in `TCP_CLOSE` or `TCP_LISTEN` (returns `EINVAL` otherwise):
   ```c
   ioctl(sockfd, SIOCX25SFACILITIES, &fac);
   ```
   `SIOCX25SFACILITIES` sets the facilities (window size, packet size, throughput, reverse charging) to be requested in the outgoing calls or negotiated on incoming calls.  Values are validated against allowed ranges (`af_x25.c:1468–1494`).

3. Bind a source X.121 address:
   ```c
   bind(sockfd, &src_sockaddr_x25, sizeof(src_sockaddr_x25));
   ```
   Registers the socket's source X.121 address. Adds the socket to the global `x25_list` (protected by `x25_list_lock`), clears `SOCK_ZAPPED`. The address must consist only of ASCII digit characters.

4. Connect to the remote address (blocking):
   ```c
   connect(sockfd, &dst_sockaddr_x25, sizeof(dst_sockaddr_x25));
   ```
   Looks up the route for the destination address, acquires a neighbour, allocates a unique LCI via `x25_new_lci()`, sets state to `X25_STATE_1` / `TCP_SYN_SENT`, and sends a `CALL_REQUEST` via `x25_write_internal()`. Starts T21 timer. Blocks in `x25_wait_for_connection_establishment()` until a `CALL_ACCEPTED` or `CLEAR_REQUEST` is received, or T21 fires. Side effect: if the link is in `X25_LINK_STATE_0`, this triggers the L2 connect handshake.

If you're interested in the facilities that were negotiated for the connection, use `SIOCX25GFACILITIES` to retrieve them after `connect()` or `accept()`.

---

#### Send and Receive Data

In order to send data, the application should send data with `send(sockfd, buf, len, MSG_EOR)`.  `MSG_EOR` indicates that the current record is complete, which helps to maintain X.25 packet boundaries.

Every `read()` call on a `SOCK_SEQPACKET` socket is expected to read an entire packet.  This means that calls to read should have sufficiently large buffers, otherwise received packets will be truncated.

X.25 also supports `INTERRUPT` packets, which can contain one byte of data under older specifications (and more or less under newer specs).  To send an interrupt packet, the application should `send(sockfd, buf, len, MSG_OOB)`.

Upon receiving an OOB `INTERRUPT` packet, the Linux kernel sends a `SIGURG` to the socket owner.  The interrupt packet data can then be read with `recv(sockfd, buf, len, MSG_OOB)`.  If a particular thread is associated with a socket, that thread should be set as the owner of the socket so that it can handle `SIGURG` (OOB Interrupt data) and `SIGPIPE` (write to closed socket):

```c
#define _GNU_SOURCE // Required for F_SETOWN_EX
#include <fcntl.h>
#include <unistd.h>
#include <sys/syscall.h>

// Within the specific thread intended to receive signals:
struct f_owner_ex owner;
owner.type = F_OWNER_TID;
owner.pid = syscall(SYS_gettid); // Get current thread's TID

fcntl(sockfd, F_SETOWN_EX, &owner);
```

The X.25 Q-Bit (Qualified Data) indicates that a data packet is meant for packet layer control rather than user data.  If you want to send/receive the Q-Bit in data packet headers, you need to set `X25_QBITINCL` socket option.  With this option enabled, the first byte of each send and receive buffer contains the Q-Bit flag (1 = Q-Bit set, 0 = Q-Bit clear).  If `X25_QBITINCL` is not set, the Q-Bit on received packets is ignored.

```c
int one = 1;
setsockopt(sockfd, SOL_X25, X25_QBITINCL, &one, sizeof(one));
```

---

#### Close a Connection

This describes the DTE-initiated close sequence.

1. Application calls `close(sockfd)` or the gateway decides to clear.
   Kernel `x25_release()` runs.

2. If socket is in `X25_STATE_3` (data transfer):
   Kernel clears queues, sends CLEAR\_REQUEST, enters `X25_STATE_2` (`TCP_CLOSE`), starts T23 timer.

3. If T23 expires (180 s default) with no confirmation: kernel destroys socket unconditionally.

---

#### Handle a Remotely Closed Connection

*  Application on the socket receives EOF or error from `recv()`/`recvmsg()`, then calls `close(sockfd)`.
*  Application receives a `SIGPIPE` from `send()`, which also returns an error, then calls `close(sockfd)`.

Note that applications can choose to `signal(SIGPIPE, SIG_IGN)` and handle the error code returned by `read()` instead of processing `SIGPIPE`

---

## Standard Socket Functions

This section documents the behaviour of standard libc socket functions when used with `AF_X25` sockets, sourced from the kernel implementation in `net/x25/af_x25.c`.

### `socket()`

```c
sockfd = socket(AF_X25, SOCK_SEQPACKET, 0);
```

`SOCK_SEQPACKET` is the only supported socket type; `SOCK_STREAM`, `SOCK_DGRAM`, and others return `ESOCKTNOSUPPORT`. The `protocol` argument must be `0`; any other value returns `EINVAL`. AF_X25 does not support network namespaces: creating a socket outside `init_net` returns `EAFNOSUPPORT`.

---

### `bind()`

```c
bind(sockfd, (struct sockaddr *)&addr, sizeof(addr));
```

Registers the socket's local X.121 address and inserts it into the global socket list.  Binding is **mandatory** before `connect()` — autobinding is not supported (`af_x25.c:800`).  The address string must consist entirely of ASCII decimal digit characters; any non-digit character returns `EINVAL`.  Binding the null X.25 address (`""` / all spaces) is accepted and acts as a wildcard.  The socket may only be bound once; a second `bind()` returns `EINVAL`.

---

### `connect()`

```c
connect(sockfd, (struct sockaddr *)&addr, sizeof(addr));
```

Looks up a route for the destination address, acquires a neighbour, allocates a unique LCI, and sends a `CALL_REQUEST`.  Blocks until `CALL_ACCEPTED` is received or T21 expires (default 200 s).

Returns:

| Return value | Condition |
| :--- | :--- |
| `0` | Connection established |
| `EINPROGRESS` | `O_NONBLOCK` set and connection in progress |
| `EISCONN` | Socket is already in `TCP_ESTABLISHED` (no reconnect on `SOCK_SEQPACKET`) |
| `EALREADY` | Connection attempt already in progress (`TCP_SYN_SENT`) |
| `EINVAL` | Not bound, bad address length, or non-digit characters in address |
| `ENETUNREACH` | No route to destination |
| `ECONNREFUSED` | `CLEAR_REQUEST` received from remote |

After `connect()` returns `EINPROGRESS`, poll for `EPOLLOUT` (writeable = connected) or `EPOLLERR`/`EPOLLHUP` (failed).  Call `connect()` a second time after `EPOLLOUT` to retrieve the final result: it returns `0` on success or a negative error code.

---

### `listen()`

```c
listen(sockfd, backlog);
```

Places a bound socket into the listening state (`TCP_LISTEN`).  Must be called on a socket that is in `SS_UNCONNECTED` state; returns `EINVAL` otherwise.  `backlog` sets `sk->sk_max_ack_backlog`, which caps the number of pending unaccepted calls queued by the kernel.

---

### `accept()`

```c
newfd = accept(sockfd, (struct sockaddr *)&peer_addr, &addrlen);
```

Dequeues one pending incoming call from the listening socket's receive queue.  Blocks until a call arrives (or `sk_rcvtimeo` expires, returning `EAGAIN`).  Returns a new, already-connected socket in `TCP_ESTABLISHED` / `X25_STATE_3`.  The `peer_addr` / `addrlen` arguments are **not** filled in by `accept()` itself — use `getpeername()` on the returned socket to obtain the caller's X.121 address.

When `SIOCX25CALLACCPTAPPRV` has been set on the listening socket, incoming calls land in `X25_STATE_5` and the application must call `ioctl(newfd, SIOCX25SENDCALLACCPT)` before data transfer begins.

---

### `getsockname()` and `getpeername()`

```c
getsockname(sockfd, (struct sockaddr *)&addr, &addrlen);  /* local address  */
getpeername(sockfd, (struct sockaddr *)&addr, &addrlen);  /* remote address */
```

Both are served by `x25_getname()` (`af_x25.c:916`).

- **`getsockname()`** returns `x25->source_addr` as a `struct sockaddr_x25`. Works in any socket state; returns an empty string (`x25_addr[0] == '\0'`) if the socket has not been bound.
- **`getpeername()`** returns `x25->dest_addr`. Returns `ENOTCONN` unless `sk->sk_state == TCP_ESTABLISHED`. On the server side, `dest_addr` is populated with the caller's address during `x25_rx_call_request()`.

Both calls return `sizeof(struct sockaddr_x25)` (18 bytes) on success.

---

### `send()`, `sendto()`, `sendmsg()`

```c
send(sockfd, buf, len, MSG_EOR);           /* normal data  */
send(sockfd, buf, len, MSG_OOB);           /* interrupt packet */
```

Accepted flags: `MSG_EOR`, `MSG_OOB`, `MSG_DONTWAIT`.  Any other flag combination returns `EINVAL`.

**`MSG_EOR` is required for normal data sends.**  The flag signals that the current record is complete.  Omitting it (i.e., passing `flags = 0`) returns `EINVAL`, because the kernel does not support partial records at the userspace interface.

If the payload exceeds the negotiated packet size (default 128 bytes, log₂-encoded in `x25->facilities.pacsize_out`), `x25_output()` fragments it into multiple X.25 data packets automatically, setting the M-bit on all but the last fragment.  The maximum single `send()` length is 65535 bytes.

**`MSG_OOB`** sends an X.25 INTERRUPT packet.  The payload is silently truncated to 32 bytes.

**`sendto()` with a destination address:** the destination must exactly match the already-connected `x25->dest_addr`; a different address returns `EISCONN`.

`SIGPIPE` is raised (and `EPIPE` returned) if `SEND_SHUTDOWN` is set on the socket — i.e., after `close()` begins teardown or after a remote clear.

---

### `recv()`, `recvfrom()`, `recvmsg()`

```c
recv(sockfd, buf, sizeof(buf), 0);         /* normal data  */
recv(sockfd, buf, sizeof(buf), MSG_OOB);   /* interrupt packet */
```

**Each call returns exactly one reassembled X.25 record** (all M-bit fragments have been coalesced by the kernel before delivery).  `MSG_EOR` is always set in `msg->msg_flags`.

If `buf` is smaller than the record, the excess is silently discarded and `MSG_TRUNC` is set.  Unlike TCP, there is no way to read the remainder in a subsequent call — size the buffer to at least the negotiated `pacsize_in` (default 128 bytes).

**`MSG_OOB`** receives from the interrupt queue.  Returns `EINVAL` if `SOCK_URGINLINE` is set or the interrupt queue is empty.  The kernel strips the X.25 header; if `X25_QBITINCL` is set, a leading zero byte (Q-bit = 0) is prepended.

**`recvfrom()` / `recvmsg()` with a non-NULL `src_addr`:** fills `msg_name` with a `struct sockaddr_x25` containing `x25->dest_addr` (the remote peer address) and sets `msg_namelen` to `sizeof(struct sockaddr_x25)`.

Blocks until data arrives unless `MSG_DONTWAIT` or `O_NONBLOCK` is set, in which case `EAGAIN` is returned if no data is available.

---

### `poll()`, `select()`, `epoll()`

AF_X25 uses the generic `datagram_poll()` (`net/core/datagram.c`).  Events reported:

| Event | Condition |
| :--- | :--- |
| `EPOLLIN \| EPOLLRDNORM` | Data in `sk_receive_queue`, or `RCV_SHUTDOWN` set |
| `EPOLLOUT \| EPOLLWRNORM` | Send buffer has space (`sock_writeable()`) |
| `EPOLLHUP` | `sk_state == TCP_CLOSE` or both shutdown directions set |
| `EPOLLRDHUP` | `RCV_SHUTDOWN` set |
| `EPOLLERR` | `sk_err` non-zero, or error queue non-empty |

**Important:** while a non-blocking `connect()` is in progress (`sk_state == TCP_SYN_SENT`), `datagram_poll()` returns the current mask without `EPOLLOUT`, even if the send buffer is nominally free.  Wait for `EPOLLOUT` to confirm the connection is established, or `EPOLLHUP`/`EPOLLERR` to detect failure.

For interrupt (OOB) data notification, use `fcntl(F_SETOWN_EX)` to direct `SIGURG` to the correct thread (see the Send and Receive Data section above).

---

### `shutdown()`

```c
shutdown(sockfd, how);  /* returns EOPNOTSUPP */
```

`shutdown()` is **not supported** (`sock_no_shutdown`); it always returns `EOPNOTSUPP`.  To half-close or fully close a connection, use `close()`.

---

### `close()`

```c
close(sockfd);
```

Behaviour depends on the current X.25 state:

| State at `close()` | Kernel action |
| :--- | :--- |
| `X25_STATE_0` (idle) or `X25_STATE_2` (awaiting clear confirmation) | `x25_disconnect()` and socket freed immediately |
| `X25_STATE_1` (awaiting call accepted), `X25_STATE_3` (data transfer), or `X25_STATE_4` (awaiting reset confirmation) | Queues cleared, `CLEAR_REQUEST` sent, T23 started (default 180 s). Socket enters `X25_STATE_2` and is orphaned with `SOCK_DESTROY` set; freed when `CLEAR_CONFIRMATION` arrives or T23 fires |
| `X25_STATE_5` (call accepted pending) | `CLEAR_REQUEST` sent, `x25_disconnect()` called, socket freed immediately |

---

### `socketpair()`

Not supported; always returns `EOPNOTSUPP`.

---

### `mmap()`

Not supported; always returns `EOPNOTSUPP`.

---

### `getsockopt()` and `setsockopt()`

Only `SOL_X25` / `X25_QBITINCL` is handled.  Any other `level` or `optname` returns `ENOPROTOOPT`.

```c
int one = 1;
setsockopt(sockfd, SOL_X25, X25_QBITINCL, &one, sizeof(one));
```

See the Send and Receive Data section for the effect of `X25_QBITINCL` on the data layout.

---

### `ioctl(TIOCOUTQ)` and `ioctl(TIOCINQ)`

```c
int bytes;
ioctl(sockfd, TIOCOUTQ, &bytes);   /* send buffer space remaining */
ioctl(sockfd, TIOCINQ,  &bytes);   /* bytes in next received packet */
```

- **`TIOCOUTQ`** returns `sk_sndbuf - sk_wmem_alloc`, clamped to ≥ 0.  This is the remaining space in the send buffer, not the number of bytes pending transmission.
- **`TIOCINQ`** returns the `skb->len` of the first socket buffer in `sk_receive_queue`, or 0 if the queue is empty.  Because AF_X25 delivers one complete record per `recv()`, this value equals the size of the next record.

---

## AF\_X25 Socket IOCTLs

The kernel module supports several IOCTLs for management. All X.25-specific IOCTLs are in the `SIOCPROTOPRIVATE` range starting at `0x89E0`.

### Complete IOCTL Table

Code should prefer `#include <linux/x25.h>` to get these constants where possible:

| IOCTL | Value | Structure | Description |
| :--- | :--- | :--- | :--- |
| `SIOCX25GSUBSCRIP` | `0x89E0` | `x25_subscrip_struct` | Get interface global facility mask and extended mode setting. |
| `SIOCX25SSUBSCRIP` | `0x89E1` | `x25_subscrip_struct` | Set global facility mask and extended mode setting. Requires `CAP_NET_ADMIN`. |
| `SIOCX25GFACILITIES` | `0x89E2` | `x25_facilities` | Get the negotiated facilities on a connected socket. |
| `SIOCX25SFACILITIES` | `0x89E3` | `x25_facilities` | Set requested facilities. Socket must be in `TCP_LISTEN` or `TCP_CLOSE` state (`af_x25.c:1465`). |
| `SIOCX25GCALLUSERDATA` | `0x89E4` | `x25_calluserdata` | Get the Call User Data from an incoming call. |
| `SIOCX25SCALLUSERDATA` | `0x89E5` | `x25_calluserdata` | Set Call User Data for an outgoing Call Request. |
| `SIOCX25GCAUSEDIAG` | `0x89E6` | `x25_causediag` | Get the last received Cause/Diagnostic codes. |
| `SIOCX25SCUDMATCHLEN` | `0x89E7` | `x25_subaddr` | Set how many CUD bytes a listening socket matches on. Socket must be in `TCP_CLOSE`. |
| `SIOCX25CALLACCPTAPPRV` | `0x89E8` | (none) | Enable manual call acceptance mode (clears `X25_ACCPT_APPRV_FLAG`). Socket must be in `TCP_CLOSE`. |
| `SIOCX25SENDCALLACCPT` | `0x89E9` | (none) | Send a Call Accepted for a manually-held incoming call. Socket must be `TCP_ESTABLISHED`. Requires `SIOCX25CALLACCPTAPPRV` to have been called first. |
| `SIOCX25GDTEFACILITIES` | `0x89EA` | `x25_dte_facilities` | Get DTE (OSI network address extension) facilities. |
| `SIOCX25SDTEFACILITIES` | `0x89EB` | `x25_dte_facilities` | Set DTE facilities. Socket must be in `TCP_LISTEN` or `TCP_CLOSE` state. |

### Managing X.25 Routes

Standard routing IOCTLs can be used with AF\_X25 sockets:

| IOCTL | Structure | Description |
| :--- | :--- | :--- |
| `SIOCADDRT` | `x25_route_struct` | Add a prefix-based route to an interface. Requires `CAP_NET_ADMIN`. |
| `SIOCDELRT` | `x25_route_struct` | Remove a route. Requires `CAP_NET_ADMIN`. |

`ioctl(sockfd, SIOCADDRT, &x25_route_struct)`:

Adds an X.25 routing entry. The kernel (in `x25_route.c:x25_add_route`) stores a prefix+sigdigits→device mapping. When an AF\_X25 socket `connect()` is called to a matching address, the kernel uses this route to determine which TUN interface to use. Requires an open AF\_X25 socket (for the IOCTL dispatcher) and `CAP_NET_ADMIN`.

`ioctl(sockfd, SIOCDELRT, &x25_route_struct)`:

Removes an X.25 routing entry. Existing connected sockets are not affected.

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

For incoming calls, the kernel first checks that the call's destination address matches the socket's bound address (or the socket is bound to the null/wildcard address).  It then applies CUD matching via `x25_find_listener()`:

*  If the socket's `cudmatchlength` (set by `SIOCX25SCUDMATCHLEN`) is zero, or the incoming call CUD is shorter than `cudmatchlength`, the socket is recorded as `next_best` (address-only match).
*  If `cudmatchlength > 0` and the first `cudmatchlength` bytes of the call CUD match the socket's stored CUD (`calluserdata.cuddata`), the socket is a direct match and the call is routed to it immediately.
*  If `cudmatchlength > 0` and the call CUD does not match, the socket is skipped entirely (not even `next_best`).

If no direct CUD match is found, the `next_best` socket (address-only match) receives the call.  Note that `SIOCX25SCALLUSERDATA` sets the CUD used for outgoing calls and stored for comparison; the `cudlength` field in `x25_calluserdata` is only used for outgoing calls. The effective match length is always `cudmatchlength` from `SIOCX25SCUDMATCHLEN`.

This behaviour differs from the code comment ("Note: if a listening socket has cud set it must only get calls with matching cud"). In practice, a socket with `cudmatchlength > 0` that fails CUD matching is skipped, but an address-only socket (`cudmatchlength == 0`) will still receive the call as `next_best`. To reliably filter by CUD:

1.  Set `cudmatchlength` with `SIOCX25SCUDMATCHLEN`.
2.  Set CUD bytes with `SIOCX25SCALLUSERDATA`.
3.  After `accept()`, use `SIOCX25GCALLUSERDATA` to verify the call's CUD matched as expected.

### `struct x25_subscrip_struct`
```c
struct x25_subscrip_struct {
    char          device[200-sizeof(unsigned long)]; /* 192 bytes on x86_64 */
    unsigned long global_facil_mask;
    unsigned int  extended;
};
```

`global_facil_mask` gets or sets the neighbour facilities mask:

*  `X25_MASK_REVERSE` (0x01): Include `reverse` in created facilities. (default on link device up).
*  `X25_MASK_THROUGHPUT` (0x02): Include `throughput` in created facilities. (default on link device up).
*  `X25_MASK_PACKET_SIZE` (0x04): Include `pacsize_in` and `pacsize_out` in created facilities. (default on link device up).
*  `X25_MASK_WINDOW_SIZE` (0x08): Include `winsize_in` and `winsize_out` in created facilities. (default on link device up).
*  `X25_MASK_CALLING_AE` (0x10) / `X25_MASK_CALLED_AE` (0x20): Include `X25_MARKER` + `X25_DTE_SERVICES` in created facilities.
*  `X25_MASK_CALLING_AE`: Include `X25_FAC_CALLING_AE` if it is set when creating facilities.  `dte_facs->calling_ae` can be set with `SIOCX25SDTEFACILITIES`.
*  `X25_MASK_CALLED_AE`: Include `X25_FAC_CALLED_AE` if it is set when creating facilities.  `dte_facs->called_ae` can be set with `SIOCX25SDTEFACILITIES`.

`extended` gets or sets extended window modulus support (0 = 8, 1 = 128), as well as extended GFI and M bit handling.  It does not affect LCI mapping.

`SIOCX25GSUBSCRIP` and `SIOCX25SSUBSCRIP` require `device` to be the name of an up `ARPHRD_X25` device with a registered neighbour; otherwise they have no effect.

### `struct x25_dte_facilities`

```c
struct x25_dte_facilities {
        __u16 delay_cumul;    // unused
        __u16 delay_target;   // unused
        __u16 delay_max;      // unused
        __u8 min_throughput;  // unused
        __u8 expedited;       // unused
        __u8 calling_len;
        __u8 called_len;
        __u8 calling_ae[20];
        __u8 called_ae[20];
};
```

DTE Facilities can only be set on sockets in `TCP_LISTEN` or `TCP_CLOSE` state.

### `struct x25_route_struct`
```c
struct x25_route_struct {
    struct x25_address address;
    unsigned int       sigdigits;
    char               device[200-sizeof(unsigned long)]; /* 192 bytes on x86_64 */
};
```
---

## X.25 over TUN (ARPHRD\_X25)

Software can interface with the kernel by creating a TUN device and setting its link type to `ARPHRD_X25`. This tells the kernel to treat the interface as a native X.25 packet device.

### Encapsulation and Handshake
In order to provide consistent detection of X.25 packets and maintain the kernel state machine for connections, the TUN device must be opened **without** `IFF_NO_PI` so that the 4-byte Protocol Information header is included in every frame.  PI packets exchanged with the TUN device include a 4-byte PI header (`[0x00, 0x00, 0x08, 0x05]`, referred to as `[PI]` below) followed by a 1-byte control header.

#### Control Headers
The following headers are defined (source: `net/x25/x25_dev.c`, constants from `include/net/x25device.h`):

| Value | Name | Purpose |
| :--- | :--- | :--- |
| `0x00` | `TunHeaderData` | Standard X.25 PLP packet data follows. |
| `0x01` | `TunHeaderConnect` | Link Layer (L2) connection request/ack. |
| `0x02` | `TunHeaderDisconnect` | Link Layer (L2) disconnection. |
| `0x03` | `TunHeaderParam` | Exchange of link parameters. Not used in practice for `ARPHRD_X25`. |

#### The Connect Handshake

When the kernel's X.25 stack needs to transmit a frame and the link is down (`X25_LINK_STATE_0`), it sends a `TunHeaderConnect (0x01)` frame with an empty payload (`x25_dev.c:x25_establish_link`). The gateway **must** respond with an identical `TunHeaderConnect (0x01)` frame. On receiving the echo, the kernel calls `x25_link_established()`, transitions the link to `X25_LINK_STATE_2`, and immediately sends a `RESTART_REQUEST` packet (LCI=0, type `0xFB`) as a `TunHeaderData` frame. The gateway must respond to the `RESTART_REQUEST` with a `RESTART_CONFIRMATION` (LCI=0, type `0xFF`). Only then does the kernel transition to `X25_LINK_STATE_3` and begin forwarding queued packets.

All `CALL_REQUEST`s (from `connect()`), `CALL_ACCEPTED`s (for inbound calls), `CLR_REQ`s, `CLR_CONF`s, and data frames are queued until `STATE_3`. They are flushed by `x25_link_control()` (`x25_link.c:124–126`) when `STATE_3` is entered. (COMPAT003)

If the kernel receives a `RESTART_CONFIRMATION` while already in `STATE_3`, it kills all active sockets with `ENETUNREACH`, sends a new `RESTART_REQUEST`, and returns to `STATE_2`. (COMPAT004)

When the kernel receives a `RESTART_REQUEST` while in `X25_LINK_STATE_3`, all AF\_X25 sockets are killed (`ENETUNREACH`), but the link state stays at `STATE_3` and the kernel immediately sends `RESTART_CONFIRMATION`. The kernel also remains in `STATE_3` after this (it does not transition back to `STATE_2`). The gateway reads the resulting `RESTART_CONFIRMATION` from TUN. (COMPAT005)

#### The Disconnect Handshake
The kernel sends `TunHeaderDisconnect (0x02)` with an **empty payload** when the link is terminated (`x25_dev.c:x25_link_terminated`). On receipt, the gateway must immediately clean up all active sessions. No echo or response is sent back to the kernel. The kernel has already called `x25_kill_by_neigh()` internally, which disconnects every AF\_X25 socket on that interface with `ENETUNREACH`. Sending `CLR_REQ` packets back to the kernel after this point is unnecessary.

When the TUN fd is closed, the TUN driver calls `netif_carrier_off()`, which fires `NETDEV_CHANGE` with no carrier. The X.25 `NETDEV_CHANGE` handler calls `x25_link_terminated()`, which calls `x25_kill_by_neigh()`, disconnecting all remaining AF\_X25 sockets with `ENETUNREACH`. This is the same cleanup that writing `TunHeaderDisconnect` achieves. Writing `TunHeaderDisconnect` before closing the fd is still preferable: it provides a deterministic point at which the gateway can complete session teardown before handing off to the process exit path.

#### Kernel Link State Machine
The kernel maintains an internal link state for each neighbor device (`x25_link.c`):

| State | Name | Description |
| :--- | :--- | :--- |
| `X25_LINK_STATE_0` | Down | No link. Frame transmission triggers link establishment. |
| `X25_LINK_STATE_1` | Connect Sent | Kernel sent TunHeaderConnect, awaiting echo. |
| `X25_LINK_STATE_2` | Restart Sent | Echo received; `RESTART_REQUEST` sent, awaiting `RESTART_CONFIRMATION`. |
| `X25_LINK_STATE_3` | Operational | `RESTART_CONFIRMATION` received; ready for data. |

## TUN Operations

This section provides step-by-step procedures for common X.25 connection management tasks, including the required control header handshakes with the kernel. All TUN frames use the 4-byte PI header `[0x00, 0x00, 0x08, 0x05]` as prefix.

### Open an X.25 TUN in PI Mode

This establishes a TUN interface ready for X.25 traffic. "PI mode" means the TUN device includes the 4-byte Protocol Information header in every frame (i.e., `IFF_NO_PI` is **not** set).

1. Open the TUN character device:
   ```
   tunfd = open("/dev/net/tun", O_RDWR)
   ```
   Opens the TUN/TAP control file. Returns a file descriptor that is used for all subsequent configuration and I/O on the virtual interface. The interface does not yet exist.


2. Configure TUN mode with PI headers (do NOT include `IFF_NO_PI`):
   ```
   ioctl(tunfd, TUNSETIFF, ifr)  /* ifr.ifr_flags = IFF_TUN */
   ```
   Creates or attaches to a named TUN interface. `IFF_TUN` selects layer-3 (IP-like) framing, as opposed to `IFF_TAP` (Ethernet). Omitting `IFF_NO_PI` causes the kernel to prepend a 4-byte Protocol Information header `[0x00, 0x00, type_hi, type_lo]` to every frame, where the type field is `ETH_P_X25` (0x0805) for X.25.

3. Set link type to ARPHRD\_X25 (271):
   ```
   ioctl(tunfd, TUNSETLINK, ARPHRD_X25)
   ```
   The kernel registers a new neighbor object in `X25_LINK_STATE_0`.  Sets the hardware type of the TUN interface to `ARPHRD_X25`. This causes the kernel's AF\_X25 packet handler (`x25_lapb_receive_frame` in `x25_dev.c`) to recognise frames written to this TUN interface as X.25 LAPB frames. It also triggers `NETDEV_POST_TYPE_CHANGE`, which calls `x25_link_device_up()` to register a neighbor object for the device in `X25_LINK_STATE_0`.

4. Bring the interface UP (requires setting up a temporary socket for the IOCTL):
   ```
   ioctl(sockfd, SIOCSIFFLAGS, ifr)  /* flags |= IFF_UP | IFF_RUNNING */
   ```
   Brings the network interface up operationally. Note: `NETDEV_UP` is not handled by the X.25 stack; the neighbour object was already registered during the `TUNSETLINK` step (via `NETDEV_POST_TYPE_CHANGE`). The interface is now visible to the X.25 routing layer but the L2 link is still in `X25_LINK_STATE_0`.

5. Optionally add X.25 routes (requires `CAP_NET_ADMIN`; uses a temporary AF\_X25 socket):
   ```
   ioctl(x25_sock, SIOCADDRT, &x25_route_struct)
   ```

6. **L2 Connect Handshake** — triggered the first time the kernel needs to transmit (e.g., on the first incoming `CALL_REQ` written to TUN, or on a socket `connect()` call):

   Kernel → Gateway: `[PI][0x01]` (TunHeaderConnect, empty payload)

   Gateway → Kernel: `[PI][0x01]` (echo TunHeaderConnect back)

   Kernel transitions to `X25_LINK_STATE_2` and sends `RESTART_REQUEST`. Any frames queued while the link was down remain queued until `X25_LINK_STATE_3`.

7. **L3 Restart Handshake**:

   Kernel → Gateway (via TunHeaderData): `[PI][0x00][0x10, 0x00, 0xFB, 0x00, 0x00]`
   *(GFI=0x10, LCI=0, Type=RESTART_REQUEST, cause=0x00, diag=0x00)*

   Gateway → Kernel (via TunHeaderData): `[PI][0x00][0x10, 0x00, 0xFF]`
   *(GFI=0x10, LCI=0, Type=RESTART_CONFIRMATION)*

   Kernel transitions to `X25_LINK_STATE_3`. The socket is now operational.

   `RESTART_CONFIRMATION` is sent as `TunHeaderData` with LCI=0 and packet type `0xFF`. When the kernel's `x25_link_control()` receives this in `X25_LINK_STATE_2`, it transitions to `X25_LINK_STATE_3` and flushes all queued outbound frames to the device. Failure to send `RESTART_CONFIRMATION` leaves the link in `X25_LINK_STATE_2` and the T20 restart timer (default 180 s) retransmits the `RESTART_REQUEST` repeatedly.

---

### Establishing a call using an X.25 Packet Socket in PI Mode

1. **CALL_REQUEST** — Kernel → TUN Gateway (TunHeaderData):
   ```
   [PI][0x00][GFI|LCI_H, LCI_L, 0x0B, addr_block, fac_block, CUD...]
   ```

2. **CALL_ACCEPTED** — Remote DCE → TUN Gateway → Kernel (TunHeaderData):
   ```
   [PI][0x00][GFI|LCI_H, LCI_L, 0x0F, addr_block, fac_block]
   ```
   Kernel state machine (`x25_state1_machine`) transitions to `X25_STATE_3` / `TCP_ESTABLISHED`.
   `connect()` returns 0 (or the socket becomes readable for non-blocking callers).

---

### Close an X.25 TUN Packet Connection

1. If socket is in `X25_STATE_3` (data transfer):
   Kernel clears queues, sends `CLEAR_REQUEST`, enters `X25_STATE_2` (`TCP_CLOSE`), starts T23 timer.

2. **CLEAR_REQUEST** — Kernel → TUN Gateway (TunHeaderData):
   ```
   [PI][0x00][GFI|LCI_H, LCI_L, 0x13, cause, diag]
   ```
   Gateway relays to remote DCE.

3. **CLEAR_CONFIRMATION** — Remote DCE → TUN Gateway → Kernel (TunHeaderData):
   ```
   [PI][0x00][GFI|LCI_H, LCI_L, 0x17]
   ```
   Kernel `x25_state2_machine` calls `x25_disconnect()`, moves to `X25_STATE_0`, socket is freed.

4. If T23 expires (180 s default) with no confirmation: kernel destroys socket unconditionally.

---

### Clear All Connections on a Packet Socket and Shut Down

Gateway-initiated graceful shutdown.

1. For each active session in the session manager:
   a. Send `CLEAR_REQUEST` to the remote peer (over TCP) with an appropriate cause code.
   b. Remove the session from the session manager.

2. Send **TunHeaderDisconnect** to the kernel to instruct it to close all connections on the packet socket:
   ```
   write(tunfd, [0x00, 0x00, 0x08, 0x05, 0x02])
   ```
   Instructs the kernel to terminate the L2 link. `x25_lapb_receive_frame()` calls `x25_link_terminated(nb)` which: sets neighbor state to `X25_LINK_STATE_0`, purges the neighbor's outbound queue, stops the T20 timer, and calls `x25_kill_by_neigh(nb)`. `x25_kill_by_neigh` iterates all sockets and calls `x25_disconnect(s, ENETUNREACH, 0, 0)` for every socket associated with this neighbor. This effectively clears all connections on the packet socket. Any pending `connect()` or `recv()` call on those sockets returns immediately with `ENETUNREACH`.

3. Close the TUN file descriptor:
   ```
   close(tunfd)
   ```
   The kernel fires `NETDEV_UNREGISTER`, cleaning up neighbor and route entries.

---

### Receive a Notification that an X.25 Connection Was Closed Remotely and Clean Up

The remote DCE initiates clearing.

1. **CLEAR_REQUEST** — Remote DCE → TUN Gateway:
   Gateway writes to TUN as TunHeaderData:
   ```
   [PI][0x00][GFI|LCI_H, LCI_L, 0x13, cause, diag]
   ```
   The remote peer has initiated clearing. The gateway decodes the CLEAR_REQUEST from the remote DCE and writes it to the kernel via TUN. The kernel's `x25_state3_machine` receives it and processes the remote-initiated clear.

2. Kernel `x25_state3_machine` receives `CLEAR_REQUEST`:
   - Sends **CLEAR_CONFIRMATION** back via TunHeaderData: `[PI][0x00][GFI|LCI_H, LCI_L, 0x17]`
   - Calls `x25_disconnect(sk, 0, cause, diag)` → socket moves to `X25_STATE_0`, `sk_state = TCP_CLOSE`
   - Wakes any blocked `recv()` with EOF or error.

3. Gateway reads **CLEAR_CONFIRMATION** from TUN (TunHeaderData). Gateway forwards `CLR_CONF` to remote and removes the LCI mapping from the session manager.

### Receive a Notification that an X.25 Packet Socket Was Disconnected Remotely and Clean Up

The link layer (L2) is terminated by the kernel. This affects all connections on the interface.

1. Kernel sends **TunHeaderDisconnect** with empty payload to the TUN device:
   ```
   [PI][0x02]
   ```
   The kernel sends this (via `x25_link_terminated()`) when the link is terminated (on `NETDEV_CHANGE` carrier-off, `NETDEV_DOWN`, or receipt of `X25_IFACE_DISCONNECT`). The frame has an empty payload; only the 5-byte `[PI][0x02]` sequence is written to the TUN fd. On receipt, the gateway must clean up all sessions. The kernel has already killed all associated AF\_X25 sockets internally; no acknowledgement or `CLR_REQ` to the kernel is required.

2. Gateway reads the frame. The payload is empty, only the control byte `0x02` is present.

3. Gateway calls `closeAllSessions()`:
   - For each active session: send `CLEAR_REQUEST` to the remote peer (cause: `NetworkCongestion` or `OutOfOrder`).
   - Remove all sessions from the session manager.

4. **No response is sent back to the kernel.** The kernel has already called `x25_kill_by_neigh()` internally, disconnecting all AF\_X25 sockets on that interface with `ENETUNREACH`. Any further writes to those sockets will fail.

5. The TUN gateway may continue running and await a new L2 connect handshake (step 6–7 in Use Case 1) before accepting further calls.

---

## References
* `man 7 x25`: Linux X.25 protocol implementation.
* Linux Kernel: `net/x25/af_x25.c`
* Linux Kernel: `net/x25/x25_dev.c`
* Linux Kernel: `net/x25/x25_link.c`
* Linux Kernel: `net/x25/x25_in.c`
* Linux Kernel: `include/uapi/linux/x25.h`
* Linux Kernel: `include/net/x25device.h`
