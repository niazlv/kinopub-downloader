// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package androidroot reads files out of other apps' private storage on an
// Android device — but only when this process is already running as root.
//
// It exists so the CLI can, when the user launches it under root (e.g. `sudo
// kinopub …` or from a root shell), read the official kino.pub app's stored
// session and introspect its APK.
//
// Privilege policy — the tool never elevates itself. It does not invoke su,
// sudo, or any setuid helper, even if asked: a downloader that silently
// escalates to read another app's private data is indistinguishable from
// malware. Root is used only when the process already holds it (euid 0). When
// it does not, every privileged operation reports root as unavailable and the
// caller degrades gracefully or asks the user to re-run under root.
//
// One device quirk is handled here: on modern Android the shell (even as root)
// may run in a mount namespace where other apps' /data/data/<pkg> directories
// are not visible. Reading them through init's mount namespace at
// /proc/1/root/data/data/<pkg> works regardless, so reads try that path first
// and fall back to the bare one. Command execution is injected via Execer so
// this is testable without a real device.
package androidroot

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// Execer runs a program with the given arguments and returns its standard
// output. A non-zero exit must be reported as a non-nil error. It abstracts
// os/exec so tests can supply canned command results.
type Execer func(ctx context.Context, name string, args ...string) ([]byte, error)

// OSExec is the production Execer: it runs the program and returns its stdout,
// wrapping a non-zero exit (with any stderr) as an error.
func OSExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return stdout.Bytes(), fmt.Errorf("%s: %w: %s", name, err, msg)
		}
		return stdout.Bytes(), fmt.Errorf("%s: %w", name, err)
	}
	return stdout.Bytes(), nil
}

// rootShell is the shell used to run privileged scripts. Android's system shell
// has /system/bin on its PATH, which the pm calls rely on.
const rootShell = "/system/bin/sh"

// Runner runs privileged commands on the local Android device, using only root
// this process already holds — it never elevates.
//
// The zero value is not usable; construct one with New. A Runner is not safe
// for concurrent use during resolution — resolve once (Available) before
// sharing.
type Runner struct {
	exec   Execer
	euid   func() int
	logger domain.Logger
}

// Option configures a Runner.
type Option func(*Runner)

// WithLogger attaches a logger; component logs go under "androidroot".
func WithLogger(l domain.Logger) Option {
	return func(r *Runner) {
		if l != nil {
			r.logger = l.Component("androidroot")
		}
	}
}

// WithEUIDFunc overrides how the current effective uid is read (tests inject a
// fixed value). Defaults to os.Geteuid.
func WithEUIDFunc(fn func() int) Option {
	return func(r *Runner) {
		if fn != nil {
			r.euid = fn
		}
	}
}

// New builds a Runner over the given Execer. Pass OSExec for real devices.
func New(e Execer, opts ...Option) *Runner {
	r := &Runner{exec: e, euid: os.Geteuid}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Available reports whether the process is running as root, i.e. whether the
// privileged operations below can work. It is the single gate the caller uses
// to decide between reading the app and asking the user to re-run under root.
func (r *Runner) Available(_ context.Context) bool {
	return r.euid() == 0
}

// Run executes a shell script as the current (root) user and returns its
// stdout. It fails when the process is not root — it never tries to become
// root. The script is passed verbatim to the shell, so callers must quote
// arguments themselves (see quote).
func (r *Runner) Run(ctx context.Context, script string) ([]byte, error) {
	if r.euid() != 0 {
		return nil, fmt.Errorf("%w: not running as root (the tool never elevates itself)", domain.ErrAPITokenUnavailable)
	}
	return r.exec(ctx, rootShell, "-c", script)
}

// ReadAppFile reads a file from an app's private storage, e.g.
// ReadAppFile(ctx, "com.kinopub", "shared_prefs/login.xml"). It tries init's
// mount namespace first (the reliable path on modern Android) and falls back to
// the bare data path, so it works across Android versions.
func (r *Runner) ReadAppFile(ctx context.Context, pkg, relPath string) ([]byte, error) {
	rel := strings.TrimPrefix(relPath, "/")
	direct := "/data/data/" + pkg + "/" + rel
	// A single script that reads whichever candidate exists keeps this to one
	// invocation. cat's bytes go to stdout untouched (binary-safe).
	script := fmt.Sprintf(
		"for f in %s %s; do [ -f \"$f\" ] && exec cat \"$f\"; done; echo __ANDROIDROOT_MISSING__ 1>&2; exit 3",
		quote("/proc/1/root"+direct), quote(direct),
	)
	out, err := r.Run(ctx, script)
	if err != nil {
		return nil, fmt.Errorf("read %s of %s: %w", rel, pkg, err)
	}
	return out, nil
}

// Getprop returns a system property (e.g. "ro.build.version.release"). getprop
// needs no root, so it runs directly regardless of privilege — this lets the
// User-Agent carry the real Android release even when the tool is not root.
// An unset property comes back as an empty string with no error.
func (r *Runner) Getprop(ctx context.Context, key string) (string, error) {
	out, err := r.exec(ctx, "/system/bin/getprop", key)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// PackageAPKPath returns the filesystem path of an installed package's base APK
// via "pm path", stripping the "package:" prefix and taking the first (base)
// entry. Split APKs list extra lines, which are ignored.
func (r *Runner) PackageAPKPath(ctx context.Context, pkg string) (string, error) {
	out, err := r.Run(ctx, "pm path "+quote(pkg))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if p, ok := strings.CutPrefix(line, "package:"); ok && p != "" {
			return p, nil
		}
	}
	return "", fmt.Errorf("pm path: no APK path for %s", pkg)
}

// PackageDump returns "pm dump <pkg>" output, from which callers read
// versionName and similar package metadata.
func (r *Runner) PackageDump(ctx context.Context, pkg string) ([]byte, error) {
	return r.Run(ctx, "pm dump "+quote(pkg))
}

// ReadFile reads an arbitrary root-readable file (already an absolute path such
// as an APK path from PackageAPKPath), trying init's namespace first.
func (r *Runner) ReadFile(ctx context.Context, path string) ([]byte, error) {
	script := fmt.Sprintf(
		"for f in %s %s; do [ -f \"$f\" ] && exec cat \"$f\"; done; echo __ANDROIDROOT_MISSING__ 1>&2; exit 3",
		quote("/proc/1/root"+path), quote(path),
	)
	out, err := r.Run(ctx, script)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

func (r *Runner) debug(msg string, fields ...domain.Field) {
	if r.logger != nil {
		r.logger.Debug(msg, fields...)
	}
}

// quote wraps s in single quotes for POSIX sh, escaping any embedded single
// quotes. It protects paths that contain shell metacharacters (Android APK
// paths carry ~, +, = and the like).
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
