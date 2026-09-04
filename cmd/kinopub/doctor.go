// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/browsercookies"
	"github.com/niazlv/kinopub-downloader/internal/lib/httpx"
	"github.com/niazlv/kinopub-downloader/internal/lib/logx"
	"github.com/niazlv/kinopub-downloader/internal/services/doctor"
	"github.com/niazlv/kinopub-downloader/internal/services/feedparser"
	"github.com/niazlv/kinopub-downloader/internal/services/inputresolver"
	"github.com/niazlv/kinopub-downloader/internal/services/mediaresolver"
	"github.com/niazlv/kinopub-downloader/internal/services/pagescraper"
	"github.com/niazlv/kinopub-downloader/internal/services/proxyprovider"
)

// runDoctor verifies downloaded files against the state file and optionally
// repairs inconsistencies.
// Usage: kinopub doctor [--fix] [--clean-tmp] [--output <dir>]
func runDoctor(args []string) int {
	fs := flag.NewFlagSet("kinopub doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		outputDir   string
		fix         bool
		cleanTmp    bool
		verbose     bool
		skipProbe   bool
		ffprobePath string
		cookie      string
		userAgent   string
		browserCk   browserCookiesFlag
		proxyURL    string
		siteHost    string
	)

	fs.StringVar(&outputDir, "output", "", "output directory to check (default: current directory)")
	fs.StringVar(&outputDir, "o", "", "output directory to check (shorthand)")
	fs.BoolVar(&fix, "fix", false, "repair state file (remove broken entries, delete corrupt files)")
	fs.BoolVar(&cleanTmp, "clean-tmp", false, "delete orphan .tmp files from interrupted downloads")
	fs.BoolVar(&verbose, "v", false, "verbose output")
	fs.BoolVar(&skipProbe, "skip-probe", false, "skip duration verification (no network, faster)")
	fs.StringVar(&ffprobePath, "ffprobe", "", "ffprobe binary path (default: ffprobe on PATH)")
	fs.StringVar(&cookie, "cookie", "", "Cookie header for resolving source")
	fs.StringVar(&userAgent, "user-agent", "", "User-Agent for resolving source")
	fs.Var(&browserCk, "browser-cookies", "auto-load cookies: safari, chrome, firefox, or auto")
	fs.StringVar(&proxyURL, "proxy", "", "proxy URL (http, https, or socks5)")
	fs.StringVar(&siteHost, "site", "", "site host to target, e.g. kino.watch (default: "+domain.DefaultSiteHost+")")
	registerColorFlags(fs)

	fs.Usage = func() {
		h := newHelpPrinter(os.Stderr, errStyle)
		h.text("Verify downloaded files against the state file and repair inconsistencies.")

		h.section("Usage:")
		h.commands(command{name: "kinopub doctor [flags]"})

		h.section("The doctor checks for:")
		h.bullet("Files recorded as completed but missing on disk", "")
		h.bullet("Files that are truncated (smaller than recorded size)", "")
		h.bullet("Files whose duration doesn't match the source", "")
		h.line("    %s", errStyle.Gray("(resolves fresh media URLs via the same pipeline as download)"))
		h.bullet("State entries with no file path (incomplete records)", "")
		h.bullet("Orphan .tmp files from interrupted downloads", "")
		h.blank()
		h.text("Duration verification resolves the series from the source (page_link/feed_url " +
			"in state metadata), gets fresh media URLs, probes them with ffprobe, and " +
			"compares with local file duration. No hardcoded thresholds.")
		h.blank()
		h.text("With --fix, broken state entries are removed and corrupt files deleted, " +
			"so the next download run will re-download the affected episodes.")

		h.section("Flags:")
		h.flags(fs)
	}

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}

	// Handle --browser-cookies consuming the next positional arg.
	posArgs := fs.Args()
	if browserCk.set && browserCk.value == browsercookies.BrowserAuto && len(posArgs) > 0 {
		if isKnownBrowser(posArgs[0]) {
			browserCk.value = strings.ToLower(posArgs[0])
		}
	}

	if outputDir == "" {
		outputDir, _ = os.Getwd()
	}

	// Resolve auth (same precedence as the main download command). A browser-load
	// failure is non-fatal here — doctor can still run read-only checks without
	// fresh source resolution.
	site := domain.SiteFromHost(siteHost)
	resolvedCookie, userAgent, _ := resolveAuth(cookie, userAgent, browserCk, site, false)

	auth := domain.RequestAuth{
		Cookie:    resolvedCookie,
		UserAgent: userAgent,
		Headers:   map[string]string{"Referer": site.Referer()},
		Site:      site,
	}

	// Set up logger.
	coord := logx.NewCoordinator(os.Stderr)
	verb := domain.VerbosityNormal
	if verbose {
		verb = domain.VerbosityVerbose
	}
	logger := logx.New(consoleHandlers(verb, coord))

	// Wire up dependencies — same services as the main download command.
	proxyProv, err := proxyprovider.New(proxyURL)
	if err != nil {
		errorf("%v", err)
		return 1
	}
	httpClient := httpx.WithAuth(proxyProv.HTTPClient(), auth)

	var resolverOpts []inputresolver.Option
	if auth.HasCredentials() {
		scraper := pagescraper.New(httpClient, logger)
		resolverOpts = append(resolverOpts, inputresolver.WithPageScraper(scraper))
	}
	inputRes := inputresolver.New(logger, resolverOpts...)
	feedPars := feedparser.New(httpClient, logger)
	mediaRes := mediaresolver.New(
		httpClient,
		makeRunOutput(),
		logger,
		auth,
	)

	deps := doctor.Deps{
		Logger:        logger,
		InputResolver: inputRes,
		FeedParser:    feedPars,
		MediaResolver: mediaRes,
	}

	opts := doctor.Options{
		OutputDir:   outputDir,
		Fix:         fix,
		CleanTmp:    cleanTmp,
		SkipProbe:   skipProbe,
		FFprobePath: ffprobePath,
	}

	report, err := doctor.Run(context.Background(), deps, opts)
	if err != nil {
		errorf("%v", err)
		return 1
	}

	// Print report.
	printDoctorReport(report, fix)

	if report.HasIssues() && !fix {
		return 1
	}
	return 0
}

// printDoctorReport outputs the doctor findings to stderr.
func printDoctorReport(report *doctor.Report, fixed bool) {
	st := errStyle
	field := func(label, format string, args ...any) {
		fmt.Fprintf(os.Stderr, "%s %s\n", st.Gray(label), fmt.Sprintf(format, args...))
	}

	fmt.Fprintf(os.Stderr, "\n%s\n", st.Bold("Doctor Report"))
	fmt.Fprintf(os.Stderr, "%s\n", st.Gray("─────────────"))

	if report.SeriesTitle != "" {
		field("Series:    ", "%s", report.SeriesTitle)
	}
	if report.SeriesID != "" {
		field("Series ID: ", "%s", report.SeriesID)
	}
	field("State file:", "%s", report.StateFile)
	field("Entries:   ", "%d total, %s healthy", report.TotalInState, st.Green(fmt.Sprintf("%d", report.Healthy)))
	if report.Skipped > 0 {
		field("Skipped:   ", "%s (remote links expired, could not verify duration)",
			st.Yellow(fmt.Sprintf("%d", report.Skipped)))
	}
	fmt.Fprintf(os.Stderr, "\n")

	if !report.HasIssues() {
		fmt.Fprintf(os.Stderr, "%s All files are consistent with the state file.\n\n",
			st.Green("✓"))
		return
	}

	// Group issues by kind for cleaner output.
	byKind := make(map[doctor.IssueKind][]doctor.Issue)
	for _, issue := range report.Issues {
		byKind[issue.Kind] = append(byKind[issue.Kind], issue)
	}

	kindOrder := []doctor.IssueKind{
		doctor.IssueMissing,
		doctor.IssueTruncated,
		doctor.IssueDurationMismatch,
		doctor.IssueSizeMismatch,
		doctor.IssueNoPath,
		doctor.IssueOrphanTmp,
	}

	for _, kind := range kindOrder {
		issues := byKind[kind]
		if len(issues) == 0 {
			continue
		}

		// A lost or damaged file is red; the rest are bookkeeping the --fix
		// pass can settle, so they read as warnings.
		heading := st.Yellow(fmt.Sprintf("%s (%d):", kind.String(), len(issues)))
		if kind == doctor.IssueMissing || kind == doctor.IssueTruncated {
			heading = st.Red(fmt.Sprintf("%s (%d):", kind.String(), len(issues)))
		}
		fmt.Fprintf(os.Stderr, "  %s\n", heading)
		for _, issue := range issues {
			if issue.Key != "" {
				fmt.Fprintf(os.Stderr, "    %s %s %s\n",
					st.Gray("•"), st.Cyan(issue.Key+":"), issue.Detail)
			} else {
				fmt.Fprintf(os.Stderr, "    %s %s\n", st.Gray("•"), issue.Detail)
			}
		}
		fmt.Fprintf(os.Stderr, "\n")
	}

	if fixed {
		fmt.Fprintf(os.Stderr, "%s State file repaired. Run the download command again to re-download affected episodes.\n\n",
			st.Green("✓"))
	} else {
		fmt.Fprintf(os.Stderr, "Run with %s to repair the state file (broken entries will be removed\n",
			st.Cyan("--fix"))
		fmt.Fprintf(os.Stderr, "so the next download re-fetches affected episodes).\n\n")
	}
}
