// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/logx"
	"github.com/niazlv/kinopub-downloader/internal/services/proxyprovider"
	"github.com/niazlv/kinopub-downloader/internal/services/updater"
)

// runUpdate checks GitHub Releases for a newer version and installs it.
//
// Checking is available to any build; installing is not. A binary that was not
// produced by the upstream release workflow — a local build, a fork's build, a
// build from a dirty tree — reports what it found and stops, because replacing
// it would discard whatever made it different in the first place.
func runUpdate(args []string) int {
	fs := flag.NewFlagSet("kinopub update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		checkOnly bool
		proxyURL  string
		verbose   bool
	)
	fs.BoolVar(&checkOnly, "check", false, "only report whether a newer release exists, install nothing")
	fs.BoolVar(&verbose, "v", false, "verbose output")
	fs.StringVar(&proxyURL, "proxy", "", "proxy URL (http, https, socks5)")
	registerColorFlags(fs)

	fs.Usage = func() {
		h := newHelpPrinter(os.Stderr, errStyle)
		h.title("kinopub update", "", "install the latest release")

		h.section("Usage:")
		h.commands(command{name: "kinopub update [--check] [--proxy URL]"})

		h.section("Flags:")
		h.flags(fs)
		h.blank()
		h.text("Only release builds can replace themselves, and only from the repository " +
			"they were built from. Development builds report what is available and " +
			"leave themselves alone.")
	}
	if err := fs.Parse(args); err != nil {
		// `update -h` asked for the help it just got: that is not a failure,
		// and the other subcommands already treat it as success.
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	verb := domain.VerbosityNormal
	if verbose {
		verb = domain.VerbosityVerbose
	}
	coord := logx.NewCoordinator(os.Stderr)
	logger := logx.New(consoleHandlers(verb, coord))

	proxyProv, err := proxyprovider.New(proxyURL)
	if err != nil {
		errorf("%v", err)
		return 1
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	info := buildInfo()
	up := updater.New(proxyProv.HTTPClient(), logger, info)

	rel, newer, err := up.Check(ctx)
	if err != nil {
		errorf("could not check for updates: %v", err)
		return 1
	}

	fmt.Printf("%s %s\n", outStyle.Gray("Installed:"), info.Describe())
	fmt.Printf("%s %s\n", outStyle.Gray("Latest:   "), outStyle.Bold(rel.Tag))

	switch {
	case domain.IsDevBuild(info.Version):
		fmt.Printf("\nThis is a development build, so there is nothing to compare it against.\n")
		fmt.Printf("See %s\n", outStyle.Cyan(rel.PageURL))
		return 0
	case !newer:
		fmt.Printf("\n%s\n", outStyle.Green("Already up to date."))
		return 0
	}

	fmt.Printf("\n%s %s\n", outStyle.Yellow("A newer release is available:"), outStyle.Cyan(rel.PageURL))
	if checkOnly {
		return 0
	}

	if ok, why := up.CanSelfUpdate(); !ok {
		// Not an error: the binary is doing the right thing by declining.
		fmt.Fprintln(os.Stderr)
		notef("not updating automatically — %s. Install it manually from %s", why, rel.PageURL)
		return 0
	}

	fmt.Printf("%s\n", outStyle.Gray("Downloading and verifying…"))
	if err := up.Apply(ctx, rel); err != nil {
		errorf("%v", err)
		return 1
	}

	fmt.Printf("%s Updated to %s.\n", outStyle.Green("✓"), outStyle.Bold(rel.Tag))
	return 0
}
