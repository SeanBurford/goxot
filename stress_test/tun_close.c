#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <fcntl.h>
#include <sys/ioctl.h>
#include <linux/if.h>
#include <linux/if_tun.h>
#include <errno.h>

/*
 * tun_close: Inject a CLEAR or RESET request into a TUN interface.
 * Primarily used to clean up stuck kernel X.25 sockets.
 */

#define TUN_HEADER_SIZE 5
#define PROTO_X25_PLP 0x0805

void usage(const char *prog) {
    fprintf(stderr, "Usage: %s [-D dev] [-R] [-c cause] [-d diag] <lci>\n", prog);
    fprintf(stderr, "  -D dev    TUN device (default: tun0)\n");
    fprintf(stderr, "  -R        Send RESET REQUEST (0x1B) instead of CLEAR REQUEST (0x13)\n");
    fprintf(stderr, "  -c cause  Cause code (default: 0x00)\n");
    fprintf(stderr, "  -d diag   Diagnostic code (default: 0x00)\n");
    fprintf(stderr, "\nIMPORTANT LIMITATION (STRESS007):\n");
    fprintf(stderr, "  This tool opens /dev/net/tun and attaches to the named device via TUNSETIFF.\n");
    fprintf(stderr, "  For a single-queue TUN device, only one fd can be attached at a time.\n");
    fprintf(stderr, "  If tun-gateway is running and holds the TUN fd open, TUNSETIFF returns EBUSY\n");
    fprintf(stderr, "  and this tool cannot inject anything.\n");
    fprintf(stderr, "  Use this tool ONLY after tun-gateway has exited (e.g. to clear a stuck kernel\n");
    fprintf(stderr, "  socket). To clear an LCI on a live tun-gateway, use its management socket\n");
    fprintf(stderr, "  or restart the gateway.\n");
    fprintf(stderr, "\nNOTE on success message (STRESS009):\n");
    fprintf(stderr, "  'Successfully injected' confirms only that the write to the TUN fd succeeded.\n");
    fprintf(stderr, "  Whether the kernel acts on the packet depends on the socket state:\n");
    fprintf(stderr, "    X25_STATE_1 (Awaiting Call Accepted): CLEAR REQUEST fully processed.\n");
    fprintf(stderr, "    X25_STATE_3 (Data Transfer): CLEAR REQUEST fully processed.\n");
    fprintf(stderr, "    X25_STATE_2 (Awaiting Clear Confirm): Accelerates T23 expiry; harmless.\n");
    fprintf(stderr, "    X25_STATE_0 (Ready/Disconnected): Packet silently discarded by kernel.\n");
    fprintf(stderr, "\nExamples:\n");
    fprintf(stderr, "  %s 5        # Send Clear to LCI 5\n", prog);
    fprintf(stderr, "  %s -R 10   # Send Reset to LCI 10\n", prog);
    exit(1);
}

int encode_bcd(const char *addr, unsigned char *out, int start_nibble) {
    int len = strlen(addr);
    for (int i = 0; i < len; i++) {
        int nibble_idx = start_nibble + i;
        unsigned char digit = addr[i] - '0';
        if (digit > 9) digit = 0; 
        
        if (nibble_idx % 2 == 0) {
            out[nibble_idx / 2] = (digit << 4);
        } else {
            out[nibble_idx / 2] |= (digit & 0x0F);
        }
    }
    return start_nibble + len;
}

int main(int argc, char *argv[]) {
    char *dev = "tun0";
    int use_reset = 0;
    unsigned char cause = 0x00;
    unsigned char diag = 0x00;
    int opt;

    while ((opt = getopt(argc, argv, "D:Rc:d:")) != -1) {
        switch (opt) {
            case 'D': dev = optarg; break;
            case 'R': use_reset = 1; break;
            case 'c': cause = (unsigned char)strtol(optarg, NULL, 0); break;
            case 'd': diag = (unsigned char)strtol(optarg, NULL, 0); break;
            default: usage(argv[0]);
        }
    }

    if (optind >= argc) usage(argv[0]);

    int lci = atoi(argv[optind++]);

    int fd = open("/dev/net/tun", O_RDWR);
    if (fd < 0) {
        perror("open /dev/net/tun");
        fprintf(stderr, "Hint: Run as root or with CAP_NET_ADMIN.\n");
        return 1;
    }

    struct ifreq ifr;
    memset(&ifr, 0, sizeof(ifr));
    ifr.ifr_flags = IFF_TUN; 
    strncpy(ifr.ifr_name, dev, IFNAMSIZ);

    if (ioctl(fd, TUNSETIFF, (void *)&ifr) < 0) {
        perror("ioctl(TUNSETIFF)");
        close(fd);
        return 1;
    }

    unsigned char buf[1024];
    memset(buf, 0, sizeof(buf));

    // 1. Linux TUN PI Header (Packet Information)
    buf[0] = 0x00;
    buf[1] = 0x00;
    buf[2] = (PROTO_X25_PLP >> 8) & 0xFF; // 0x08
    buf[3] = PROTO_X25_PLP & 0xFF;        // 0x05
    buf[4] = 0x00; // Header type: Data (injection into line)

    // 2. X.25 Packet
    // GFI=1 (standard), LCI=lci
    buf[5] = 0x10 | ((lci >> 8) & 0x0F); 
    buf[6] = lci & 0xFF;
    buf[7] = use_reset ? 0x1B : 0x13; // RESET REQUEST or CLEAR REQUEST
    buf[8] = cause;
    buf[9] = diag;

    int pos = 10;
    if (write(fd, buf, pos) < 0) {
        perror("write to tun");
        close(fd);
        return 1;
    }

    printf("Successfully injected %s (LCI=%d, Cause=0x%02X, Diag=0x%02X) into %s\n",
           use_reset ? "RESET" : "CLEAR", lci, cause, diag, dev);

    close(fd);
    return 0;
}
