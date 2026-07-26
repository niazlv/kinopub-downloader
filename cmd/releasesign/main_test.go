// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func TestLoadPrivateKey(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(privateKeyEnv, base64.StdEncoding.EncodeToString(priv))

	got, err := loadPrivateKey()
	if err != nil {
		t.Fatalf("loadPrivateKey: %v", err)
	}
	if !got.Equal(priv) {
		t.Error("the key did not survive the round trip")
	}
}

// Every rejection here is a release that would otherwise be signed with
// something that is not the key, and so unverifiable by any binary.
func TestLoadPrivateKey_Rejects(t *testing.T) {
	cases := map[string]string{
		"unset":            "",
		"not base64":       "!!!not-base64!!!",
		"too short":        base64.StdEncoding.EncodeToString([]byte("short")),
		"a public key":     base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize)),
		"one byte missing": base64.StdEncoding.EncodeToString(make([]byte, ed25519.PrivateKeySize-1)),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(privateKeyEnv, value)
			if _, err := loadPrivateKey(); err == nil {
				t.Error("want an error, got nil")
			}
		})
	}
}

// The workflow stamps binaries with the key derived here and signs with the
// private half; if the two ever disagreed, every release would be rejected by
// the binaries it shipped alongside.
func TestDerivedPublicKeyMatchesSignatures(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(privateKeyEnv, base64.StdEncoding.EncodeToString(priv))

	loaded, err := loadPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := loaded.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("unexpected key type")
	}

	msg := []byte("checksums")
	if !ed25519.Verify(pub, msg, ed25519.Sign(loaded, msg)) {
		t.Error("the derived public key does not verify this key's signatures")
	}
}
