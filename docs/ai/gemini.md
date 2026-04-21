# GoXOT Development Retrospective: Gemini & Engineering Collaboration

This document serves as a retrospective on the architectural and implementation journey of the GoXOT project. It outlines the major phases of work, key discoveries, and lessons learned to improve future development cycles.

---

## Phase 1: Core Bridge Implementation (XOT-X25-TCP)

### Goal and Achievement
The primary goal was to establish a functional bridge between X.25 Over TCP (XOT) and local X.25 instances (gateways and servers). We achieved a modular structure where `xot-server` acts as the central hub, relaying traffic between standard `xot-gateway` instances (using TCP sockets) and `tun-gateway` instances (targeting Linux kernel interfaces).

### Discoveries
*   **Encapsulation Quirks**: The necessity of the 2-byte length header in the XOT protocol was straightforward, but the interaction with `unixpacket` sockets for local intra-process communication proved critical for preserving packet boundaries without additional overhead.
*   **Facility Parsing**: We discovered that many legacy X.25 implementations omit facility length fields in certain states, requiring more robust parsing logic than a strict reading of the spec might suggest.

### Improvements & Prompting Cycles
*   **Back-and-Forth**: Multiple cycles were spent refining the LCI mapping between connections.
*   **Suggestion**: Future prompts should explicitly request a "State Transition Diagram" or "LCI Lifecycle Guard" during the design phase. A structural improvement would be a centralized `SessionManager` interface instead of ad-hoc map management in main loops.

---

## Phase 2: Linux TUN Integration (ARPHRD_X25)

### Goal and Achievement
The aim was to allow Linux kernel-based X.25 applications to use the gateway by creating a virtual TUN device. We successfully implemented the `ARPHRD_X25` link type and the unique Protocol Information (PI) header encapsulation required by the kernel's character-device-to-X25 bridge.

### Discoveries
*   **The Connect Handshake**: We discovered through kernel source analysis that the gateway **must** echo the `TunHeaderConnect (0x01)` signal. Without this explicit L2 acknowledgement, the kernel leaves the interface in a non-synchronized state, silently dropping data.
*   **IOCTL Utility**: The `SIOCX25GCAUSEDIAG` IOCTL was identified as the only reliable way to extract asynchronous protocol errors from the kernel stack.

### Improvements & Prompting Cycles
*   **Back-and-Forth**: The handshake behavior was initially documented as a "observed behavior" but not fully codified, leading to runtime failures under load.
*   **Suggestion**: When interfacing with kernel modules, prompts should specifically ask for "Low-Level Fact Finding" in the module source code (`net/x25/*`) before implementation begins.

---

## Phase 3: Protocol Formalization (Documentation & Standards)

### Goal and Achievement
The goal was to create a comprehensive technical manual in `/docs/tech` that serves as the single source of truth for GoXOT. We successfully consolidated disparate notes into structured guides for Packet Formats, Facilities, States, and Cause/Diag codes.

### Discoveries
*   **Implementation Gaps**: The act of documenting "best practices" (e.g., specific cause codes for specific failure modes) immediately revealed gaps in the existing code where generic error codes were used.
*   **Linux/Cisco Differences**: Documenting the Cause/Diag table highlighted how different vendors interpret legacy protocol nuances differently (e.g., "Out of Order" vs "Number Busy").

### Improvements & Prompting Cycles
*   **Back-and-Forth**: Merging documentation files initially led to the loss of "Observed Behaviors," requiring a restoration turn.
*   **Suggestion**: Use a "Preservation Checklist" when merging documents. Structured prompts should define "Must-Keep" sections like Handshakes, Observed Behaviors, and Known Incompatibilities.

---

## Phase 4: Best Practice Alignment (Hardening)

### Goal and Achievement
The goal was to synchronize the source code with the "Best Practices" established in Phase 3. We updated cause code usage, implemented better facility validation, and added critical session cleanup logic.

### Discoveries
*   **LCI Leakage**: We discovered that while we logged "Call Cleared," the gateway was not actively deleting session mappings from its internal maps when the kernel initiated the clear.
*   **Handshake Symmetry**: Realized that `TunHeaderDisconnect` is as critical as the Connect handshake for preventing LCI collisions on interface restart.

### Improvements & Prompting Cycles
*   **Back-and-Forth**: Issues surrounding LCI closure and state tracking required multiple follow-up turns because the initial "cleanup" was only partial (logging only).
*   **Suggestion**: Implement a **"Definition of Done" for Protocol Handlers** as a system prompt instruction. This should include: 
    1. Mapping the "Open" state.
    2. Mapping the "Close" state (both local and remote initiation).
    3. Defining the "Cleanup" of all associated maps/conns.
    4. Validating the state against the "Best Practice" documentation.
