package main

import "testing"

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
