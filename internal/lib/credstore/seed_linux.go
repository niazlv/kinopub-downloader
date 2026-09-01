// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package credstore

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// machineIDPaths are the well-known stable machine identifiers, in priority
// order. The first that exists and is non-empty becomes the seed.
func machineIDPaths() []string {
	paths := []string{"/etc/machine-id"}
	// Termux on Android keeps its own tree under $PREFIX; Android itself has
	// no /etc/machine-id.
	if prefix := os.Getenv("PREFIX"); prefix != "" {
		paths = append(paths, filepath.Join(prefix, "etc", "machine-id"))
	}
	paths = append(paths, "/var/lib/dbus/machine-id")
	return paths
}

// machineSeed returns a machine-specific identifier on Linux (including
// Android/Termux).
//
// It prefers a persistent machine id — /etc/machine-id, $PREFIX/etc/machine-id
// or /var/lib/dbus/machine-id — and, when none of those exist, generates one
// and stores it so it survives reboots. Android has none of the standard files,
// and the boot id this code used to fall back to changes on every reboot, which
// silently made every saved credential undecryptable after a restart. Only when
// no id can be persisted at all does it fall back to the boot id, so the tool
// still works read-only within a single boot.
//
// The generated id is a file on the same device, exactly like /etc/machine-id
// on a desktop: it does not defend against someone who can read the whole
// device, only against a stolen credentials file being decrypted elsewhere.
func machineSeed() ([]byte, error) {
	for _, path := range machineIDPaths() {
		if id := readMachineID(path); id != "" {
			return []byte(id), nil
		}
	}

	// Nothing standard exists (typical on Android): create one and keep it.
	if id, err := generatePersistentMachineID(); err == nil {
		return []byte(id), nil
	}

	// Last resort: the boot id. Stable only until the next reboot — see the
	// legacy seeds below, which let a file written under it still be read.
	if id := readMachineID(bootIDPath); id != "" {
		return []byte(id), nil
	}

	return nil, fmt.Errorf("no machine identifier found and none could be created: tried %s, %s",
		strings.Join(machineIDPaths(), ", "), bootIDPath)
}

const bootIDPath = "/proc/sys/kernel/random/boot_id"

// legacySeeds returns seeds that older versions may have encrypted with, tried
// only when the primary seed fails to decrypt. A file written before the
// persistent id existed was keyed on the boot id, so it stays readable for the
// remainder of that boot and is then re-encrypted with the stable seed.
func legacySeeds() [][]byte {
	if id := readMachineID(bootIDPath); id != "" {
		return [][]byte{[]byte(id)}
	}
	return nil
}

// readMachineID reads and trims an id file, returning "" when absent or empty.
func readMachineID(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// generatePersistentMachineID creates a random id and writes it where the next
// run will find it: $PREFIX/etc/machine-id under Termux, otherwise beside the
// credential file. It is world-readable (0644, like /etc/machine-id) so that a
// store written by root and one read by the ordinary user derive the same key.
func generatePersistentMachineID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf)

	var candidates []string
	if prefix := os.Getenv("PREFIX"); prefix != "" {
		candidates = append(candidates, filepath.Join(prefix, "etc", "machine-id"))
	}
	// Fall back to the config directory, which is always writable by the user
	// whose credentials these are.
	if dir, err := credDir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, "machine-id"))
	}

	for _, path := range candidates {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			continue
		}
		// Another process may have won the race; prefer whatever landed first
		// so both agree on the same seed.
		if existing := readMachineID(path); existing != "" {
			return existing, nil
		}
		if err := os.WriteFile(path, []byte(id+"\n"), 0644); err != nil {
			continue
		}
		chownToStoreOwner(path)
		return id, nil
	}
	return "", fmt.Errorf("could not persist a machine id")
}
