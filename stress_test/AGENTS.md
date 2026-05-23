# AGENTS.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build

```bash
make          # builds stress_test and tun_close
make clean
```

Compiler: `gcc -Wall -O2 -pthread`

## stress_test

Simulates concurrent X.25 sessions against an XOT gateway. Two modes:

**Receiver** (echo server — start first):
```bash
./stress_test -r -a 127100
```

**Sender**:
```bash
./stress_test -N 4 -l 4096 -d 127100,127300 -T 30 -n 500 -W 7 -P 1024
```

| Flag | Default | Meaning |
|---|---|---|
| `-N` | 1 | Concurrent sender threads |
| `-l` | 8192 | Max packet size (bytes) |
| `-d` | `127100,127300` | Destination X.121 range (start,end) |
| `-T` | 10 | Run duration (seconds) |
| `-n` | 100 | Max calls (0 = unlimited) |
| `-W` | 4 | Requested window size |
| `-P` | 512 | Requested packet size |
| `-a` | (auto) | Local X.121 address base |
| `-b` | 1000 | Backoff on failure (ms) |
| `-C` | 10000 | Connect timeout (ms) |

**Data pattern**: each byte at offset `i` is `(i ^ thread_id ^ call_id) & 0xFF`. The receiver echoes it back; the sender verifies byte-for-byte. `data_mismatches` in the summary indicates payload corruption.

**Local address generation**: sender auto-generates unique per-call addresses using `base_addr + hour + minute + sequence`. The first 6 chars of `-a` are used as the base; if omitted, defaults to `"127001"`.

**Packet size encoding**: the kernel struct uses log₂ values (`pacsize_in/out` range 3–7). The tool converts the requested `-P` value with `log = 0; while(size > 1) { size >>= 1; log++; }`.

**Q-bit**: both sender and receiver use `X25_QBITINCL`; the kernel prepends a 1-byte Q-bit header to all packets. Stats count only the user data bytes.

**Non-blocking connect**: uses `poll()` with the `-C` timeout to detect hung remote stacks without blocking the thread.

## tun_close

Injects a CLEAR REQUEST or RESET REQUEST directly into the TUN interface. Used to clean up stuck kernel X.25 sockets after `tun-gateway` exits.

```bash
./tun_close [OPTIONS] <lci>
./tun_close -R -D tun1 -c 0x03 -d 0x0A 10
```

| Flag | Default | Meaning |
|---|---|---|
| `-D` | `tun0` | TUN device name |
| `-R` | (off) | Send RESET REQUEST (0x1B) instead of CLEAR REQUEST (0x13) |
| `-c` | 0x00 | X.25 cause code |
| `-d` | 0x00 | X.25 diagnostic code |

**Cannot run while tun-gateway is active** — `TUNSETIFF` returns `EBUSY` because the TUN device only allows one open fd at a time. This is a post-mortem cleanup tool only.

**Kernel processing depends on X.25 socket state**: fully processed in `X25_STATE_1` (awaiting call accepted) and `X25_STATE_3` (data transfer); accelerates T23 timer in `X25_STATE_2` (awaiting clear confirm); silently discarded in `X25_STATE_0` (ready/disconnected).
