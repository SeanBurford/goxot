package xot

import (
	"encoding/json"
	"testing"
)

func TestParseAddrSpec(t *testing.T) {
	cases := []struct {
		input   string
		want    AddrSpec
		wantErr bool
	}{
		{"", "", false},
		{"0", "", false},
		{"1998", ":1998", false},
		{"8080", ":8080", false},
		{":8080", ":8080", false},
		{"127.0.0.1:8080", "127.0.0.1:8080", false},
		{"[::1]:8080", "[::1]:8080", false},
		{"[::]:1998", "[::]:1998", false},
		// IPv6 with zone
		{"[fe80::1%eth0]:1998", "[fe80::1%eth0]:1998", false},
		// Invalid inputs
		{"notanip:8080", "", true},
		{"65536", "", true},
		{"0.0.0.0", "", true}, // missing port
	}
	for _, c := range cases {
		got, err := ParseAddrSpec(c.input)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseAddrSpec(%q): expected error, got %q", c.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseAddrSpec(%q): unexpected error: %v", c.input, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseAddrSpec(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestAddrSpecUnmarshalJSON(t *testing.T) {
	cases := []struct {
		json string
		want AddrSpec
	}{
		{`1998`, ":1998"},
		{`0`, ""},
		{`"1998"`, ":1998"},
		{`""`, ""},
		{`"0"`, ""},
		{`":8080"`, ":8080"},
		{`"127.0.0.1:8080"`, "127.0.0.1:8080"},
		{`"[::1]:9000"`, "[::1]:9000"},
	}
	for _, c := range cases {
		var a AddrSpec
		if err := json.Unmarshal([]byte(c.json), &a); err != nil {
			t.Errorf("Unmarshal(%s): %v", c.json, err)
			continue
		}
		if a != c.want {
			t.Errorf("Unmarshal(%s) = %q, want %q", c.json, a, c.want)
		}
	}
}

func TestAddrSpecDialAddr(t *testing.T) {
	cases := []struct {
		spec       AddrSpec
		resolvedIP string
		want       string
	}{
		{":1998", "10.0.0.1", "10.0.0.1:1998"},
		{"10.0.0.2:1998", "10.0.0.1", "10.0.0.2:1998"},
		{"[::1]:1998", "10.0.0.1", "[::1]:1998"},
		{"", "10.0.0.1", "10.0.0.1:1998"},
		// IPv6 resolved IP
		{":1998", "::1", "[::1]:1998"},
	}
	for _, c := range cases {
		got := c.spec.DialAddr(c.resolvedIP)
		if got != c.want {
			t.Errorf("AddrSpec(%q).DialAddr(%q) = %q, want %q", c.spec, c.resolvedIP, got, c.want)
		}
	}
}
