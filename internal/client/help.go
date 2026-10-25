package client

import (
	"fmt"
	"strings"
)

// helpEntry is the text of `help COMMAND` and `COMMAND --help`.
type helpEntry struct{ usage, text, example string }

// commandHelp has one entry for every command in the dispatch table of run
// and for the commands handled before it (help, version).
var commandHelp = map[string]helpEntry{
	"help": {"help [COMMAND]",
		"Print the command overview, or the usage, description and an example of one command. `COMMAND --help` does the same.",
		"templatecontext help add"},
	"version": {"version", "Print the version and commit of this binary.", "templatecontext version"},

	"init": {"init --email EMAIL [--name NAME] [--port N] [--password-stdin]",
		"Create the project (database, storage/ and templatecontext.json) and the local account, then start the server. An existing account keeps its password unless --password-stdin is given.",
		"printf '%s\\n' \"$PASSWORD\" | templatecontext init --email you@example.localhost --password-stdin"},
	"ensure-running": {"ensure-running [--take-over]",
		"Start the server in the background unless it already runs for this database (alias: start). --take-over ignores a lease from another computer; use it only when that computer is off.",
		"templatecontext ensure-running"},
	"start": {"start [--take-over]", "Alias of ensure-running.", "templatecontext start"},
	"serve": {"serve [--take-over] [--ui-dev URL]", "Run the server in the foreground until interrupted. --ui-dev http://127.0.0.1:5173 serves the reader through a loopback Vite development server; APIs and sign-in remain on this server. Start Vite separately or use the repository development launcher. This option is foreground-only and is not saved in project configuration.", "templatecontext serve"},
	"stop":  {"stop", "Write the database back to the synced folder and stop the server. Run it before switching computers.", "templatecontext stop"},
	"status": {"status",
		"Show paths, port, lease, write-back conflicts and whether the server runs. With --url: whether the server answers and who is signed in.",
		"templatecontext status"},
	"open": {"open [--email EMAIL] [--no-browser]",
		"Open the reader signed in through a single-use link valid for 2 minutes. --email signs in as that account, e.g. to open the printed link on a computer that uses --url.",
		"templatecontext open --email user@example.localhost --no-browser"},
	"flush": {"flush", "Write the database back to the synced folder now.", "templatecontext flush"},
	"recover": {"recover --keep local|synced",
		"Resolve unsaved local changes when the synced database also changed elsewhere: keep this computer's copy or the synced one. The other copy is kept beside it.",
		"templatecontext recover --keep synced"},
	"password": {"password [--password-stdin]", "Change the reader password of the local account.", "templatecontext password"},
	"account": {"account --email EMAIL [--name NAME] [--password-stdin]",
		"Create another account on this server, e.g. for a person using it with --url. Without --password-stdin it never prompts: a new account gets a random password nobody knows and an existing one keeps its password.",
		"templatecontext account --email user@example.localhost --name User"},
	"login-code": {"login-code --email EMAIL [--ttl SECONDS]",
		"Print a one-time sign-in code for that account (single use, kept in server memory only, default 600 seconds, 30-600) for `login --url URL --code-stdin`.",
		"templatecontext login-code --email user@example.localhost"},
	"skill": {"skill [--agent claude-code|codex|antigravity|all] [--dir DIR]",
		"Install the agent skill with this computer's binary path and database (or, with --url, the remote server). Agents whose configuration folder is missing are skipped.",
		"templatecontext skill --agent claude-code"},
	"login": {"login --url URL (--code-stdin | --email EMAIL [--password-stdin])",
		"Sign in to a forwarded server, with a one-time code from `login-code` read from standard input, or with a password. Only a token is stored, in the private cache.",
		"printf '%s\\n' \"$CODE\" | templatecontext login --url http://127.0.0.1:8098 --code-stdin"},
	"logout": {"logout --url URL", "Forget the stored session for that server.", "templatecontext logout --url http://127.0.0.1:8098"},
	"tunnel": {"tunnel serve [--new-address] | tunnel forward (ADDRESS | --address-stdin) [--url URL]",
		"Carry the server's port between two computers through a private tailcat tunnel (WireGuard, bootstrapped through a public tailcat relay; no account, no administrator rights, nothing else to install). On the agent computer, `tunnel serve` exposes only the project's port, proxied to the server on 127.0.0.1, and prints the tunnel address. The address stays the same across restarts (its identity is kept in the private cache), and a running forward reconnects on its own; --new-address replaces it, after which the old address stops working. Anyone with the address can reach the port, so share it privately; TemplateContext still requires sign-in. On the person's computer, `tunnel forward` reads the address (preferably from standard input) and listens on the same loopback port as --url or TEMPLATECONTEXT_URL (default http://127.0.0.1:8098); a busy port is an error, never replaced by another. Both run in the foreground until interrupted; the forward never uses a local database.",
		"printf '%s\\n' \"$ADDRESS\" | templatecontext tunnel forward --address-stdin"},

	"whoami": {"whoami", "Show the signed-in account and the server URL.", "templatecontext whoami"},
	"schema": {"schema", "Print the live SQL-readable schema: tables, columns and types, and the search indexes.", "templatecontext schema"},
	"sql": {"sql SQL|-",
		"Run one read-only SELECT over your own records (alias: query). Results are bounded: when `truncated` is true, select fewer columns or page with ORDER BY and LIMIT/OFFSET. `-` reads the query from standard input.",
		"templatecontext sql \"SELECT id, message, created FROM greetings ORDER BY created DESC LIMIT 20\""},
	"query": {"query SQL|-", "Alias of sql.", "templatecontext query \"SELECT count(*) FROM greetings\""},
	"add": {"add MESSAGE|-",
		"Add a greeting (1-500 characters) owned by the signed-in account. The server sets the owner. `-` reads the message from standard input. Prints {\"id\", \"message\", \"owner\", \"created\", \"updated\"}.",
		"templatecontext add \"Hello from the command line\""},
	"list": {"list [--limit N]",
		"List your greetings, newest first (default 50, at most 500), as an array of {\"id\", \"message\", \"owner\", \"created\", \"updated\"}.",
		"templatecontext list --limit 10"},
	"search": {"search TERMS [--limit N] [--offset N --generation G]",
		"Rank your greetings by full-text match of their message. 1-16 terms, each matched as a whole word (all must match). Prints {\"query\", \"generation\", \"greetings\", \"hasMore\", \"nextOffset\"}. To page, pass the returned generation with --offset; a generation conflict (exit 4) means your greetings changed: restart at offset 0.",
		"templatecontext search \"hello\" --limit 10"},
}

// printHelp prints the help of one command, or the overview for "".
func printHelp(command string) error {
	if command == "" {
		fmt.Print(Usage)
		return nil
	}
	h, ok := commandHelp[command]
	if !ok {
		return fail(2, "unknown command %q; run `templatecontext help`", command)
	}
	fmt.Printf("usage: templatecontext %s\n\n%s\n\nExample:\n  %s\n", h.usage, wrap(h.text, 78), h.example)
	if command != "help" && command != "version" {
		fmt.Println("\nOptions for every command: --db PATH or --url URL, --pretty.")
	}
	return nil
}

// wrap folds each one-line paragraph at width; indented blocks stay as written.
func wrap(text string, width int) string {
	paragraphs := strings.Split(text, "\n\n")
	for i, p := range paragraphs {
		if strings.HasPrefix(p, " ") || strings.Contains(p, "\n") {
			continue
		}
		var lines []string
		line := ""
		for _, word := range strings.Fields(p) {
			if line != "" && len(line)+1+len(word) > width {
				lines, line = append(lines, line), ""
			}
			if line != "" {
				line += " "
			}
			line += word
		}
		paragraphs[i] = strings.Join(append(lines, line), "\n")
	}
	return strings.Join(paragraphs, "\n\n")
}

// wantsHelp reports whether the arguments ask for help (before any "--").
func wantsHelp(argv []string) bool {
	for _, arg := range argv {
		if arg == "--" {
			return false
		}
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

// helpTopic returns the command named after `help`, skipping global options.
func helpTopic(rest []string) (string, error) {
	a, err := parseArgs(rest, nil, nil)
	if err != nil {
		return "", err
	}
	if len(a.pos) > 1 {
		return "", fail(2, "usage: templatecontext help [COMMAND]")
	}
	if len(a.pos) == 0 {
		return "", nil
	}
	return strings.TrimSpace(a.pos[0]), nil
}
