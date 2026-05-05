package main

import "testing"

func TestHTTPExample(t *testing.T) {
	tests := []struct {
		name          string
		transportMode string
	}{
		{name: "native", transportMode: "native"},
		{name: "loopback", transportMode: "loopback"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pair, err := newPair(tt.transportMode)
			if err != nil {
				t.Fatalf("newPair(%q): %v", tt.transportMode, err)
			}
			defer func() {
				if err := pair.Close(); err != nil {
					t.Fatalf("close pair: %v", err)
				}
			}()

			target, shutdownServer, err := startHTTPServer(pair.left.vt, pair.left.core.Address())
			if err != nil {
				t.Fatalf("start HTTP server: %v", err)
			}
			defer shutdownServer()

			body, err := doHTTPRequest(pair.right.vt, target)
			if err != nil {
				t.Fatalf("do HTTP request: %v", err)
			}
			if body != serverResponse {
				t.Fatalf("response body = %q, want %q", body, serverResponse)
			}
		})
	}
}
