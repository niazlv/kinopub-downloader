// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package androidroot

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeExec records calls and replies from a table keyed by the full argv.
type fakeExec struct {
	calls   [][]string
	replies map[string]reply
}

type reply struct {
	out []byte
	err error
}

func (f *fakeExec) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	argv := append([]string{name}, args...)
	f.calls = append(f.calls, argv)
	if r, ok := f.replies[strings.Join(argv, "\x00")]; ok {
		return r.out, r.err
	}
	return nil, errors.New("command not found")
}

// invokedSuOrSudo reports whether any elevation binary was ever executed. The
// tool must never do this.
func (f *fakeExec) invokedSuOrSudo() bool {
	for _, c := range f.calls {
		base := c[0]
		if i := strings.LastIndexByte(base, '/'); i >= 0 {
			base = base[i+1:]
		}
		if base == "su" || base == "sudo" || base == "tsu" {
			return true
		}
	}
	return false
}

func key(argv ...string) string { return strings.Join(argv, "\x00") }

func euidRoot() int    { return 0 }
func euidNonRoot() int { return 10359 }

func TestNonRootIsUnavailableAndNeverElevates(t *testing.T) {
	fe := &fakeExec{replies: map[string]reply{}}
	r := New(fe.exec, WithEUIDFunc(euidNonRoot))

	if r.Available(context.Background()) {
		t.Fatal("Available() = true for a non-root process")
	}
	if _, err := r.Run(context.Background(), "echo hi"); err == nil {
		t.Fatal("Run() error = nil, want failure when not root")
	}
	if _, err := r.ReadAppFile(context.Background(), "com.kinopub", "shared_prefs/login.xml"); err == nil {
		t.Fatal("ReadAppFile error = nil, want failure when not root")
	}
	if fe.invokedSuOrSudo() {
		t.Error("an elevation binary was invoked; the tool must never elevate")
	}
	// Nothing privileged should have been executed at all.
	if len(fe.calls) != 0 {
		t.Errorf("unexpected commands run while not root: %v", fe.calls)
	}
}

func TestAlreadyRootUsesPlainShell(t *testing.T) {
	fe := &fakeExec{replies: map[string]reply{
		key(rootShell, "-c", "echo hi"): {out: []byte("hi\n")},
	}}
	r := New(fe.exec, WithEUIDFunc(euidRoot))

	if !r.Available(context.Background()) {
		t.Fatal("Available() = false when process is already root")
	}
	if _, err := r.Run(context.Background(), "echo hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fe.invokedSuOrSudo() {
		t.Error("su/sudo invoked even though the process is already root")
	}
}

func TestReadAppFileTriesProcRootFirst(t *testing.T) {
	fe := &fakeExec{replies: map[string]reply{}}
	r := New(fe.exec, WithEUIDFunc(euidRoot))
	_, _ = r.ReadAppFile(context.Background(), "com.kinopub", "shared_prefs/login.xml")

	last := fe.calls[len(fe.calls)-1]
	if last[0] != rootShell || last[1] != "-c" {
		t.Fatalf("unexpected read invocation: %v", last)
	}
	script := last[2]
	proc := strings.Index(script, "/proc/1/root/data/data/com.kinopub/shared_prefs/login.xml")
	bare := strings.Index(script, "'/data/data/com.kinopub/shared_prefs/login.xml'")
	if proc < 0 || bare < 0 {
		t.Fatalf("script missing candidate paths: %q", script)
	}
	if proc > bare {
		t.Errorf("/proc/1/root path must be tried first; script: %q", script)
	}
}

func TestPackageAPKPathStripsPrefixAndPicksBase(t *testing.T) {
	fe := &fakeExec{replies: map[string]reply{
		key(rootShell, "-c", "pm path 'com.kinopub'"): {out: []byte("package:/data/app/~~abc==/com.kinopub-xyz==/base.apk\npackage:/data/app/~~abc==/com.kinopub-xyz==/split_config.arm64_v8a.apk\n")},
	}}
	r := New(fe.exec, WithEUIDFunc(euidRoot))
	got, err := r.PackageAPKPath(context.Background(), "com.kinopub")
	if err != nil {
		t.Fatalf("PackageAPKPath: %v", err)
	}
	want := "/data/app/~~abc==/com.kinopub-xyz==/base.apk"
	if got != want {
		t.Errorf("PackageAPKPath = %q, want %q", got, want)
	}
}

func TestGetpropNeedsNoRoot(t *testing.T) {
	// getprop must work even on a non-root process, and must not elevate.
	fe := &fakeExec{replies: map[string]reply{
		key("/system/bin/getprop", "ro.build.version.release"): {out: []byte("16\n")},
	}}
	r := New(fe.exec, WithEUIDFunc(euidNonRoot))
	got, err := r.Getprop(context.Background(), "ro.build.version.release")
	if err != nil {
		t.Fatalf("Getprop: %v", err)
	}
	if got != "16" {
		t.Errorf("Getprop = %q, want 16", got)
	}
	if fe.invokedSuOrSudo() {
		t.Error("Getprop must not elevate")
	}
}

func TestQuoteEscapesMetacharacters(t *testing.T) {
	got := quote("/data/app/~~Mo+Sk==/base.apk")
	if got != "'/data/app/~~Mo+Sk==/base.apk'" {
		t.Errorf("quote = %q", got)
	}
	if got := quote("a'b"); got != `'a'\''b'` {
		t.Errorf("quote embedded quote = %q", got)
	}
}
