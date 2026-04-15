package xot

import (
	"os"
	"testing"
	"time"
)

func TestConfigManager(t *testing.T) {
	filename := "test_config.json"
	content := `{
		"tun": {"lci_start": 10, "lci_end": 20},
		"servers": [{"prefix": "123/3", "ip": "1.1.1.1"}]
	}`
	err := os.WriteFile(filename, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(filename)

	cm, err := NewConfigManager(filename)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	tun := cm.GetTunConfig()
	if tun.LciStart != 10 || tun.LciEnd != 20 {
		t.Errorf("Expected TUN LCI 10-20, got %d-%d", tun.LciStart, tun.LciEnd)
	}

	srv := cm.GetServer("12345")
	if srv == nil {
		t.Errorf("GetServer failed to find matching server")
	} else if srv.IP != "1.1.1.1" {
		t.Errorf("Expected IP 1.1.1.1, got %s", srv.IP)
	}

	// Test DNS validation
	dnsContent := `{
		"servers": [
			{"prefix": "456/3", "dns_name": "example.org"},
			{"prefix": "789/3", "dns_pattern": "^(...)", "dns_name": "\\1.example.org"}
		]
	}`
	os.WriteFile(filename, []byte(dnsContent), 0644)

	// Force mod time change for reload
	now := time.Now().Add(time.Second)
	os.Chtimes(filename, now, now)

	cm.Reload()

	srv = cm.GetServer("45678")
	if srv == nil {
		t.Errorf("Failed to find DNS server 456")
	} else {
		if srv.DNSPattern != "^(...)(...)" {
			t.Errorf("Expected default DNS pattern, got %s", srv.DNSPattern)
		}
	}

	srv = cm.GetServer("78901")
	if srv == nil {
		t.Errorf("Failed to find DNS server 789")
	} else {
		if srv.DNSPattern != "^(...)" {
			t.Errorf("Expected custom DNS pattern, got %s", srv.DNSPattern)
		}
	}
}
