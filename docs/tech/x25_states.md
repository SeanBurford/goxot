# X.25 Packet Layer States

The X.25 Packet Layer Protocol (PLP) transitions through several states during call setup, data transfer, and clearing. This document describes the states relevant to the GoXOT gateway implementation.

## Logical Channel States

A logical channel exists in one of the following primary states as defined in ITU X.25 Section 4.

### Call Setup/Clearing States
*   **p1 (Ready)**: The logical channel is free and available for a new call.
*   **p2 (DTE Waiting)**: The DTE (Gateway) has sent a Call Request and is waiting for a Call Connected or Clear Request.
*   **p3 (DCE Waiting)**: The DCE (Peer) has sent an Incoming Call and is waiting for a Call Accepted or Clear Request.
*   **p4 (Data Transfer)**: The call is established. Data, RR, RNR, and REJ packets are exchanged.
*   **p5 (Call Clearing)**: A Clear Request has been sent, and the logical channel is waiting for a Clear Confirmation.

### Resetting States
These states are used to recover from errors on an established logical channel.
*   **d1 (Flow Control Ready)**: Normal data transfer state (sub-state of p4).
*   **d2 (DTE Reset Request)**: The DTE has initiated a reset.
*   **d3 (DCE Reset Indication)**: The DCE has initiated a reset.

### Restarting States
These states affect all logical channels on the interface simultaneously.
*   **r1 (Packet Layer Ready)**: The interface is ready for operation.
*   **r2 (DTE Restart Request)**: The DTE has initiated a restart (e.g., on gateway startup or recovery).
*   **r3 (DCE Restart Indication)**: The DCE has initiated a restart (e.g., peer interface reset).

## State Management in GoXOT

GoXOT maintains state implicitly through its relay logic. For detailed implementation rules and specific "Definitions of Done" for protocol handlers, see [goxot_state_management.md](goxot_state_management.md).

1. **Call Request**: Receipt of a `CALL_REQ` from a client or TUN interface initiates the transition from **p1** to **p2** (locally) or **p3** (if relayed).
2. **Call Connected**: Receipt of `CALL_CONN` transitions the session to **p4** (Data Transfer).
3. **Data Relay**: While in **p4**, the gateway performs bidirectional relay of Data and Flow Control packets.
4. **Clear Request**: Either party can initiate clearing. The gateway forwards the `CLR_REQ`, transitions to **p5**, and expects a `CLR_CONF` before returning the LCI to **p1**.

### Special Handling: Restart
When the Linux kernel (DCE side of our TUN) sends a `RESTART_REQ`, the `tun-gateway` immediately responds with a `RESTART_CONF`. This effectively clears all active virtual circuits on that interface and returns them to the **p1** (Ready) state.

## References
* ITU-T Recommendation X.25 (10/96) - Section 4: "Procedures for packet layer communication".
* ITU-T Recommendation X.25 (10/96) - Annex B: "State diagrams".
