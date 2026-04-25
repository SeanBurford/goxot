package xot

import (
	"strings"
	"testing"
)

func TestResolveXotDestination(t *testing.T) {
	// This test will likely fail in this environment because of actual DNS lookups,
	// but we can at least test the pattern substitution logic if we had a way to mock net.LookupHost.
	// For now, let's just test that it correctly identifies IP vs DNS.
	
	ips, err := ResolveXotDestination("123456", &XotServerConfig{IP: "1.2.3.4"})
	if err != nil {
		t.Errorf("ResolveXotDestination failed for IP: %v", err)
	}
	if len(ips) != 1 || ips[0] != "1.2.3.4" {
		t.Errorf("Expected [1.2.3.4], got %v", ips)
	}

	// We won't test the actual DNS lookup here as it depends on external state.
}

func TestResolveXotDestinationInvalidPattern(t *testing.T) {
	srv := &XotServerConfig{DNSPattern: "[invalid", DNSName: "x.example.com"}
	_, err := ResolveXotDestination("12345", srv)
	if err == nil {
		t.Fatal("Expected error for invalid dns_pattern")
	}
	if !strings.Contains(err.Error(), "invalid dns_pattern") {
		t.Errorf("Expected 'invalid dns_pattern' in error, got: %v", err)
	}
}

func TestResolveXotDestinationNoMatch(t *testing.T) {
	srv := &XotServerConfig{
		DNSPattern: `^(\d{3})(\d+)`,
		DNSName:    `\2.\1.local`,
	}
	_, err := ResolveXotDestination("abc", srv)
	if err == nil {
		t.Fatal("Expected error when address does not match pattern")
	}
	if !strings.Contains(err.Error(), "abc") {
		t.Errorf("Expected address in error message, got: %v", err)
	}
}

func TestResolveXotDestinationSubstitution(t *testing.T) {
	// net.LookupHost("127.0.0.1") returns ["127.0.0.1"] without DNS
	srv := &XotServerConfig{
		DNSPattern: `^(.*)`,
		DNSName:    `127.0.0.1`,
	}
	ips, err := ResolveXotDestination("anyaddress", srv)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	found := false
	for _, ip := range ips {
		if ip == "127.0.0.1" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected 127.0.0.1 in result, got %v", ips)
	}
}
