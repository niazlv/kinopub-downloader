// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package credstore

import "testing"

// Running as root must never orphan a store that already exists: an upgrade
// changed where a root process looks, and the desktop `sudo kinopub` setups
// that predate it keep their credentials under root's own home.
func TestChooseStoreHome(t *testing.T) {
	const (
		root    = "/root"
		invoker = "/home/user"
	)
	tests := []struct {
		name         string
		invokerKnown bool
		existing     []string // homes that already hold a store
		want         string
	}{
		{
			name:         "no invoker resolved keeps root's own home",
			invokerKnown: false,
			want:         root,
		},
		{
			name:         "fresh install writes where the user will read",
			invokerKnown: true,
			want:         invoker,
		},
		{
			name:         "existing sudo store under root is kept",
			invokerKnown: true,
			existing:     []string{root},
			want:         root,
		},
		{
			name:         "user's own store always wins",
			invokerKnown: true,
			existing:     []string{invoker},
			want:         invoker,
		},
		{
			name:         "both exist: the user's is the one runs will read",
			invokerKnown: true,
			existing:     []string{root, invoker},
			want:         invoker,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exists := func(p string) bool {
				for _, e := range tt.existing {
					if e == p {
						return true
					}
				}
				return false
			}
			if got := chooseStoreHome(root, invoker, tt.invokerKnown, exists); got != tt.want {
				t.Errorf("chooseStoreHome = %q, want %q", got, tt.want)
			}
		})
	}
}
