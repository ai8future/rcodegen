package main

import (
	"testing"

	"rcodegen/pkg/server/openai"
)

// A limit that exists to bound memory must not be disabled by a typo. Anything
// that is not a positive integer stops the server rather than being read as
// "unset", so an operator who set a limit is never running without one.
func TestAsyncLimitsFromEnv(t *testing.T) {
	defaults := openai.DefaultAsyncLimits(3)

	tests := []struct {
		name      string
		live      string
		bytes     string
		wantLive  int
		wantBytes int64
		wantErr   bool
	}{
		{name: "unset uses the defaults for this slot count",
			wantLive: defaults.MaxLive, wantBytes: defaults.MaxBytes},
		{name: "both overridden", live: "2", bytes: "1024",
			wantLive: 2, wantBytes: 1024},
		{name: "one overridden", live: "5",
			wantLive: 5, wantBytes: defaults.MaxBytes},
		{name: "surrounding whitespace", live: "  7  ",
			wantLive: 7, wantBytes: defaults.MaxBytes},
		{name: "zero live is refused, not read as unset", live: "0", wantErr: true},
		{name: "zero bytes is refused, not read as unset", bytes: "0", wantErr: true},
		{name: "negative live", live: "-1", wantErr: true},
		{name: "negative bytes", bytes: "-1", wantErr: true},
		{name: "unparseable live", live: "eight", wantErr: true},
		{name: "unparseable bytes", bytes: "64MiB", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("RSERVE_ASYNC_MAX_LIVE", test.live)
			t.Setenv("RSERVE_ASYNC_MAX_BYTES", test.bytes)

			limits, err := asyncLimitsFromEnv(3)
			if test.wantErr {
				if err == nil {
					t.Fatalf("asyncLimitsFromEnv() = %+v, want an error", limits)
				}
				return
			}
			if err != nil {
				t.Fatalf("asyncLimitsFromEnv() error = %v", err)
			}
			if limits.MaxLive != test.wantLive {
				t.Errorf("MaxLive = %d, want %d", limits.MaxLive, test.wantLive)
			}
			if limits.MaxBytes != test.wantBytes {
				t.Errorf("MaxBytes = %d, want %d", limits.MaxBytes, test.wantBytes)
			}
		})
	}
}

func TestValidateBindAddress(t *testing.T) {
	tests := []struct {
		name                string
		address             string
		allowInsecureRemote bool
		wantErr             bool
	}{
		{name: "IPv4 loopback", address: "127.0.0.1"},
		{name: "IPv6 loopback", address: "::1"},
		{name: "bracketed IPv6 loopback", address: "[::1]"},
		{name: "localhost", address: "localhost"},
		{name: "all interfaces", address: "0.0.0.0", wantErr: true},
		{name: "LAN address", address: "10.0.4.10", wantErr: true},
		{name: "empty means all interfaces", address: "", wantErr: true},
		{name: "explicit override", address: "0.0.0.0", allowInsecureRemote: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateBindAddress(test.address, test.allowInsecureRemote)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateBindAddress() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
