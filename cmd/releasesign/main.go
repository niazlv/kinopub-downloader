// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Command releasesign generates the project's release signing key pair and
// signs release checksums with it.
//
// `kinopub update` replaces the running binary with one downloaded from GitHub.
// Checksums published next to that binary prove nothing on their own — whatever
// can replace the binary can replace the checksums — so the checksums file is
// signed with an ed25519 key whose public half is compiled into the updater.
//
// Usage:
//
//	releasesign keygen                 # print a new key pair
//	releasesign sign <file>            # sign a file, print the signature
//
// The private key is read from KINOPUB_SIGNING_KEY, never from an argument:
// command lines are visible to every process on the machine and end up in CI
// logs, while environment variables of a single step do not.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

const privateKeyEnv = "KINOPUB_SIGNING_KEY"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "keygen":
		os.Exit(keygen())
	case "pubkey":
		os.Exit(pubkey())
	case "sign":
		if len(os.Args) != 3 {
			usage()
			os.Exit(2)
		}
		os.Exit(sign(os.Args[2]))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `releasesign — release signing for kinopub

  releasesign keygen        generate a key pair
  releasesign pubkey        print the public half of %s
  releasesign sign <file>   sign a file (reads %s)

`, privateKeyEnv, privateKeyEnv)
}

// loadPrivateKey reads and validates the signing key from the environment.
func loadPrivateKey() (ed25519.PrivateKey, error) {
	raw := os.Getenv(privateKeyEnv)
	if raw == "" {
		return nil, fmt.Errorf("%s is not set", privateKeyEnv)
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid base64: %w", privateKeyEnv, err)
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%s must be a %d-byte ed25519 private key, got %d bytes",
			privateKeyEnv, ed25519.PrivateKeySize, len(key))
	}
	return ed25519.PrivateKey(key), nil
}

// pubkey derives the public half of the signing key.
//
// An ed25519 private key contains its public key, so the release workflow can
// stamp binaries with the verification key without a second secret to configure
// and keep in step with the first.
func pubkey() int {
	key, err := loadPrivateKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	pub, ok := key.Public().(ed25519.PublicKey)
	if !ok {
		fmt.Fprintf(os.Stderr, "unexpected key type\n")
		return 1
	}
	fmt.Println(base64.StdEncoding.EncodeToString(pub))
	return 0
}

func keygen() int {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate key: %v\n", err)
		return 1
	}

	// The public half goes into the source, the private half into a secret.
	// Printing both here is the only time they appear together.
	fmt.Printf("public key (paste into updater.SigningPublicKey):\n%s\n\n",
		base64.StdEncoding.EncodeToString(pub))
	fmt.Printf("private key (store as the %s repository secret, then delete this output):\n%s\n",
		privateKeyEnv, base64.StdEncoding.EncodeToString(priv))
	return 0
}

func sign(path string) int {
	key, err := loadPrivateKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", path, err)
		return 1
	}

	fmt.Println(base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(key), data)))
	return 0
}
