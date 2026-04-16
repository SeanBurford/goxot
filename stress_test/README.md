# X.25 Stress Test Tool

This tool is designed to stress test the XOT gateway system by simulating multiple concurrent X.25 sessions.

## Features

- **Multi-threaded Sender**: Simulate high load with multiple threads.
- **Randomized Calling**: Call random X.121 addresses within a range.
- **Data Verification**: Verify data integrity with weakly unique data patterns.
- **Facility Negotiation**: Request specific window and packet sizes.
- **Detailed Reporting**: Reports min/max negotiated facilities, bandwidth, and failures.
- **Receiver Mode**: Reflects data back to the sender for end-to-end testing.

## Compilation

```bash
make
```

## Usage

### Receiver Mode
```bash
./stress_test -r -a 127100
```
- `-r`: Enable receiver mode.
- `-a`: Local X.121 address to bind to.

### Sender Mode
```bash
./stress_test -N 4 -l 4096 -d 127100,127300 -T 30 -n 500 -W 7 -P 1024
```
- `-N`: Number of threads (default 1).
- `-l`: Buffer size / max data length (default 8192).
- `-d`: Destination X.121 address range (default 127100,127300).
- `-T`: Run time in seconds (default 10).
- `-n`: Maximum number of calls (default 100).
- `-W`: Requested window size (default 4).
- `-P`: Requested packet size (default 512).
- `-a`: Local X.121 address to bind to (optional, but recommended if `connect` fails with `EINVAL`).

## Example Output

```text
--- Stress Test Summary ---
Run Time: 30.05 seconds
Calls Made: 500
Calls Received: 0
Calls Failed: 0
Packets Sent: 15420
Packets Received: 15420
Bytes Sent: 31584200
Bytes Received: 31584200
Data Mismatches: 0
Packet Size Negotiated (In):  Min: 512, Max: 512
Packet Size Negotiated (Out): Min: 512, Max: 512
Window Size Negotiated (In):  Min: 7, Max: 7
Window Size Negotiated (Out): Min: 7, Max: 7
Average Bandwidth (Sent): 1025.42 KB/s
Average Bandwidth (Recv): 1025.42 KB/s
---------------------------
```
