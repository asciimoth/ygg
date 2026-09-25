package core

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
)

func TestDecodeNodeInfoKey(t *testing.T) {
	validBytes := make([]byte, ed25519.PublicKeySize)
	for i := range validBytes {
		validBytes[i] = byte(i)
	}
	tests := []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "empty", wantErr: "no remote public key supplied"},
		{name: "invalid hexadecimal", value: "zz", wantErr: "failed to decode public key"},
		{name: "short", value: hex.EncodeToString(validBytes[:ed25519.PublicKeySize-1]), wantErr: "invalid public key length"},
		{name: "long", value: hex.EncodeToString(append(validBytes, 0)), wantErr: "invalid public key length"},
		{name: "valid", value: hex.EncodeToString(validBytes)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := decodeNodeInfoKey(tt.value)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("decodeNodeInfoKey() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeNodeInfoKey() error = %v", err)
			}
			if string(key[:]) != string(validBytes) {
				t.Fatalf("decodeNodeInfoKey() key = %x, want %x", key, validBytes)
			}
		})
	}
}

func TestGetNodeInfoRejectsInvalidKeyBeforeRequest(t *testing.T) {
	var info nodeinfo
	if _, err := info.getNodeInfo(hex.EncodeToString(make([]byte, ed25519.PublicKeySize-1))); err == nil {
		t.Fatal("getNodeInfo() error = nil, want invalid key length error")
	}
}
