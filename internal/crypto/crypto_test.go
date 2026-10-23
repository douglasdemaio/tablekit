package crypto

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestSealOpen(t *testing.T) {
	key, _ := base64.StdEncoding.DecodeString(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	s, err := NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal([]byte("totp-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("totp-secret")) {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := s.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "totp-secret" {
		t.Fatalf("got %q", got)
	}
	if _, err := s.Open(append(sealed[:len(sealed)-1], sealed[len(sealed)-1]^1)); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil || !ok {
		t.Fatalf("valid password rejected: %v %v", ok, err)
	}
	ok, err = VerifyPassword("wrong password entirely", hash)
	if err != nil || ok {
		t.Fatalf("invalid password accepted: %v %v", ok, err)
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
}

func TestTokensEqual(t *testing.T) {
	raw, err := RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if !TokensEqual(raw, HashToken(raw)) {
		t.Fatal("token did not verify")
	}
	if TokensEqual(raw+"x", HashToken(raw)) {
		t.Fatal("different token verified")
	}
}
