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
func resolveAuth(cookie, userAgent string, browserCk browserCookiesFlag, site domain.Site, browserLoadFatal bool) (resolvedCookie, resolvedUA string, fatal bool) {
	resolvedCookie = cookie
	resolvedUA = userAgent

	if resolvedCookie == "" && browserCk.set {
		ck, ckDomain, err := browsercookies.Load(browserCk.value, cookieDomains(site)...)
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
			// No website session saved; run anonymously. An app-token-only
			// login lands here: its User-Agent belongs to the Android client
			// and must not be attached to website requests.
		case !storedCredentialsAllowed(stored.Site, site):
			warnStoredCredentialsWithheld(stored.Site, site)
		default:
			resolvedCookie = stored.Cookie
			if resolvedUA == "" && stored.UserAgent != "" {
				resolvedUA = stored.UserAgent
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

// storedCredentialsAllowed reports whether the session saved by `kinopub login`
// may be sent to the site this run targets. A stored session belongs to exactly
// one site, so it travels to that host and its subdomains only — the target
// host is whatever the user-supplied URL names, and an unrelated one is by
// definition not the site the user logged in to.
//
// storedSite is empty for credentials written before the site was recorded.
// Rather than forcing a re-login, those are treated the way the tool treated
// them when it wrote them: as belonging to one of the hosts the service is
// known by. They are still withheld from any host outside that set.
func storedCredentialsAllowed(storedSite string, target domain.Site) bool {
	if strings.TrimSpace(storedSite) == "" {
		return domain.AnyKnownSiteOwns(target.String())
	}
	return domain.SiteFromHost(storedSite).Owns(target.String())
}

// warnStoredCredentialsWithheld explains why the saved session is not being
// sent. Unlike a browser-cookie domain mismatch, which is only advisory, this
// one withholds the cookie — so say so, and say how to get one that fits. The
// run continues anonymously, which is enough for public pages.
func warnStoredCredentialsWithheld(storedSite string, site domain.Site) {
	origin := strings.TrimSpace(storedSite)
	if origin == "" {
		origin = strings.Join(domain.KnownSiteHosts, " or ") + " (site not recorded at login)"
	}
	warnf("saved credentials for %s are not sent to %s — a stored session "+
		"is only used on the site it was created for. Continuing without them. "+
		"To use credentials here, run `kinopub login --site %s`, or pass --cookie explicitly.",
		origin, site, site)
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
		appToken  string
		appBase   string
		proxyURL  string
	)

	fs.StringVar(&cookie, "cookie", "", "raw Cookie header value to store")
	fs.StringVar(&userAgent, "user-agent", "", "User-Agent to store (browser UA for cookies; the app's UA for --app)")
	fs.Var(&browserCk, "browser-cookies", "auto-load cookies from a browser: safari, chrome, firefox, or auto")
	fs.StringVar(&siteHost, "site", "", "site host to read cookies for, e.g. kino.watch (default: "+strings.Join(domain.KnownSiteHosts, ", then ")+")")
	fs.BoolVar(&appMode, "app", false, "save the installed kino.pub app's session (its API token) instead of a website cookie — reuses the app's device slot")
	fs.StringVar(&appToken, "app-token", "", "app access token to save for --app (default: read from the installed app when run as root)")
	fs.StringVar(&appBase, "app-base", "", "override the kino.pub JSON API base URL for --app (default: "+kinopubapp.DefaultAPIBase+")")
	fs.StringVar(&proxyURL, "proxy", "", "proxy URL used to validate the --app token (http, https, socks5)")
	registerColorFlags(fs)

	fs.Usage = func() {
		h := newHelpPrinter(os.Stderr, errStyle)
		h.text("Save authentication (encrypted, machine-bound). Two independent methods:")

		h.section("Website cookies (Cloudflare session):")
		h.commands(
			command{name: "kinopub login --cookie \"cf_clearance=...; _identity=...\" --user-agent \"Mozilla/5.0 ...\""},
			command{name: "kinopub login --browser-cookies safari"},
		)

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

	// The app-session method is a separate flow: resolve, validate and save the
	// installed app's token, then stop. It shares no state with the cookie path.
	if appMode {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
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
	site := domain.SiteFromHost(siteHost)
	resolvedCookie := cookie
	if resolvedCookie == "" && browserCk.set {
		ck, ckDomain, err := browsercookies.Load(browserCk.value, cookieDomains(site)...)
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

	// Bind the credentials to the site they were obtained for: later runs send
	// them to that site only, never to some other host a URL happens to name.
	//
	// Only the website half is replaced. The two methods are independent, so
	// saving cookies must not throw away an app session stored earlier (and
	// vice versa) — a user may hold both and switch between them.
	creds, err := credstore.Load()
	if err != nil {
		creds = credstore.Credentials{}
	}
	creds.Cookie = resolvedCookie
	creds.UserAgent = userAgent
	creds.Site = site.String()
	creds.CookieSavedAt = time.Now()

	if err := credstore.Save(creds); err != nil {
		errorf("%v", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "%s Credentials for %s saved (encrypted, machine-bound) to ~/.config/kinopub/credentials.enc\n",
		errStyle.Green("✓"), errStyle.Cyan(creds.Site))
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
	fs.BoolVar(&appOnly, "app", false, "remove only the kino.pub app session, keep the website login")
	fs.BoolVar(&cookieOnly, "cookie", false, "remove only the website login, keep the app session")
	registerColorFlags(fs)
	fs.Usage = func() {
		h := newHelpPrinter(os.Stderr, errStyle)
		h.text("Remove stored credentials.")
		h.section("Usage:")
		h.commands(
			command{name: "kinopub logout", desc: "remove everything"},
			command{name: "kinopub logout --app", desc: "remove only the app session"},
			command{name: "kinopub logout --cookie", desc: "remove only the website login"},
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
		if !creds.HasCookie() {
			fmt.Fprintf(os.Stderr, "No website login was stored.\n")
			return 0
		}
		creds.Cookie, creds.UserAgent, creds.Site = "", "", ""
		creds.CookieSavedAt = time.Time{}
		removed = "website login"
	}
	if creds.LastUsed == credstore.MethodApp && appOnly ||
		creds.LastUsed == credstore.MethodCookie && cookieOnly {
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

	// Website (cookie) session.
	if creds.HasCookie() {
		mark := "  "
		if preferred == credstore.MethodCookie {
			mark = errStyle.Green("→ ")
		}
		site := creds.Site
		if site == "" {
			site = "(site not recorded)"
		}
		fmt.Fprintf(os.Stderr, "%swebsite  · site %s%s\n", mark, errStyle.Cyan(site), savedSuffix(creds.CookieSavedAt))
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
		fmt.Fprintf(os.Stderr, "%sapp      · API %s%s\n", mark, errStyle.Cyan(base), savedSuffix(creds.AppSavedAt))

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
