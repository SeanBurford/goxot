# TUN-GATEWAY(1)

## NAME
tun-gateway - Linux TUN Interface to XOT Gateway

## SYNOPSIS
**tun-gateway** [**-tun** *name*] [**-config** *path*] [**-trace**]

## DESCRIPTION
**tun-gateway** bridges a Linux TUN interface (configured for `ARPHRD_X25`) with the XOT ecosystem. It allows standard Linux X.25 applications and sockets (AF_X25) to communicate over XOT.

It automatically manages X.25 routing table entries in the Linux kernel based on the `config.json` file and handles the X.25 Restart procedure with the kernel stack.

## OPTIONS
**-tun** *name*
    The name of the TUN interface to create or attach to. Default is "tun0".

**-config** *path*
    Path to the JSON configuration file. Used for initial route sync and watching for changes. Default is "config.json".

**-trace**
    Enable detailed trace logging of X.25 packets and TUN control headers.

## INTERFACE HEADERS
The gateway uses a 1-byte header before each X.25 packet on the TUN interface:
- **0x00**: Data
- **0x01**: Connect (used for Call Request/Accepted)
- **0x02**: Disconnect (used for Clear Request/Confirm)

## FILES
*/tmp/xot_tun.sock*
    Unix domain socket where **tun-gateway** listens for packets from **xot-server**.

## SEE ALSO
**xot-server**(1), **tun-listener**(1)
