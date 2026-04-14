package xot

import (
	"fmt"
	"log"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"
)

type dnsCacheEntry struct {
	ips    []string
	expiry time.Time
}

var (
	dnsCache   = make(map[string]dnsCacheEntry)
	dnsCacheMu sync.Mutex
)

func ResolveXotDestination(addr string, srv *XotServerConfig) ([]string, error) {
	if srv.IP != "" {
		return []string{srv.IP}, nil
	}

	// DNS lookup
	re, err := regexp.Compile(srv.DNSPattern)
	if err != nil {
		return nil, fmt.Errorf("invalid dns_pattern: %v", err)
	}

	matches := re.FindStringSubmatch(addr)
	if matches == nil {
		return nil, fmt.Errorf("address %s does not match dns_pattern %s", addr, srv.DNSPattern)
	}

	dnsName := srv.DNSName
	for i := 1; i < len(matches); i++ {
		placeholder := fmt.Sprintf("\\%d", i)
		dnsName = strings.ReplaceAll(dnsName, placeholder, matches[i])
	}

	dnsCacheMu.Lock()
	entry, ok := dnsCache[dnsName]
	if ok && time.Now().Before(entry.expiry) {
		dnsCacheMu.Unlock()
		return entry.ips, nil
	}
	dnsCacheMu.Unlock()

	log.Printf("DNS: Resolving %s (for X.121 %s)", dnsName, addr)
	ips, err := net.LookupHost(dnsName)
	if err != nil {
		return nil, fmt.Errorf("DNS lookup failed for %s: %v", dnsName, err)
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("DNS lookup for %s returned no addresses", dnsName)
	}

	dnsCacheMu.Lock()
	dnsCache[dnsName] = dnsCacheEntry{
		ips:    ips,
		expiry: time.Now().Add(60 * time.Second),
	}
	dnsCacheMu.Unlock()

	return ips, nil
}
