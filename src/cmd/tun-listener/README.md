# TUN-LISTENER(1)

## NAME
tun-listener - X.25 Diagnostic Listener Tool

## SYNOPSIS
**tun-listener** **-address** *x25_address*

## DESCRIPTION
**tun-listener** is a diagnostic tool that binds to a specific X.25 address on a Linux interface (typically a TUN interface managed by **tun-gateway**). It accepts incoming X.25 calls and provides real-time diagnostic information to the caller.

Upon connection, it displays:
- The caller's X.25 address.
- X.25 Facilities (Window size, Packet size).
- Call User Data (CUD).

The tool will automatically disconnect any call that remains idle for more than 5 seconds.

## OPTIONS
**-address** *x25_address*
    The X.25 address (BCD string) to bind and listen on. This is a required parameter.

## EXIT STATUS
Returns 0 on success, and non-zero if binding or listening fails.

## SEE ALSO
**tun-gateway**(1)
