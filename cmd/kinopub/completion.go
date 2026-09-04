// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"os"
)

// runCompletion prints a shell completion script to stdout.
// Usage: kinopub completion bash
//
//	kinopub completion fish
func runCompletion(args []string) int {
	shell := ""
	if len(args) > 0 {
		shell = args[0]
	}
	switch shell {
	case "fish":
		fmt.Print(fishCompletion)
	case "bash":
		fmt.Print(bashCompletion)
	default:
		h := newHelpPrinter(os.Stderr, errStyle)
		h.line("%s kinopub completion <shell>", errStyle.Bold("Usage:"))
		h.section("Available shells:")
		h.commands(
			command{name: "bash", desc: "source <(kinopub completion bash)"},
			command{name: "fish", desc: "kinopub completion fish | source"},
		)
		h.section("To install permanently:")
		h.line("  %s  kinopub completion bash >> ~/.bashrc", errStyle.Cyan("bash:"))
		h.line("  %s  kinopub completion fish > ~/.config/fish/completions/kinopub.fish", errStyle.Cyan("fish:"))
		if shell != "" {
			return 1
		}
	}
	return 0
}

const fishCompletion = `# kinopub fish shell completion
# Install: kinopub completion fish > ~/.config/fish/completions/kinopub.fish

set -l subcommands login logout sessions doctor config completion update

# Subcommands
complete -c kinopub -f -n "not __fish_seen_subcommand_from $subcommands" -a login      -d "Save authentication credentials"
complete -c kinopub -f -n "not __fish_seen_subcommand_from $subcommands" -a logout     -d "Remove stored credentials"
complete -c kinopub -f -n "not __fish_seen_subcommand_from $subcommands" -a sessions   -d "Show stored login sessions"
complete -c kinopub -f -n "not __fish_seen_subcommand_from $subcommands" -a doctor     -d "Verify files and repair state"
complete -c kinopub -f -n "not __fish_seen_subcommand_from $subcommands" -a config     -d "Show or change the saved settings"
complete -c kinopub -f -n "not __fish_seen_subcommand_from $subcommands" -a completion -d "Generate shell completion script"
complete -c kinopub -f -n "not __fish_seen_subcommand_from $subcommands" -a update     -d "Install the latest release"

# Main command flags
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands" -s o -l output        -d "Output directory path" -r -F
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands" -s c -l concurrency   -d "Max concurrent downloads" -r
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands" -l limit-rate      -d "Cap download speed, e.g. 2M or 500k" -r
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l proxy          -d "Proxy URL (http, https, socks5)" -r
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands" -s q -l quality       -d "Quality preference" -r -a "4k 2160p 1080p 720p 480p 360p"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l verbosity      -d "Log verbosity" -r -a "quiet\t'Suppress output' normal\t'Default' verbose\t'All messages'"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands" -s v                  -d "Verbose output"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l ffmpeg         -d "ffmpeg binary path" -r -F
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l log-file       -d "Log file path" -r -F
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l container      -d "Output container" -r -a "mkv\t'Matroska (default)' mp4\t'MPEG-4'"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l force          -d "Force re-download of completed episodes"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l seasons        -d "Season selection (e.g. 1,3-5)" -r
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l episodes       -d "Episode selection (e.g. 1,3-5)" -r
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l dry-run        -d "List episodes without downloading"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l cookie         -d "Raw Cookie header value" -r
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l user-agent     -d "User-Agent header" -r
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l header         -d "Extra HTTP header 'Name: Value'" -r
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l browser-cookies -d "Auto-load cookies from browser" -r -a "safari chrome firefox auto"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l feed-file      -d "Read RSS feed from local file" -r -F
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l ffmpeg-args    -d "Extra ffmpeg arguments" -r
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands" -s x                  -d "Extra ffmpeg argument (repeatable)" -r
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l no-chunked     -d "Disable chunked HTTP download"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l audio          -d "Audio track selection (e.g. anilibria,!jpn)" -r
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l audio-menu     -d "Show interactive audio-track picker"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l subs           -d "Subtitle track selection (e.g. rus,!eng)" -r
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l subs-menu      -d "Show interactive subtitle-track picker"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l subs-external  -d "Write subtitles as separate .srt files"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l subs-only      -d "Download only subtitles, skipping video/audio"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l video-menu     -d "Show interactive video-quality picker"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l no-domain-rewrite -d "Do not rewrite former site domains to the current one"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l app            -d "Use the installed kino.pub app's session (its API token)"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l app-token      -d "App access token for --app" -r
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l app-base       -d "Override the kino.pub JSON API base URL" -r
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l app-codec      -d "Preferred codec for --app: h264 or h265" -r -a "h264 h265"
complete -c kinopub -n "__fish_seen_subcommand_from update" -l check   -d "Only report whether a newer release exists"
complete -c kinopub -n "__fish_seen_subcommand_from update" -l proxy   -d "Proxy URL" -r
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l color          -d "When to color output" -r -a "auto\t'A terminal only (default)' always\t'Even when piped' never\t'Never'"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l no-color       -d "Never color output"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l no-notify      -d "No system notifications for this run"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l notify         -d "System notifications even if turned off in the settings"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands" -s i -l interactive    -d "Pick quality, audio and subtitles interactively"
complete -c kinopub -n "not __fish_seen_subcommand_from $subcommands"      -l version        -d "Print version and exit"

# Color flags are accepted by every subcommand
complete -c kinopub -n "__fish_seen_subcommand_from login doctor config update" -l color    -d "When to color output" -r -a "auto always never"
complete -c kinopub -n "__fish_seen_subcommand_from login doctor config update" -l no-color -d "Never color output"

# login flags
complete -c kinopub -n "__fish_seen_subcommand_from login" -l cookie          -d "Cookie header to store" -r
complete -c kinopub -n "__fish_seen_subcommand_from login" -l user-agent      -d "User-Agent to store" -r
complete -c kinopub -n "__fish_seen_subcommand_from login" -l browser-cookies -d "Auto-load cookies from browser" -r -a "safari chrome firefox auto"
complete -c kinopub -n "__fish_seen_subcommand_from login" -l app             -d "Save the installed kino.pub app's session (its API token)"
complete -c kinopub -n "__fish_seen_subcommand_from login" -l app-token       -d "App access token to save for --app" -r
complete -c kinopub -n "__fish_seen_subcommand_from login" -l qr              -d "Authorize this tool by QR/device code (own, self-renewing session)"
complete -c kinopub -n "__fish_seen_subcommand_from login" -l client-secret   -d "OAuth client secret for --qr" -r
complete -c kinopub -n "__fish_seen_subcommand_from login" -l app-base        -d "Override the kino.pub JSON API base URL" -r
complete -c kinopub -n "__fish_seen_subcommand_from login" -l proxy           -d "Proxy URL to validate the --app token" -r
complete -c kinopub -n "__fish_seen_subcommand_from logout" -l app    -d "Remove only the app session" 
complete -c kinopub -n "__fish_seen_subcommand_from logout" -l cookie -d "Remove only the website login" 
complete -c kinopub -n "__fish_seen_subcommand_from sessions" -l check -d "Verify the app token online" 
complete -c kinopub -n "__fish_seen_subcommand_from sessions" -l proxy -d "Proxy URL for --check" -r
complete -c kinopub -f -n "__fish_seen_subcommand_from sessions; and not __fish_seen_subcommand_from export import" -a export -d "Export the session to a portable file"
complete -c kinopub -f -n "__fish_seen_subcommand_from sessions; and not __fish_seen_subcommand_from export import" -a import -d "Import a session from a portable file"
complete -c kinopub -n "__fish_seen_subcommand_from export" -l out            -d "File to write (- for stdout)" -r
complete -c kinopub -n "__fish_seen_subcommand_from export" -l force          -d "Overwrite the destination"
complete -c kinopub -n "__fish_seen_subcommand_from export" -l include-cookie -d "Also export the website cookie session"
complete -c kinopub -n "__fish_seen_subcommand_from import" -l replace        -d "Discard any existing session instead of merging"

# doctor flags
complete -c kinopub -n "__fish_seen_subcommand_from doctor" -s o -l output         -d "Output directory to check" -r -F
complete -c kinopub -n "__fish_seen_subcommand_from doctor"      -l fix             -d "Repair state file"
complete -c kinopub -n "__fish_seen_subcommand_from doctor"      -l clean-tmp       -d "Delete orphan .tmp files"
complete -c kinopub -n "__fish_seen_subcommand_from doctor" -s v                   -d "Verbose output"
complete -c kinopub -n "__fish_seen_subcommand_from doctor"      -l skip-probe      -d "Skip duration verification"
complete -c kinopub -n "__fish_seen_subcommand_from doctor"      -l ffprobe         -d "ffprobe binary path" -r -F
complete -c kinopub -n "__fish_seen_subcommand_from doctor"      -l cookie          -d "Cookie header for resolving source" -r
complete -c kinopub -n "__fish_seen_subcommand_from doctor"      -l user-agent      -d "User-Agent for resolving source" -r
complete -c kinopub -n "__fish_seen_subcommand_from doctor"      -l browser-cookies -d "Auto-load cookies from browser" -r -a "safari chrome firefox auto"
complete -c kinopub -n "__fish_seen_subcommand_from doctor"      -l proxy           -d "Proxy URL" -r

# config: the verb, then the key, then its values
complete -c kinopub -f -n "__fish_seen_subcommand_from config; and not __fish_seen_subcommand_from show get set unset path" -a "show\t'Show every setting' get\t'Print one value' set\t'Save a setting' unset\t'Back to the default' path\t'Path of the settings file'"
complete -c kinopub -f -n "__fish_seen_subcommand_from config; and __fish_seen_subcommand_from get set unset; and not __fish_seen_subcommand_from notifications" -a "notifications\t'System notifications about progress'"
complete -c kinopub -f -n "__fish_seen_subcommand_from config; and __fish_seen_subcommand_from set; and __fish_seen_subcommand_from notifications" -a "on\t'Post notifications (default)' off\t'Never post notifications'"

# completion flags
complete -c kinopub -f -n "__fish_seen_subcommand_from completion" -a "bash fish"
`

const bashCompletion = `# kinopub bash shell completion
# Install: source <(kinopub completion bash)
#          or: kinopub completion bash >> ~/.bashrc

_kinopub_completion() {
    local cur prev words cword
    _init_completion || return

    local subcommands="login logout sessions doctor config completion update"
    local main_flags="-o --output -c --concurrency --limit-rate --proxy -q --quality
        --verbosity -v --ffmpeg --log-file --container --force --seasons --episodes
        --dry-run --cookie --user-agent --header --browser-cookies
        --feed-file --ffmpeg-args -x --no-chunked --audio --audio-menu \
        --subs --subs-menu --subs-external --subs-only \\
        --video-menu --no-domain-rewrite -i --interactive \
        --no-notify --notify \
        --app --app-token --app-base --app-codec --color --no-color --version"

    # Detect which subcommand is active
    local subcmd=""
    for w in "${words[@]:1}"; do
        case "$w" in
            login|logout|sessions|doctor|config|completion|update)
                subcmd="$w"
                break
                ;;
        esac
    done

    case "$subcmd" in
        login)
            case "$prev" in
                --cookie|--user-agent|--app-token|--app-base|--client-secret|--proxy) return ;;
                --color) COMPREPLY=($(compgen -W "auto always never" -- "$cur")); return ;;
                --browser-cookies) COMPREPLY=($(compgen -W "safari chrome firefox auto" -- "$cur")); return ;;
            esac
            COMPREPLY=($(compgen -W "--cookie --user-agent --browser-cookies --app --app-token --app-base --qr --client-secret --proxy --color --no-color" -- "$cur"))
            ;;
        logout)
            COMPREPLY=($(compgen -W "--app --cookie --color --no-color" -- "$cur"))
            ;;
        sessions)
            case "$prev" in
                --proxy) return ;;
                --color) COMPREPLY=($(compgen -W "auto always never" -- "$cur")); return ;;
            esac
            # Sub-subcommands of sessions, and their own flags.
            local sessions_sub=""
            for w in "${words[@]:1}"; do
                case "$w" in
                    export|import) sessions_sub="$w"; break ;;
                esac
            done
            case "$sessions_sub" in
                export)
                    case "$prev" in
                        --out) COMPREPLY=($(compgen -f -- "$cur")); return ;;
                    esac
                    COMPREPLY=($(compgen -W "--out --force --include-cookie --color --no-color" -- "$cur"))
                    return
                    ;;
                import)
                    COMPREPLY=($(compgen -f -W "--replace --color --no-color" -- "$cur"))
                    return
                    ;;
            esac
            COMPREPLY=($(compgen -W "export import --check --proxy --color --no-color" -- "$cur"))
            ;;
        doctor)
            case "$prev" in
                -o|--output|--ffprobe) COMPREPLY=($(compgen -d -- "$cur")); return ;;
                --cookie|--user-agent|--proxy) return ;;
                --color) COMPREPLY=($(compgen -W "auto always never" -- "$cur")); return ;;
                --browser-cookies) COMPREPLY=($(compgen -W "safari chrome firefox auto" -- "$cur")); return ;;
            esac
            COMPREPLY=($(compgen -W "-o --output --fix --clean-tmp -v --skip-probe
                --ffprobe --cookie --user-agent --browser-cookies --proxy
                --color --no-color" -- "$cur"))
            ;;
        config)
            # config <verb> <key> <value>: offer whichever part comes next.
            case "$prev" in
                get|set|unset) COMPREPLY=($(compgen -W "notifications" -- "$cur")); return ;;
                notifications) COMPREPLY=($(compgen -W "on off" -- "$cur")); return ;;
                --color) COMPREPLY=($(compgen -W "auto always never" -- "$cur")); return ;;
            esac
            COMPREPLY=($(compgen -W "show get set unset path --color --no-color" -- "$cur"))
            ;;
        completion)
            COMPREPLY=($(compgen -W "bash fish" -- "$cur"))
            ;;
        update)
            case "$prev" in
                --proxy) return ;;
                --color) COMPREPLY=($(compgen -W "auto always never" -- "$cur")); return ;;
            esac
            COMPREPLY=($(compgen -W "--check --proxy -v --color --no-color" -- "$cur"))
            ;;
        *)
            # Main command
            if [[ "$cur" == -* ]]; then
                case "$prev" in
                    -o|--output|--log-file|--feed-file|--ffmpeg)
                        COMPREPLY=($(compgen -f -- "$cur")); return ;;
                    -q|--quality)
                        COMPREPLY=($(compgen -W "4k 2160p 1080p 720p 480p 360p" -- "$cur")); return ;;
                    --container)
                        COMPREPLY=($(compgen -W "mkv mp4" -- "$cur")); return ;;
                    --verbosity)
                        COMPREPLY=($(compgen -W "quiet normal verbose" -- "$cur")); return ;;
                    --color)
                        COMPREPLY=($(compgen -W "auto always never" -- "$cur")); return ;;
                    --browser-cookies)
                        COMPREPLY=($(compgen -W "safari chrome firefox auto" -- "$cur")); return ;;
                    --cookie|--user-agent|--proxy|--header|--seasons|--episodes| \
                    --ffmpeg-args|-x|-c|--concurrency|--limit-rate|--audio|--subs)
                        return ;;
                esac
                COMPREPLY=($(compgen -W "$main_flags" -- "$cur"))
            else
                # No subcommand yet: offer subcommands + file completion for URL/path arg
                if [[ -z "$subcmd" ]]; then
                    COMPREPLY=($(compgen -W "$subcommands" -- "$cur"))
                    # Also allow files (for local feed files)
                    COMPREPLY+=($(compgen -f -- "$cur"))
                fi
            fi
            ;;
    esac
}

complete -F _kinopub_completion kinopub
`
