// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package main is the CLI entrypoint for the kinopub downloader.
// It parses flags, builds the RunConfig, wires up all services, and
// delegates to the app composition root (Req 1.4, 7.3, 15.1, 15.2, 15.3, 16.3).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/niazlv/kinopub-downloader/internal/app/kinopub"
	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/audiomenu"
	"github.com/niazlv/kinopub-downloader/internal/lib/browsercookies"
	"github.com/niazlv/kinopub-downloader/internal/lib/credstore"
	"github.com/niazlv/kinopub-downloader/internal/lib/desktopnotify"
	"github.com/niazlv/kinopub-downloader/internal/lib/httpx"
	"github.com/niazlv/kinopub-downloader/internal/lib/logx"
	"github.com/niazlv/kinopub-downloader/internal/lib/ratelimit"
	"github.com/niazlv/kinopub-downloader/internal/lib/termuxapi"
	"github.com/niazlv/kinopub-downloader/internal/lib/termx"
	"github.com/niazlv/kinopub-downloader/internal/services/apiclient"
	"github.com/niazlv/kinopub-downloader/internal/services/apiscraper"
	"github.com/niazlv/kinopub-downloader/internal/services/downloader"
	"github.com/niazlv/kinopub-downloader/internal/services/feedparser"
	"github.com/niazlv/kinopub-downloader/internal/services/hlsdownloader"
	"github.com/niazlv/kinopub-downloader/internal/services/inputresolver"
	"github.com/niazlv/kinopub-downloader/internal/services/kinopubapp"
	"github.com/niazlv/kinopub-downloader/internal/services/mediaresolver"
	"github.com/niazlv/kinopub-downloader/internal/services/outputlayout"
	"github.com/niazlv/kinopub-downloader/internal/services/pagescraper"
	"github.com/niazlv/kinopub-downloader/internal/services/progress"
	"github.com/niazlv/kinopub-downloader/internal/services/proxyprovider"
	"github.com/niazlv/kinopub-downloader/internal/services/statestore"
)

// Build provenance, stamped by the release workflow through -ldflags. An
// ordinary `go build` leaves everything but version empty, which is how a
// development build is recognized — and why it is not allowed to update itself.
// See domain.BuildInfo.
var (
	version         = domain.DevVersion
	buildRepo       = ""
	buildRef        = ""
	buildCommit     = ""
	buildSigningKey = ""
)

// buildInfo assembles this binary's provenance.
func buildInfo() domain.BuildInfo {
	return domain.BuildInfo{
		Version:    version,
		Repo:       buildRepo,
		Ref:        buildRef,
		Commit:     buildCommit,
		SigningKey: buildSigningKey,
	}
}

// defaultUserAgent is a realistic Safari User-Agent used when the user supplies
// none. It serves two purposes: Cloudflare binds cf_clearance to the UA that
// solved the challenge (a mismatched UA is rejected with 403), and even without
// cookies Go's default "Go-http-client/1.1" looks suspicious to Cloudflare. The
// user can override it with --user-agent to match the browser that issued the
// cookies.
const defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.4 Safari/605.1.15"

func main() {
	// The color decision is made before anything can be printed: a bad flag,
	// a missing URL, or a subcommand with no flag set of its own all produce
	// output before any parse has happened.
	setColorMode(detectColorMode(os.Args[1:]))
	os.Exit(run())
}

func run() int {
	// Handle subcommands before flag parsing.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "login":
			return runLogin(os.Args[2:])
		case "logout":
			return runLogout(os.Args[2:])
		case "sessions":
			return runSessions(os.Args[2:])
		case "doctor":
			return runDoctor(os.Args[2:])
		case "completion":
			return runCompletion(os.Args[2:])
		case "update":
			return runUpdate(os.Args[2:])
		}
	}

	// Define flags.
	var (
		output      string
		concurrency int
		limitRate   string
		proxyURL    string
		quality     string
		verbosity   string
		ffmpegPath  string
		logFile     string
		container   string
		force       bool
		seasons     string
		episodes    string
		dryRun      bool
		showVersion bool
		cookie      string
		userAgent   string
		headerVals  headerList
		browserCk   browserCookiesFlag
		feedFile    string
		ffmpegArgs  string
		ffmpegX     ffmpegExtraList
		noChunked   bool
		audioSel    string
		audioMenu   bool
		subsSel     string
		subsMenu    bool
		subsExtern  bool
		subsOnly    bool
		videoMenu   bool
		interactive bool
		siteHost    string
		keepDomains bool
		appMode     bool
		appToken    string
		appBase     string
		appCodec    string
	)

	fs := flag.NewFlagSet("kinopub", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	fs.StringVar(&output, "output", "", "output directory path")
	fs.StringVar(&output, "o", "", "output directory path (shorthand)")
	fs.IntVar(&concurrency, "concurrency", 0, "max concurrent downloads (default: 2)")
	fs.IntVar(&concurrency, "c", 0, "max concurrent downloads (shorthand)")
	fs.StringVar(&limitRate, "limit-rate", "", "cap total download speed, e.g. 2M or 500k (default: unlimited)")
	fs.StringVar(&proxyURL, "proxy", "", "proxy URL (http, https, or socks5)")
	fs.StringVar(&quality, "quality", "", "quality preference (e.g. 1080p)")
	fs.StringVar(&quality, "q", "", "quality preference (shorthand)")
	var verbose bool
	fs.StringVar(&verbosity, "verbosity", "normal", "log verbosity: quiet, normal, verbose")
	fs.BoolVar(&verbose, "v", false, "enable verbose output")
	fs.StringVar(&ffmpegPath, "ffmpeg", "", "ffmpeg binary path (default: ffmpeg on PATH)")
	fs.StringVar(&logFile, "log-file", "", "log file path")
	fs.StringVar(&container, "container", "mkv", "output container: mkv or mp4")
	fs.BoolVar(&force, "force", false, "force re-download of completed episodes")
	fs.StringVar(&seasons, "seasons", "", "season selection (e.g. 1,3-5)")
	fs.StringVar(&episodes, "episodes", "", "episode selection (e.g. 1,3-5)")
	fs.BoolVar(&dryRun, "dry-run", false, "list episodes without downloading")
	fs.StringVar(&cookie, "cookie", "", "raw Cookie header value sent with every request (and to ffmpeg)")
	fs.StringVar(&userAgent, "user-agent", "", "User-Agent sent with every request (must match the browser that issued the cookies)")
	fs.Var(&headerVals, "header", "extra HTTP header 'Name: Value' (repeatable)")
	fs.Var(&browserCk, "browser-cookies", "auto-load site cookies from a browser: safari, chrome, firefox, or auto (default auto when given without a value)")
	fs.StringVar(&siteHost, "site", "", "site host to target, e.g. kino.watch (default: taken from the <url>, else "+domain.DefaultSiteHost+")")
	fs.BoolVar(&keepDomains, "no-domain-rewrite", false, "keep URLs exactly as given: do not rewrite the site's former domains (e.g. kino.pub) to the current one ("+domain.DefaultSiteHost+")")
	fs.StringVar(&feedFile, "feed-file", "", "read the RSS feed from a local file instead of fetching it over the network")
	fs.StringVar(&ffmpegArgs, "ffmpeg-args", "", "extra ffmpeg arguments as a single string (advanced, e.g. \"-c:v libx265 -crf 28\")")
	fs.Var(&ffmpegX, "x", "extra ffmpeg argument (repeatable, advanced, e.g. --x \"-c:v\" --x libx265)")
	fs.BoolVar(&noChunked, "no-chunked", false, "disable chunked HTTP download (use ffmpeg streaming for all sources)")
	fs.StringVar(&audioSel, "audio", "", "audio track selection: comma-separated patterns; prefix with '!' (or '-') to exclude (e.g. \"anilibria\", \"!jpn\", \"anilibria,!jpn\")")
	fs.BoolVar(&audioMenu, "audio-menu", false, "show an interactive audio-track picker before downloading (TTY only)")
	fs.StringVar(&subsSel, "subs", "", "subtitle track selection, same syntax as --audio (e.g. \"rus\", \"!eng\", \"rus,!eng\")")
	fs.BoolVar(&subsMenu, "subs-menu", false, "show an interactive subtitle-track picker before downloading (TTY only)")
	fs.BoolVar(&subsExtern, "subs-external", false, "write subtitles as separate .srt files instead of muxing them into the container")
	fs.BoolVar(&subsOnly, "subs-only", false, "download only subtitles as .srt files, skipping video and audio (page links only)")
	fs.BoolVar(&videoMenu, "video-menu", false, "show an interactive video-quality picker before downloading (TTY only)")
	fs.BoolVar(&interactive, "interactive", false, "pick quality, then audio, then subtitles interactively (implies --video-menu --audio-menu --subs-menu)")
	fs.BoolVar(&interactive, "i", false, "interactive mode (shorthand)")
	fs.BoolVar(&appMode, "app", false, "download using the installed kino.pub app's session (its API token) instead of website cookies — reuses the app's device slot, no new login")
	fs.StringVar(&appToken, "app-token", "", "app access token for --app (default: the token saved by `login --app`, else read from the installed app when run as root)")
	fs.StringVar(&appBase, "app-base", "", "override the kino.pub JSON API base URL for --app (default: "+kinopubapp.DefaultAPIBase+")")
	fs.StringVar(&appCodec, "app-codec", "", "preferred codec family for --app downloads: h264 (default) or h265")
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	registerColorFlags(fs)

	fs.Usage = func() {
		h := newHelpPrinter(os.Stderr, errStyle)
		h.title("kinopub", version, "download full-fidelity video from kino.watch (ex-kino.pub) and its mirrors")

		h.section("Usage:")
		h.commands(
			command{name: "kinopub [flags] <url>"},
			command{name: "kinopub login [flags]", desc: "save authentication credentials"},
			command{name: "kinopub logout [flags]", desc: "remove stored credentials"},
			command{name: "kinopub sessions [--check]", desc: "show stored login sessions"},
			command{name: "kinopub doctor [flags]", desc: "verify files and repair state"},
			command{name: "kinopub update [flags]", desc: "install the latest release"},
			command{name: "kinopub completion <shell>", desc: "generate a shell completion script (bash, fish)"},
		)

		h.section("The <url> can be:")
		h.bullet("A site page link:      ", "https://kino.watch/item/view/38290")
		h.bulletCont(len("A site page link:      "), "https://kino.watch/item/view/38290/s1e1")
		h.bullet("A podcast feed link:   ", "https://kino.watch/podcast/get/38290/TOKEN")
		h.bullet("A local RSS/XML file:  ", "./feed.xml")
		h.blank()
		h.text("Any host is accepted — the site is taken from the URL you pass, so mirrors and " +
			"future domain changes work as-is. Cookies, Referer and feed URLs all follow it. " +
			"Use --site only when there is no URL to derive the host from (e.g. --feed-file).")
		h.blank()
		h.text("URLs that still use a former domain of the site (kino.pub) are rewritten to the "+
			"current one (%s) automatically — both the URL you pass and links found inside feeds. "+
			"Pass --no-domain-rewrite to keep every URL exactly as given.", domain.DefaultSiteHost)
		h.blank()
		h.text("Page links are resolved automatically when credentials are available " +
			"(via login, --cookie, or --browser-cookies).")

		h.groupedFlags(fs, mainFlagGroups)

		h.section("Authentication:")
		h.line("  Two ways to authenticate — pick one:")
		h.blank()
		h.line("  %s the site is behind Cloudflare, so you need valid session cookies:", errStyle.Bold("A) Website cookies —"))
		h.step(1, "Log in to the site in your browser")
		h.step(2, "Copy cookies from DevTools (Network tab → request header → Cookie)")
		h.step(3, "Run: kinopub login --cookie \"paste_here\"")
		h.step(4, "Now just: kinopub https://kino.watch/item/view/38290")
		h.line("     On macOS with Full Disk Access granted to your terminal:")
		h.line("       %s", errStyle.Cyan("kinopub login --browser-cookies safari"))
		h.blank()
		h.line("  %s reuse the token of the installed kino.pub Android app", errStyle.Bold("B) Mobile app session (--app) —"))
		h.line("     (no new device slot, no browser). Reading the token needs root, which you")
		h.line("     grant yourself — the tool never elevates:")
		h.step(1, "Save it once under root:  su -c 'kinopub login --app'   (or: sudo kinopub login --app)")
		h.step(2, "Then just: kinopub --app https://kino.pub/item/view/126715")
		h.line("     No root? Pass a token you already have:")
		h.line("       %s", errStyle.Cyan("kinopub login --app --app-token <TOKEN>"))

		h.section("Examples:")
		h.example("Download a series (credentials from `kinopub login`)",
			"kinopub -o ./downloads https://kino.watch/item/view/38290")
		h.example("Download using a direct podcast feed link (no auth needed)",
			"kinopub -o ./downloads https://kino.watch/podcast/get/12345/token")
		h.example("List what would be downloaded without writing files",
			"kinopub --dry-run https://kino.watch/item/view/38290")
		h.example("Only seasons 1 and 3-5, 1080p, through a proxy",
			"kinopub --seasons 1,3-5 -q 1080p --proxy socks5://127.0.0.1:1080 <url>")
		h.example("Keep only the AniLibria dub, never the Japanese original",
			"kinopub --audio \"anilibria,!jpn\" https://kino.watch/item/view/38290")
		h.example("Pick the audio track interactively before downloading",
			"kinopub --audio-menu https://kino.watch/item/view/38290")
		h.example("Keep only Russian subtitles, as separate .srt files",
			"kinopub --subs rus --subs-external https://kino.watch/item/view/38290")
		h.example("Pick quality, audio and subtitles interactively, then download",
			"kinopub -i https://kino.watch/item/view/38290")
		h.example("A link to one episode narrows the run by itself",
			"kinopub https://kino.watch/item/view/38290/s1e1")
		h.example("Download nothing but the Russian subtitles",
			"kinopub --subs-only --subs rus https://kino.watch/item/view/38290")
		h.example("One-off with explicit cookies (without saving)",
			"kinopub --cookie \"cf_clearance=...; PHPSESSID=...\" <url>")
		h.example("Use the installed app's session (after `login --app`)",
			"kinopub --app https://kino.pub/item/view/126715")
		h.example("A mirror or a renamed domain — just pass its URL",
			"kinopub https://kino.example/item/view/38290")
		h.example("Use a locally saved feed file",
			"kinopub --feed-file ./feed.xml -o ./downloads")
	}

	if err := fs.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}

	// Support the space-separated form "--browser-cookies safari": because the
	// flag has an optional value, the browser name lands in the positional args.
	// If the flag was given bare and the first positional is a known browser
	// name, consume it as the flag's value.
	posArgs := fs.Args()
	if browserCk.set && browserCk.value == browsercookies.BrowserAuto && len(posArgs) > 0 {
		if isKnownBrowser(posArgs[0]) {
			browserCk.value = strings.ToLower(posArgs[0])
			posArgs = posArgs[1:]
		}
	}

	// --version
	if showVersion {
		// GPL-3.0 §5(d) asks an interactive program to point users at the
		// licence and state the absence of warranty.
		fmt.Printf("%s %s\n", outStyle.Bold("kinopub"), buildInfo().Describe())
		fmt.Printf("%s\n", outStyle.Gray("Copyright (C) 2026 niazlv"))
		fmt.Printf("%s\n", outStyle.Gray("License GPL-3.0-or-later: GNU GPL version 3 or later <https://gnu.org/licenses/gpl.html>"))
		fmt.Printf("%s\n", outStyle.Gray("This program comes with ABSOLUTELY NO WARRANTY."))
		fmt.Printf("%s\n", outStyle.Gray("This is free software: you are free to change and redistribute it."))
		return 0
	}

	// Validate the positional URL argument (Req 1.4).
	// Exactly one URL is required, unless a local --feed-file is supplied, in
	// which case the URL is optional (used only to derive the series id).
	args := posArgs
	if feedFile == "" {
		if len(args) != 1 {
			errorf("%s", domain.ErrExactlyOneURL.Error())
			fmt.Fprintln(os.Stderr)
			fs.Usage()
			return 1
		}
	} else if len(args) > 1 {
		errorf("at most one URL argument is allowed with --feed-file")
		fmt.Fprintln(os.Stderr)
		fs.Usage()
		return 1
	}
	var inputURL string
	if len(args) == 1 {
		inputURL = args[0]
	}

	// Auto-detect: if the positional argument is a path to an existing file
	// (not a URL), treat it as a local feed file. This lets the user simply
	// pass a downloaded .xml file without needing --feed-file explicitly.
	if inputURL != "" && feedFile == "" && !strings.Contains(inputURL, "://") {
		if info, err := os.Stat(inputURL); err == nil && !info.IsDir() {
			feedFile = inputURL
			inputURL = "" // no URL to resolve
		}
	}

	// Parse verbosity. The -v flag overrides --verbosity to "verbose".
	if verbose {
		verbosity = "verbose"
	}
	verb, err := parseVerbosity(verbosity)
	if err != nil {
		errorf("%v", err)
		return 1
	}

	// Parse container.
	cont, err := parseContainer(container)
	if err != nil {
		errorf("%v", err)
		return 1
	}

	// Parse season/episode selections.
	seasonSel, err := kinopub.ParseSelection(seasons)
	if err != nil {
		errorf("%v", err)
		return 1
	}
	episodeSel, err := kinopub.ParseSelection(episodes)
	if err != nil {
		errorf("%v", err)
		return 1
	}

	// Parse audio-track preference.
	audioPref, err := kinopub.ParseAudioPreference(audioSel)
	if err != nil {
		errorf("%v", err)
		return 1
	}

	// Parse subtitle-track preference.
	subsPref, err := kinopub.ParseSubtitlePreference(subsSel)
	if err != nil {
		errorf("%v", err)
		return 1
	}

	// Parse the bandwidth cap up front so a typo fails fast, before any network
	// work, rather than after resolving auth and scraping.
	rateLimit, err := ratelimit.ParseRate(limitRate)
	if err != nil {
		errorf("%v", err)
		return 1
	}

	// The site is whatever host the URL names, so mirrors and renamed domains
	// need no code change; --site names it when there is no URL to derive it
	// from (e.g. --feed-file) or to override it.
	site := domain.SiteFromURL(inputURL)
	if siteHost != "" {
		site = domain.SiteFromHost(siteHost)
	}

	// Old bookmarks and shared links outlive a domain the service has left
	// behind; a run against one would form every request URL with the dead
	// host. Move such a run onto the current domain, unless the user forbids it.
	if !keepDomains {
		inputURL, site = upgradeSiteDomain(inputURL, site)
	}

	// Resolve authentication. Two independent modes:
	//   - website: a Cloudflare-passing Cookie + matching User-Agent;
	//   - --api:   a mobile-app OAuth access token used as a Bearer, with the
	//              app's User-Agent so requests blend in. No cookie is involved.
	// With no method named on the command line, reach for whichever stored
	// credentials the user last logged in or downloaded with, so neither --app
	// nor --cookie has to be repeated on every run.
	if !appMode && feedFile == "" {
		if use, reason := chooseSavedAppSession(inputURL, cookie != "" || browserCk.set); use {
			appMode = true
			notef("using the saved kino.pub app session (%s). Pass --cookie or "+
				"`login --cookie` to use the website instead.", reason)
		}
	}

	var resolvedCookie string
	if !appMode {
		var fatal bool
		resolvedCookie, userAgent, fatal = resolveAuth(cookie, userAgent, browserCk, site, true)
		if fatal {
			return 1
		}
		// Having logged in with `login --app` is a statement of intent: with no
		// website cookie to fall back on, use that session rather than failing
		// with "authentication required" while a usable one sits in the store.
		// Only item links qualify — a podcast feed or --feed-file run has no
		// API equivalent.
		if resolvedCookie == "" && feedFile == "" && savedAppSessionApplies(inputURL) {
			appMode = true
			userAgent = "" // let the app session supply its own User-Agent
			notef("using the saved kino.pub app session (no website cookies found). " +
				"Pass --cookie or `login --cookie` to use the website instead.")
		}
	}

	// Build RunConfig.
	// Merge ffmpeg extra args: --ffmpeg-args (split by whitespace) + --x (individual).
	var extraFFmpegArgs []string
	if ffmpegArgs != "" {
		extraFFmpegArgs = append(extraFFmpegArgs, splitShellArgs(ffmpegArgs)...)
	}
	extraFFmpegArgs = append(extraFFmpegArgs, ffmpegX...)

	cfg := domain.RunConfig{
		InputURL:        inputURL,
		Site:            site,
		NoDomainRewrite: keepDomains,
		OutputPath:      output,
		MaxConcurrency:  concurrency,
		RateLimit:       rateLimit,
		ProxyURL:        proxyURL,
		Quality:         domain.Quality(quality),
		Verbosity:       verb,
		FFmpegPath:      ffmpegPath,
		LogFilePath:     logFile,
		Container:       cont,
		ForceRedownload: force,
		SeasonSel:       seasonSel,
		EpisodeSel:      episodeSel,
		DryRun:          dryRun,
		Cookie:          resolvedCookie,
		UserAgent:       userAgent,
		Headers:         headerVals.toMap(),
		BrowserCookies:  browserCk.value,
		FeedFile:        feedFile,
		FFmpegExtraArgs: extraFFmpegArgs,
		NoChunked:       noChunked,
		AudioPref:       audioPref,
		AudioMenu:       audioMenu,
		SubsPref:        subsPref,
		SubsMenu:        subsMenu,
		SubsExternal:    subsExtern,
		SubtitlesOnly:   subsOnly,
		VideoMenu:       videoMenu,
		AppMode:         appMode,
		AppToken:        appToken,
		APIBase:         appBase,
		AppCodec:        appCodec,
	}

	// A page link may already point at one episode ("…/item/view/38290/s1e1");
	// honour it as a filter unless the user gave an explicit selection.
	kinopub.ApplyURLEpisodeRef(&cfg, seasons != "", episodes != "")

	// -i is shorthand for the whole interactive flow: quality, then audio,
	// then subtitles, in the order they narrow a download.
	if interactive {
		cfg.VideoMenu = true
		cfg.AudioMenu = true
		cfg.SubsMenu = true
	}

	// Apply defaults and validate.
	kinopub.ApplyDefaults(&cfg)
	if err := kinopub.ValidateConfig(&cfg); err != nil {
		errorf("%v", err)
		return 1
	}

	// Check ffmpeg availability (Req 7.3). Skipped in dry-run mode since no
	// downloads are performed.
	if !cfg.DryRun {
		if _, err := exec.LookPath(cfg.FFmpegPath); err != nil {
			errorf("%s", domain.ErrFFmpegNotFound.Error())
			return 1
		}
	}

	// Set up a signal-driven context for graceful shutdown. NotifyContext
	// cancels ctx on the first SIGINT/SIGTERM; stop() restores default handling.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// In --api mode, resolve the access token and the app-matching User-Agent
	// (reading the installed app when allowed), validate the token, and warn on
	// any drift from the built-in baseline. Failure here is terminal: there is
	// no cookie fallback in API mode.
	if cfg.AppMode {
		if code := prepareAppSession(ctx, &cfg); code != 0 {
			return code
		}
	}

	// Wire up services.
	deps, cleanup, err := buildDependencies(cfg)
	if err != nil {
		errorf("%v", err)
		return 1
	}
	defer cleanup()

	// Create app and run.
	app, err := kinopub.New(deps)
	if err != nil {
		errorf("%v", err)
		return 1
	}

	res, runErr := app.Run(ctx, cfg)
	if runErr != nil {
		// A token that expires mid-run surfaces here as ErrAPIUnauthorized;
		// translate it to the same actionable guidance the up-front check gives,
		// rather than the raw "API rejected the access token".
		if errors.Is(runErr, domain.ErrAPIUnauthorized) {
			reportTokenExpired()
			return 1
		}
		errorf("%v", runErr)
		return 1
	}

	// Remember which credentials worked, so a later run with no flags reaches
	// for the same ones first.
	switch {
	case cfg.AppMode:
		recordAuthMethodUsed(credstore.MethodApp)
	case resolvedCookie != "":
		recordAuthMethodUsed(credstore.MethodCookie)
	}

	return exitCodeFor(res, ctx.Err(), cfg.DryRun)
}

// exitCodeFor maps a finished run to a process exit status. The engine reports
// per-episode failures in the result rather than as an error, so a run where
// every episode failed would otherwise look successful to cron jobs and shell
// scripts that only check the status.
//
// dryRun says the run was only ever meant to list episodes. Such a run downloads
// nothing by definition, which is success rather than the "nothing came through"
// failure below — otherwise `kinopub --dry-run … && …` could never proceed.
func exitCodeFor(res domain.RunResult, ctxErr error, dryRun bool) int {
	switch {
	case ctxErr != nil:
		fmt.Fprintf(os.Stderr, "%s %d of %d episodes completed\n",
			errStyle.Yellow("Interrupted:"), res.Succeeded, res.Total)
		return 130 // conventional status for termination by SIGINT
	case res.Failed > 0:
		fmt.Fprintf(os.Stderr, "%s %s succeeded, %s failed, %s skipped\n",
			errStyle.BoldRed("Finished with errors:"),
			errStyle.Green(fmt.Sprintf("%d", res.Succeeded)),
			errStyle.Red(fmt.Sprintf("%d", res.Failed)),
			errStyle.Yellow(fmt.Sprintf("%d", res.Skipped)))
		return 1
	case dryRun:
		return 0
	case res.Succeeded == 0 && res.Total > 0:
		// Nothing failed outright, yet nothing was downloaded either — e.g. every
		// episode was skipped after repeated media-resolution failures.
		fmt.Fprintf(os.Stderr, "%s (%d skipped of %d)\n",
			errStyle.Yellow("No episodes were downloaded"), res.Skipped, res.Total)
		return 1
	default:
		return 0
	}
}

// buildDependencies constructs all real service implementations and returns
// the Dependencies struct, a cleanup function, and any error.
func buildDependencies(cfg domain.RunConfig) (kinopub.Dependencies, func(), error) {
	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	// Coordinator for TTY line-discipline.
	coord := logx.NewCoordinator(os.Stderr)

	// Build logger handlers.
	handlers := buildLogHandlers(cfg, coord)

	// Open log file if configured.
	if cfg.LogFilePath != "" {
		// Owner-only: the log records request URLs and headers, which carry feed
		// tokens and signed media links.
		f, err := os.OpenFile(cfg.LogFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return kinopub.Dependencies{}, cleanup, fmt.Errorf("cannot open log file: %w", err)
		}
		cleanups = append(cleanups, func() { f.Close() })
		handlers = append(handlers, logx.NewFileHandler(f, nil))
	}

	logger := logx.New(handlers)

	// Proxy provider.
	proxyProv, err := proxyprovider.New(cfg.ProxyURL)
	if err != nil {
		return kinopub.Dependencies{}, cleanup, err
	}

	// Build the auth-aware HTTP client: wrap the proxy client so every request
	// carries the configured Cookie / User-Agent / extra headers.
	auth := domain.RequestAuth{
		Cookie:    cfg.Cookie,
		UserAgent: cfg.UserAgent,
		Headers:   cfg.Headers,
		Site:      cfg.Site,
	}
	// Always include a Referer pointing at the site — the CDN (digital-cdn.net)
	// requires it and will hang/timeout without it.
	if auth.Headers == nil {
		auth.Headers = make(map[string]string)
	}
	if auth.Headers["Referer"] == "" {
		auth.Headers["Referer"] = cfg.Site.Referer()
	}
	httpClient := httpx.WithAuth(proxyProv.HTTPClient(), auth)

	// Input resolver — with page scraper when auth is available.
	var resolverOpts []inputresolver.Option
	if auth.HasCredentials() {
		scraper := pagescraper.New(httpClient, logger)
		resolverOpts = append(resolverOpts, inputresolver.WithPageScraper(scraper))
	}
	inputRes := inputresolver.New(logger, resolverOpts...)

	// Feed parser. Links inside the feed that still name a former site domain
	// are moved onto the current one, unless --no-domain-rewrite says otherwise.
	feedPars := feedparser.New(httpClient, logger,
		feedparser.WithDomainRewrite(!cfg.NoDomainRewrite))

	// Media resolver — needs a RunOutput function for ffprobe. Its m3u8 fetches
	// go straight to the CDN, which gates HLS on cf_clearance, so this client
	// carries the cookie everywhere rather than only to the site.
	mediaRes := mediaresolver.New(
		httpx.WithAuthCDNCookies(proxyProv.HTTPClient(), auth),
		makeRunOutput(),
		logger,
		auth,
	)

	// Output layout.
	layout := outputlayout.New(cfg.Container)

	// State store.
	outputDir := cfg.OutputPath
	if outputDir == "" {
		outputDir, _ = os.Getwd()
	}
	stateStr := statestore.New(outputDir, logger)

	// Downloader.
	dl := downloader.New(
		makeRunFunc(),
		proxyProv,
		logger,
		downloader.WithFFmpegPath(cfg.FFmpegPath),
		downloader.WithAuth(auth),
		downloader.WithExtraArgs(cfg.FFmpegExtraArgs),
		downloader.WithNoChunked(cfg.NoChunked),
		downloader.WithHTTPClient(httpClient),
	)

	// Progress reporter — choose live or log based on TTY.
	var progReporter domain.ProgressReporter
	if termx.IsTTY(os.Stderr) {
		progReporter = progress.NewLive(os.Stderr, coord, progress.WithColor(errStyle.Enabled()))
	} else {
		progReporter = progress.NewLog(logger)
	}
	// Wrap with Termux notifications if termux-notification is available.
	progReporter = termuxapi.Wrap(progReporter, termuxapi.WithLogger(logger))
	// And with native desktop notifications on macOS/Linux (no-op elsewhere,
	// and on Termux, which the richer Termux notifier above already covers).
	progReporter = desktopnotify.Wrap(progReporter)

	deps := kinopub.Dependencies{
		Logger:           logger,
		InputResolver:    inputRes,
		FeedParser:       feedPars,
		MediaResolver:    mediaRes,
		Downloader:       dl,
		ProxyProvider:    proxyProv,
		ProgressReporter: progReporter,
		StateStore:       stateStr,
		OutputLayout:     layout,
		AuthedHTTPClient: httpClient,
	}

	// Optional HLS pipeline. Two ways to feed it:
	//   - --api: the JSON API backend, which fetches items with a Bearer token
	//     and yields signed hls4 manifests. No cookie needed; the token and the
	//     app User-Agent were resolved into cfg by prepareAppSession.
	//   - website: the HTML page scraper, available only when a cookie is
	//     present (the player page is behind Cloudflare + auth).
	switch {
	case cfg.AppMode:
		apiCli := apiclient.New(proxyProv.HTTPClient(), cfg.APIBase, cfg.AppToken,
			apiclient.WithUserAgent(cfg.UserAgent), apiclient.WithLogger(logger))
		deps.PageScraper = apiscraper.New(apiCli, logger,
			apiscraper.WithPreferredCodec(cfg.AppCodec))
		deps.HLSDownloader = hlsdownloader.New(httpClient, auth, logger,
			hlsdownloader.WithConcurrency(cfg.MaxConcurrency),
			hlsdownloader.WithProxy(proxyProv.ProxyURL()),
			hlsdownloader.WithRateLimit(cfg.RateLimit))
	case auth.HasCredentials():
		scraper := pagescraper.New(httpClient, logger)
		hlsDl := hlsdownloader.New(httpClient, auth, logger,
			hlsdownloader.WithConcurrency(cfg.MaxConcurrency),
			hlsdownloader.WithProxy(proxyProv.ProxyURL()),
			hlsdownloader.WithRateLimit(cfg.RateLimit))
		deps.PageScraper = scraper
		deps.HLSDownloader = hlsDl
	}

	// Interactive audio-track picker. Only meaningful when the menu is enabled
	// and stdin/stderr are a real terminal.
	if cfg.AudioMenu && termx.IsTTY(os.Stdin) && termx.IsTTY(os.Stderr) {
		deps.AudioChooser = audiomenu.New(os.Stdin, os.Stderr, true, audiomenu.WithColor(errStyle.Enabled()))
	}

	// Interactive subtitle-track picker, on the same terms as the audio one.
	if cfg.SubsMenu && termx.IsTTY(os.Stdin) && termx.IsTTY(os.Stderr) {
		deps.SubtitleChooser = audiomenu.New(os.Stdin, os.Stderr, true, audiomenu.WithColor(errStyle.Enabled()))
	}

	// Interactive video-quality picker, likewise.
	if cfg.VideoMenu && termx.IsTTY(os.Stdin) && termx.IsTTY(os.Stderr) {
		deps.VideoChooser = audiomenu.New(os.Stdin, os.Stderr, true, audiomenu.WithColor(errStyle.Enabled()))
	}

	return deps, cleanup, nil
}

// buildLogHandlers creates the console log handler based on TTY detection and verbosity.
func buildLogHandlers(cfg domain.RunConfig, coord *logx.Coordinator) []logx.Handler {
	return consoleHandlers(cfg.Verbosity, coord)
}

// consoleHandlers picks the stderr log handler every subcommand shares: the
// compact, colored layout for a terminal, the timestamped plain one for a file
// or a pipe. --color=always keeps the compact layout in a pipe, because asking
// for color is asking for the terminal presentation.
func consoleHandlers(verb domain.Verbosity, coord *logx.Coordinator) []logx.Handler {
	if termx.IsTTY(os.Stderr) || errStyle.Enabled() {
		return []logx.Handler{logx.NewConsoleHandler(os.Stderr, verb, coord, errStyle)}
	}
	return []logx.Handler{logx.NewPlainHandler(os.Stderr, verb, coord)}
}

// makeRunOutput creates a RunOutputFunc that executes a command and captures stdout.
// On failure, stderr is included in the error message for diagnostics.
func makeRunOutput() mediaresolver.RunOutputFunc {
	return func(ctx context.Context, name string, args, env []string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		if len(env) > 0 {
			cmd.Env = append(os.Environ(), env...)
		}
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			errMsg := stderr.String()
			if errMsg != "" {
				return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(errMsg))
			}
			return nil, err
		}
		return out, nil
	}
}

// makeRunFunc creates a downloader.RunFunc that executes a command, streaming
// stdout to the provided writer. ffmpeg stderr is discarded in interactive mode
// to keep the progress display clean — all useful progress info comes via
// -progress pipe:1 on stdout.
func makeRunFunc() downloader.RunFunc {
	return func(ctx context.Context, name string, args, env []string, stdout io.Writer) error {
		cmd := exec.CommandContext(ctx, name, args...)
		if len(env) > 0 {
			cmd.Env = append(os.Environ(), env...)
		}
		cmd.Stdout = stdout
		// Discard ffmpeg stderr: the verbose codec/stream info clutters the
		// live progress display. Errors are detected via the exit code.
		cmd.Stderr = io.Discard
		return cmd.Run()
	}
}
