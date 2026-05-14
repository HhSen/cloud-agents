package crypto

import (
	"strings"
	"testing"
)

const testKeyHex = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

func TestEncryptDecrypt(t *testing.T) {
	plain := "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret\n-----END OPENSSH PRIVATE KEY-----"
	enc, err := Encrypt(plain, testKeyHex)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	dec, err := Decrypt(enc, testKeyHex)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if dec != plain {
		t.Fatalf("round-trip mismatch: got %q want %q", dec, plain)
	}
}

func TestEncryptProducesUniqueOutput(t *testing.T) {
	plain := "hello"
	a, _ := Encrypt(plain, testKeyHex)
	b, _ := Encrypt(plain, testKeyHex)
	if a == b {
		t.Fatal("two encryptions of same plaintext should differ (random nonce)")
	}
}

func TestDecryptWrongKey(t *testing.T) {
	enc, _ := Encrypt("secret", testKeyHex)
	wrongKey := strings.Repeat("ff", 32)
	_, err := Decrypt(enc, wrongKey)
	if err == nil {
		t.Fatal("expected error with wrong key")
	}
}

func TestDecryptInvalidCiphertext(t *testing.T) {
	_, err := Decrypt("notvalidbase64!!", testKeyHex)
	if err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestDecryptTooShort(t *testing.T) {
	// valid base64 but too short to contain nonce
	_, err := Decrypt("aGk", testKeyHex)
	if err == nil {
		t.Fatal("expected error for too-short ciphertext")
	}
}

func TestBadKeyLength(t *testing.T) {
	_, err := Encrypt("x", "deadbeef") // only 4 bytes
	if err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestBadKeyHex(t *testing.T) {
	_, err := Encrypt("x", strings.Repeat("zz", 32)) // not valid hex
	if err == nil {
		t.Fatal("expected error for invalid hex key")
	}
}
