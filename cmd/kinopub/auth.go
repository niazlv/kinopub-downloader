// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/browsercookies"
	"github.com/niazlv/kinopub-downloader/internal/lib/credstore"
	"github.com/niazlv/kinopub-downloader/internal/services/kinopubapp"
)

// resolveAuth resolves the Cookie header and User-Agent for an outbound
// session, following the precedence: explicit --cookie > --browser-cookies >
// stored credentials from `kinopub login`, with defaultUserAgent as the final
// UA fallback. A failed browser load is fatal when browserLoadFatal is set (the
// download path cannot proceed without the requested cookies); otherwise it
// degrades to a warning (doctor can still run read-only checks). The returned
// fatal flag tells the caller to abort.
// platform marks a platform page rather than kino.pub or a mirror of it; it
// keeps a browser cookie lookup to that site alone (see browserCookieDomains).
func resolveAuth(cookie, userAgent string, browserCk browserCookiesFlag, site domain.Site, platform, browserLoadFatal bool) (resolvedCookie, resolvedUA string, fatal bool) {
	resolvedCookie = cookie
	resolvedUA = userAgent

	if resolvedCookie == "" && browserCk.set {
		ck, ckDomain, err := browsercookies.Load(browserCk.value, browserCookieDomains(site, platform)...)
		if err != nil {
			if browserLoadFatal {
				errorf("could not load cookies from browser %q: %v", browserCk.value, err)
				return "", resolvedUA, true
			}
			warnf("could not load cookies from browser %q: %v", browserCk.value, err)
		} else {
			resolvedCookie = ck
			warnCookieDomainMismatch(ckDomain, site)
		}
	}

	// Fall back to stored credentials if nothing was provided explicitly. They
	// are only replayed against the site they were saved for: the target site
	// comes from the URL the user passed, so an unrelated host — a link shared
	// as a "mirror" — must never receive the saved session.
	if resolvedCookie == "" {
		stored, err := credstore.Load()
		switch {
		case err != nil:
			warnf("could not load stored credentials: %v", err)
		case !stored.HasCookie():
			// No website login saved; run anonymously. An app-token-only
			// login lands here: its User-Agent belongs to the Android client
			// and must not be attached to website requests.
		default:
			// Logins are held per site, and only the one saved for the site
			// this run targets may travel: the target host is whatever the
			// user-supplied URL names, so a link naming an unrelated host — a
			// "mirror", say — must never receive another site's session.
			s, _, ok := stored.SessionFor(site)
			if !ok {
				warnStoredCredentialsWithheld(stored.SiteHosts(), site)
				break
			}
			resolvedCookie = s.Cookie
			if resolvedUA == "" && s.UserAgent != "" {
				resolvedUA = s.UserAgent
			}
		}
	}

	if resolvedUA == "" {
		resolvedUA = defaultUserAgent
	}
	return resolvedCookie, resolvedUA, false
}

// upgradeSiteDomain moves a run that targets a former domain of the service
// (kino.pub) onto the newest known one, rewriting the input URL to match. It
// says so on stderr, because the run will visibly talk to a different host
// than the one the user named; --no-domain-rewrite skips the call entirely.
// Mirrors and the current domain pass through unchanged.
func upgradeSiteDomain(inputURL string, site domain.Site) (string, domain.Site) {
	upgraded, ok := site.Upgraded()
	if !ok {
		return inputURL, site
	}
	notef("%s is a former domain of the site — targeting %s instead. "+
		"Pass --no-domain-rewrite to keep the original.", site, upgraded)
	if rewritten, changed := upgraded.RewriteURL(inputURL); changed {
		inputURL = rewritten
	}
	return inputURL, upgraded
}

// cookieDomains lists the domains to look for browser cookies under, most
// preferred first: the site this run targets, then the other domains the
// service is known by — so a session saved before a rename (kino.pub →
// kino.watch) is still found instead of failing outright.
func cookieDomains(site domain.Site) []string {
	domains := []string{site.String()}
	for _, known := range domain.KnownSiteHosts {
		if known != domains[0] {
			domains = append(domains, known)
		}
	}
	return domains
}

// browserCookieDomains is cookieDomains for a run, narrowed to the site itself
// when that site is a platform: a kino.pub session found under a former domain
// is no use there and would only produce a misleading "loaded cookies for
// kino.watch" warning ahead of the real error.
func browserCookieDomains(site domain.Site, platform bool) []string {
	if platform {
		return []string{site.String()}
	}
	return cookieDomains(site)
}

// reportPlatformSessionRequired explains what a platform page takes. The
// stored kino.pub session was rightly withheld from it, and the fix is not a
// kino.pub login but the platform's own cookie — from the browser the user is
// logged in with, once or per run.
func reportPlatformSessionRequired(site domain.Site, cause error) {
	host := site.String()
	// Lines are kept short: errorf wraps to the terminal, and a command
	// broken across two lines is not one the user can copy.
	errorf("%v.\n"+
		"  %s is a platform page: it takes your session there, not a kino.pub one.\n"+
		"  Once, by QR — a session of this tool's own that renews itself:\n"+
		"    kinopub login --qr --site %s\n"+
		"  Or from your browser, per run:  kinopub --browser-cookies <url>",
		cause, host, host)
}

// warnCookieDomainMismatch tells the user when the cookies we found belong to a
// different domain than the one being downloaded from. They are still used —
// mirrors often share a session — but a 403 later is then explained.
func warnCookieDomainMismatch(cookieDomain string, site domain.Site) {
	if cookieDomain == "" || site.Owns(cookieDomain) {
		return
	}
	warnf("loaded cookies for %s, but this run targets %s. "+
		"If it returns 403, log in to %s in your browser and retry.",
		cookieDomain, site, site)
}

// warnStoredCredentialsWithheld explains why no saved login is being sent.
// Unlike a browser-cookie domain mismatch, which is only advisory, this one
// withholds the cookie — so say so, and say how to get one that fits. The run
// continues anonymously, which is enough for public pages.
func warnStoredCredentialsWithheld(storedSites []string, site domain.Site) {
	warnf("saved logins are for %s; none for %s — a stored session is only "+
		"used on the site it was created for. Continuing without them. "+
		"To use credentials here, run `kinopub login --browser-cookies --site %s` "+
		"(it is kept beside the others), or pass --cookie explicitly.",
		strings.Join(storedSites, ", "), site, site)
}

// otherSites lists the sites with a login other than the one just saved.
func otherSites(creds credstore.Credentials, saved string) []string {
	var out []string
	for _, s := range creds.Sessions() {
		if s.Site != credstore.SiteKey(saved) {
			out = append(out, s.Site)
		}
	}
	return out
}

// runLogin saves authentication credentials encrypted to disk.
// Usage: kinopub login --cookie "..." [--user-agent "..."]
//
//	kinopub login --browser-cookies [safari|chrome|firefox|auto]
func runLogin(args []string) int {
	fs := flag.NewFlagSet("kinopub login", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		cookie    string
		userAgent string
		browserCk browserCookiesFlag
		siteHost  string
		appMode   bool
		qrMode    bool
		appToken  string
		appBase   string
		clientSec string
		proxyURL  string
	)

	fs.StringVar(&cookie, "cookie", "", "raw Cookie header value to store")
	fs.StringVar(&userAgent, "user-agent", "", "User-Agent to store (browser UA for cookies; the app's UA for --app)")
	fs.Var(&browserCk, "browser-cookies", "auto-load cookies from a browser: safari, chrome, firefox, or auto")
	fs.StringVar(&siteHost, "site", "", "site host: whose cookies to read, or with --qr the platform to authorize this tool on (default: "+strings.Join(domain.KnownSiteHosts, ", then ")+")")
	fs.BoolVar(&appMode, "app", false, "save the installed kino.pub app's session (its API token) instead of a website cookie — reuses the app's device slot")
	fs.StringVar(&appToken, "app-token", "", "app access token to save for --app (default: read from the installed app when run as root)")
	fs.BoolVar(&qrMode, "qr", false, "authorize this tool with kino.pub by QR/device code — its own session, works without root or Android, and renews itself")
	fs.StringVar(&appBase, "app-base", "", "override the kino.pub JSON API base URL for --app (default: "+kinopubapp.DefaultAPIBase+")")
	fs.StringVar(&clientSec, "client-secret", "", "OAuth client secret for --qr (default: from a stored session, or the installed app)")
	fs.StringVar(&proxyURL, "proxy", "", "proxy URL used to validate the --app token (http, https, socks5)")
	registerColorFlags(fs)

	fs.Usage = func() {
		h := newHelpPrinter(os.Stderr, errStyle)
		h.text("Save authentication (encrypted, machine-bound). Two independent methods:")

		h.section("Website cookies (Cloudflare session):")
		h.commands(
			command{name: "kinopub login --cookie \"cf_clearance=...; _identity=...\" --user-agent \"Mozilla/5.0 ...\""},
			command{name: "kinopub login --browser-cookies safari"},
			command{name: "kinopub login --browser-cookies --site kino.example", desc: "another site's login, kept beside the kino.pub one"},
		)
		h.blank()
		h.text("Website logins are held per site: saving one never replaces another. " +
			"A run sends the login of the site its URL names, and nothing else.")

		h.section("Platform session of its own, by QR / code:")
		h.commands(
			command{name: "kinopub login --qr --site kino.example", desc: "approve this tool on the platform's site; renews itself"},
		)
		h.blank()
		h.text("A platform built on this tool issues the tool a session of its own: no browser " +
			"cookies to expire, and it renews itself for as long as it is used.")

		h.section("kino.pub mobile app session (reuses the app's device slot):")
		h.commands(
			command{name: "kinopub login --app", desc: "read the token from the installed app (must run as root)"},
			command{name: "kinopub login --app --app-token <TOKEN>", desc: "save a token you already have (no root)"},
		)
		h.blank()
		h.text("--app takes the access token the installed kino.pub Android app already holds, " +
			"so no new device slot is claimed. Reading it from the app needs the process to be " +
			"root — the tool never elevates itself, so run it under root yourself:")
		h.line("    %s", errStyle.Cyan("su -c 'kinopub login --app'      # Android / Termux"))
		h.line("    %s", errStyle.Cyan("sudo kinopub login --app         # Linux / desktop"))
		h.blank()
		h.text("Then download without root or flags:  %s", errStyle.Cyan("kinopub --app https://kino.pub/item/view/126715"))

		h.section("kino.pub session of its own, by QR / device code:")
		h.commands(
			command{name: "kinopub login --qr", desc: "scan a QR (or type a code) to authorize this tool"},
		)
		h.blank()
		h.text("--qr asks kino.pub to authorize kinopub itself, so it needs no root, no Android " +
			"and no installed app: it is the way to use the API mode on a desktop. The session " +
			"belongs to this tool, which is also why it is the only one refreshed automatically " +
			"— an --app session is the phone's, and rotating its token would sign the app out.")
		h.blank()
		h.text("It does need the app's OAuth client secret once (kino.pub publishes no public "+
			"client). `login --app` on a rooted phone stores it; move it across with %s.",
			errStyle.Cyan("sessions export/import"))

		h.blank()
		h.text("Credentials are stored encrypted at ~/.config/kinopub/credentials.enc " +
			"and can only be decrypted on this machine.")

		h.section("Flags:")
		h.flags(fs)
	}

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}

	if appMode && qrMode {
		errorf("--app and --qr are different ways to obtain a session; pick one.")
		return 1
	}

	// The API-session methods are separate flows: resolve, validate and save a
	// token, then stop. They share no state with the cookie path.
	if appMode || qrMode {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if qrMode {
			// With a site named, the code is the platform's, not kino.pub's:
			// its own session for this tool, renewed by itself.
			if siteHost != "" {
				return loginPlatformQR(ctx, siteHost, proxyURL, userAgent)
			}
			return loginQR(ctx, appBase, proxyURL, clientSec, userAgent)
		}
		return loginApp(ctx, appToken, userAgent, appBase, proxyURL)
	}

	// Handle --browser-cookies consuming the next positional arg.
	posArgs := fs.Args()
	if browserCk.set && browserCk.value == browsercookies.BrowserAuto && len(posArgs) > 0 {
		if isKnownBrowser(posArgs[0]) {
			browserCk.value = strings.ToLower(posArgs[0])
		}
	}

	// Resolve cookie. With no --site, every domain the service is known by is
	// searched, so the user does not have to know which one they are logged in to.
	// An explicit --site is searched alone: it names a different site — a
	// platform, a mirror — and a kino.pub cookie found in its stead would be
	// saved under the wrong name and sent to the wrong host.
	site := domain.SiteFromHost(siteHost)
	resolvedCookie := cookie
	if resolvedCookie == "" && browserCk.set {
		domains := cookieDomains(site)
		if siteHost != "" {
			domains = []string{site.String()}
		}
		ck, ckDomain, err := browsercookies.Load(browserCk.value, domains...)
		if err != nil {
			errorf("could not load cookies from browser %q: %v", browserCk.value, err)
			return 1
		}
		resolvedCookie = ck
		fmt.Fprintf(os.Stderr, "Loaded cookies for %s from browser %q.\n",
			errStyle.Cyan(ckDomain), browserCk.value)
		// Without --site the search covers every domain the service is known by,
		// so the domain the cookies actually came from is the one they belong to.
		if siteHost == "" && ckDomain != "" {
			site = domain.SiteFromHost(ckDomain)
		}
	}

	if resolvedCookie == "" {
		errorf("no cookies provided. Use --cookie or --browser-cookies.")
		fs.Usage()
		return 1
	}

	// Default UA.
	if userAgent == "" {
		userAgent = defaultUserAgent
	}

	// Bind the login to the site it was obtained for: later runs send it to
	// that site only, never to some other host a URL happens to name.
	//
	// Only this site's login is replaced. Logins are held per site, and the
	// app session is independent of all of them, so saving one must not throw
	// away another stored earlier — a user holds several and switches between
	// them.
	creds, err := credstore.Load()
	if err != nil {
		creds = credstore.Credentials{}
	}
	creds.SetSession(site.String(), credstore.SiteSession{
		Cookie: resolvedCookie, UserAgent: userAgent, SavedAt: time.Now(),
	})

	if err := credstore.Save(creds); err != nil {
		errorf("%v", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "%s Credentials for %s saved (encrypted, machine-bound) to ~/.config/kinopub/credentials.enc\n",
		errStyle.Green("✓"), errStyle.Cyan(site.String()))
	if others := otherSites(creds, site.String()); len(others) > 0 {
		notef("logins for %s are kept: website logins are held per site.", strings.Join(others, ", "))
	}
	return 0
}

// runLogout removes stored credentials.
// runLogout removes stored credentials. With no flag it removes everything;
// --app or --cookie removes only that method, leaving the other in place — the
// two are independent sessions and a user may hold both.
func runLogout(args []string) int {
	fs := flag.NewFlagSet("kinopub logout", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var appOnly, cookieOnly bool
	var siteHost string
	fs.BoolVar(&appOnly, "app", false, "remove only the kino.pub app session, keep the website logins")
	fs.BoolVar(&cookieOnly, "cookie", false, "remove only a website login, keep the app session")
	fs.StringVar(&siteHost, "site", "", "with --cookie: the site whose login to remove (required when several are stored)")
	registerColorFlags(fs)
	fs.Usage = func() {
		h := newHelpPrinter(os.Stderr, errStyle)
		h.text("Remove stored credentials.")
		h.section("Usage:")
		h.commands(
			command{name: "kinopub logout", desc: "remove everything"},
			command{name: "kinopub logout --app", desc: "remove only the app session"},
			command{name: "kinopub logout --cookie", desc: "remove the only website login"},
			command{name: "kinopub logout --cookie --site kino.example", desc: "remove one site's login, keep the rest"},
		)
		h.section("Flags:")
		h.flags(fs)
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}

	if appOnly && cookieOnly {
		errorf("--app and --cookie together remove everything; pass neither for that.")
		return 1
	}

	// Whole-store removal is the simple, explicit case.
	if !appOnly && !cookieOnly {
		if err := credstore.Clear(); err != nil {
			errorf("%v", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "%s All stored credentials removed.\n", errStyle.Green("✓"))
		return 0
	}

	creds, err := credstore.Load()
	if err != nil {
		errorf("could not read stored credentials: %v", err)
		return 1
	}

	var removed string
	switch {
	case appOnly:
		if !creds.HasAppToken() {
			fmt.Fprintf(os.Stderr, "No kino.pub app session was stored.\n")
			return 0
		}
		creds.AppToken, creds.AppUserAgent, creds.APIBase = "", "", ""
		creds.AppSavedAt = time.Time{}
		removed = "kino.pub app session"
	case cookieOnly:
		sites := creds.SiteHosts()
		if len(sites) == 0 {
			fmt.Fprintf(os.Stderr, "No website login was stored.\n")
			return 0
		}
		// Logins are held per site: with several stored, "the website login"
		// names nothing in particular, so the site has to be said.
		target := siteHost
		if target == "" && len(sites) == 1 {
			target = sites[0]
		}
		if target == "" {
			errorf("several website logins are stored (%s); pass --site to say which one to remove.",
				strings.Join(sites, ", "))
			return 1
		}
		if !creds.RemoveSession(target) {
			errorf("no website login is stored for %s (stored: %s).", target, strings.Join(sites, ", "))
			return 1
		}
		removed = "website login for " + credstore.SiteKey(target)
	}
	if creds.LastUsed == credstore.MethodApp && appOnly ||
		creds.LastUsed == credstore.MethodCookie && cookieOnly && !creds.HasCookie() {
		creds.LastUsed, creds.LastUsedAt = "", time.Time{}
	}

	// If nothing is left, drop the file entirely rather than storing an empty
	// encrypted blob.
	if creds.IsEmpty() {
		if err := credstore.Clear(); err != nil {
			errorf("%v", err)
			return 1
		}
	} else if err := credstore.Save(creds); err != nil {
		errorf("%v", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "%s Removed the %s.\n", errStyle.Green("✓"), removed)
	return 0
}

// runSessions prints what is stored and which method a bare run would use. With
// --check it also validates the app token against the API, showing the account.
func runSessions(args []string) int {
	// Moving a session between machines is a separate job from inspecting one,
	// so it gets its own subcommand with its own flags.
	if len(args) > 0 {
		switch args[0] {
		case "export":
			return runSessionsExport(args[1:])
		case "import":
			return runSessionsImport(args[1:])
		}
	}

	fs := flag.NewFlagSet("kinopub sessions", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var check bool
	var proxyURL string
	fs.BoolVar(&check, "check", false, "validate the app token against the API and show the account")
	fs.StringVar(&proxyURL, "proxy", "", "proxy URL for --check (http, https, socks5)")
	registerColorFlags(fs)
	fs.Usage = func() {
		h := newHelpPrinter(os.Stderr, errStyle)
		h.text("Show stored login sessions and which one a bare run would use.")
		h.section("Usage:")
		h.commands(
			command{name: "kinopub sessions"},
			command{name: "kinopub sessions --check", desc: "also verify the app token online"},
			command{name: "kinopub sessions export --out FILE", desc: "write a portable copy"},
			command{name: "kinopub sessions import FILE", desc: "load one written elsewhere"},
		)
		h.section("Flags:")
		h.flags(fs)
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}

	creds, err := credstore.Load()
	if err != nil {
		errorf("could not read stored credentials: %v", err)
		return 1
	}
	if creds.IsEmpty() {
		fmt.Fprintf(os.Stderr, "No sessions stored. Log in with `%s login --cookie …` or `%s login --app`.\n",
			os.Args[0], os.Args[0])
		return 0
	}

	preferred := creds.PreferredMethod()
	fmt.Fprintf(os.Stderr, "Stored sessions (a bare `%s <url>` uses the %s):\n\n", os.Args[0], sessionLabel(preferred))

	// Website logins, one per site. The arrow marks what a bare kino.pub run
	// would use; a link to another site always uses that site's own login.
	_, preferredSite, _ := creds.SessionFor(domain.Site{})
	for _, s := range creds.Sessions() {
		mark := "  "
		if preferred == credstore.MethodCookie && s.Site == preferredSite {
			mark = errStyle.Green("→ ")
		}
		kind := "website  · site " + errStyle.Cyan(s.Site)
		if s.Renewable() {
			// A device session is the platform's own, and the practically
			// important fact about it is that it renews itself.
			kind = "device   · site " + errStyle.Cyan(s.Site) + " · renews itself"
			if !s.ExpiresAt.IsZero() {
				kind += " · token until " + s.ExpiresAt.Local().Format("2006-01-02")
			}
		}
		fmt.Fprintf(os.Stderr, "%s%s%s\n", mark, kind, savedSuffix(s.SavedAt))
	}

	// App session.
	if creds.HasAppToken() {
		mark := "  "
		if preferred == credstore.MethodApp {
			mark = errStyle.Green("→ ")
		}
		base := creds.APIBase
		if base == "" {
			base = "(default)"
		}
		fmt.Fprintf(os.Stderr, "%s%-8s · API %s%s\n",
			mark, sessionKindLabel(creds), errStyle.Cyan(base), savedSuffix(creds.AppSavedAt))

		// Whether the session renews itself is the practically important fact
		// about it, so it is stated rather than left to be inferred.
		if creds.CanRefresh() {
			fmt.Fprintf(os.Stderr, "           renews itself automatically%s\n", expirySuffix(creds.AppTokenExpiresAt))
		} else {
			fmt.Fprintf(os.Stderr, "           not renewable here — re-run `%s login --app` (or `login --qr`) when it expires\n",
				os.Args[0])
		}

		if check {
			ctx := context.Background()
			user, code := validateAppToken(ctx, proxyURL, creds.APIBase, creds.AppToken, creds.AppUserAgent)
			if code == 0 {
				_ = user // validateAppToken already prints the account line
			}
		} else {
			fmt.Fprintf(os.Stderr, "           run with --check to verify the token is still valid\n")
		}
	}
	return 0
}

// sessionLabel renders a preferred-method value for a sentence.
func sessionLabel(method string) string {
	switch method {
	case credstore.MethodApp:
		return "app session"
	case credstore.MethodCookie:
		return "website login"
	default:
		return "stored session"
	}
}

// savedSuffix renders " · saved <date>" when a timestamp is present.
func savedSuffix(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return " · saved " + t.Local().Format("2006-01-02 15:04")
}

// sessionKindLabel names an app-mode session by where it came from, since that
// determines whether it renews itself.
func sessionKindLabel(c credstore.Credentials) string {
	if c.TokenSource() == credstore.SourceDevice {
		return "qr"
	}
	return "app"
}

// expirySuffix renders " · expires <date>" when the expiry is known.
func expirySuffix(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return " · expires " + t.Local().Format("2006-01-02 15:04")
}
