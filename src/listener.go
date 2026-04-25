package xot

import (
	"fmt"
	"strings"
)

// X25AddrFromBytes extracts a null-terminated X.25 address string from a
// kernel-provided address byte slice (e.g. sockaddr_x25.sx25_addr.x25_addr[:]).
func X25AddrFromBytes(addr []byte) string {
	return strings.TrimRight(string(addr), "\x00")
}

// FormatX25FacilitiesRaw formats the X.25 facility parameters returned by the
// kernel SIOCX25GFACILITIES ioctl. psizeIn and psizeOut are log₂(bytes),
// e.g. 7 → 128-byte packets.
func FormatX25FacilitiesRaw(winIn, winOut, psizeIn, psizeOut uint32) string {
	return fmt.Sprintf("WinIn=%d, WinOut=%d, PktIn=%d, PktOut=%d",
		winIn, winOut, 1<<psizeIn, 1<<psizeOut)
}
