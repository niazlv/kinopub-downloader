//go:build windows

package credstore

import (
	"fmt"
	"os/exec"
	"strings"
)

// machineSeed returns a machine-specific identifier on Windows via wmic.
func machineSeed() ([]byte, error) {
	out, err := exec.Command("wmic", "csproduct", "get", "UUID").Output()
	if err != nil {
		return nil, fmt.Errorf("wmic failed: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && line != "UUID" {
			return []byte(line), nil
		}
	}
	return nil, fmt.Errorf("could not read machine UUID via wmic")
}
