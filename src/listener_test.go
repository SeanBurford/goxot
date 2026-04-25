package xot

import (
	"strings"
	"testing"
)

func TestX25AddrFromBytes(t *testing.T) {
	t.Run("null_padded", func(t *testing.T) {
		addr := make([]byte, 16)
		copy(addr, "1234567")
		got := X25AddrFromBytes(addr)
		if got != "1234567" {
			t.Errorf("Expected '1234567', got %q", got)
		}
	})

	t.Run("exact_length_no_nulls", func(t *testing.T) {
		addr := []byte("1234567890123456") // exactly 16 bytes, no nulls
		got := X25AddrFromBytes(addr)
		if got != "1234567890123456" {
			t.Errorf("Expected '1234567890123456', got %q", got)
		}
	})

	t.Run("all_nulls_empty", func(t *testing.T) {
		addr := make([]byte, 16)
		got := X25AddrFromBytes(addr)
		if got != "" {
			t.Errorf("Expected empty string, got %q", got)
		}
	})

	t.Run("16_bytes_no_null_terminator", func(t *testing.T) {
		addr := []byte("abcdefghijklmnop") // 16 bytes, no trailing null
		got := X25AddrFromBytes(addr)
		if got != "abcdefghijklmnop" {
			t.Errorf("Expected full string without truncation, got %q", got)
		}
	})

	t.Run("short_slice", func(t *testing.T) {
		addr := []byte("123\x00\x00")
		got := X25AddrFromBytes(addr)
		if got != "123" {
			t.Errorf("Expected '123', got %q", got)
		}
	})
}

func TestFormatX25FacilitiesRaw(t *testing.T) {
	t.Run("typical_values", func(t *testing.T) {
		got := FormatX25FacilitiesRaw(7, 7, 7, 7)
		want := "WinIn=7, WinOut=7, PktIn=128, PktOut=128"
		if got != want {
			t.Errorf("Expected %q, got %q", want, got)
		}
	})

	t.Run("different_packet_sizes", func(t *testing.T) {
		got := FormatX25FacilitiesRaw(3, 3, 4, 5)
		want := "WinIn=3, WinOut=3, PktIn=16, PktOut=32"
		if got != want {
			t.Errorf("Expected %q, got %q", want, got)
		}
	})

	t.Run("zero_packet_size_edge", func(t *testing.T) {
		got := FormatX25FacilitiesRaw(1, 1, 0, 0)
		want := "WinIn=1, WinOut=1, PktIn=1, PktOut=1"
		if got != want {
			t.Errorf("Expected %q, got %q", want, got)
		}
	})

	t.Run("small_window_sizes", func(t *testing.T) {
		got := FormatX25FacilitiesRaw(1, 2, 7, 7)
		if !strings.Contains(got, "WinIn=1") || !strings.Contains(got, "WinOut=2") {
			t.Errorf("Expected WinIn=1 and WinOut=2 in %q", got)
		}
	})
}
