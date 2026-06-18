package xot

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// AddrSpec is a JSON-parsed network address. It unmarshals from either a JSON
// integer (bare port → ":port") or a JSON string ("port", "host:port", or
// "[host%zone]:port"). Zero or absent normalises to the empty string.
type AddrSpec string

// UnmarshalJSON implements json.Unmarshaler for AddrSpec.
func (a *AddrSpec) UnmarshalJSON(data []byte) error {
	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		if n == 0 {
			*a = ""
			return nil
		}
		if n < 1 || n > 65535 {
			return fmt.Errorf("port %d out of range 1-65535", n)
		}
		*a = AddrSpec(fmt.Sprintf(":%d", n))
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("addrspec: expected integer or string, got %s", data)
	}
	spec, err := ParseAddrSpec(s)
	if err != nil {
		return err
	}
	*a = spec
	return nil
}

// ParseAddrSpec validates and normalises an address string from a command-line
// flag or config value. Accepts "", a bare port number string, "host:port", or
// "[IPv6%zone]:port". Returns empty string for "" or "0" (disabled/default).
func ParseAddrSpec(s string) (AddrSpec, error) {
	if s == "" || s == "0" {
		return "", nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n < 1 || n > 65535 {
			return "", fmt.Errorf("port %d out of range 1-65535", n)
		}
		return AddrSpec(fmt.Sprintf(":%d", n)), nil
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", fmt.Errorf("invalid address %q: %v", s, err)
	}
	portN, err := strconv.Atoi(port)
	if err != nil || portN < 1 || portN > 65535 {
		return "", fmt.Errorf("invalid port in %q", s)
	}
	if host != "" {
		ipStr := host
		if idx := strings.IndexByte(host, '%'); idx >= 0 {
			ipStr = host[:idx]
		}
		if net.ParseIP(ipStr) == nil {
			return "", fmt.Errorf("invalid host IP in %q", s)
		}
	}
	return AddrSpec(s), nil
}

// DialAddr returns the TCP address to connect to, combining resolvedIP with this
// AddrSpec's port. If the spec already has an explicit host it is returned as-is.
// An empty spec uses resolvedIP with PortDefault.
func (a AddrSpec) DialAddr(resolvedIP string) string {
	if a == "" {
		return net.JoinHostPort(resolvedIP, strconv.Itoa(PortDefault))
	}
	host, port, err := net.SplitHostPort(string(a))
	if err != nil {
		return string(a)
	}
	if host == "" {
		return net.JoinHostPort(resolvedIP, port)
	}
	return string(a)
}
