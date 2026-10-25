package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/tailscale/tailcat"

	"github.com/pocketcontext-labs/pocketcontext-template/internal/bundle"
	"github.com/pocketcontext-labs/pocketcontext-template/internal/tunnel"
)

// identityFile is the tunnel identity in the project's private cache: never
// in the synced project folder, which other computers and people may read.
func identityFile(p *bundle.Project) string { return filepath.Join(p.Cache, "tunnel.json") }

// runTunnel runs `tunnel serve` on the agent computer or `tunnel forward` on
// the person's computer. Both run in the foreground until interrupted.
func runTunnel(a *args, pretty bool) error {
	if len(a.pos) == 0 {
		return fail(2, "usage: templatecontext tunnel serve|forward; run `templatecontext tunnel --help`")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch a.pos[0] {
	case "serve":
		return tunnelServe(ctx, a, pretty)
	case "forward":
		return tunnelForward(ctx, a, pretty)
	}
	return fail(2, "usage: templatecontext tunnel serve|forward; run `templatecontext tunnel --help`")
}

func tunnelServe(ctx context.Context, a *args, pretty bool) error {
	if a.has("url") {
		return fail(2, "tunnel serve runs on the agent computer beside the database; use --db, not --url")
	}
	if len(a.pos) != 1 || a.bools["address-stdin"] {
		return fail(2, "usage: templatecontext tunnel serve [--new-address]")
	}
	p, err := bundle.Resolve(a.flags["db"])
	if err != nil {
		return fail(2, "%v", err)
	}
	if _, err := os.Stat(p.DB); err != nil {
		return fail(2, "no TemplateContext database at %s; run `templatecontext init` first, or pass --db", p.DB)
	}
	path := identityFile(p)
	id, err := tunnel.LoadIdentity(path, a.bools["new-address"])
	if err != nil {
		return fail(1, "%v", err)
	}
	chosen := id.Region != nil
	running, _ := bundle.Running(p)
	err = tunnel.Serve(ctx, p.Config.Port, id, func(addr tailcat.Addr) {
		if !chosen && id.Region != nil {
			if err := id.Save(path); err != nil {
				fmt.Fprintf(os.Stderr, "templatecontext: could not keep the tunnel identity (%v); the address will change on the next start\n", err)
			}
		}
		out := map[string]any{"address": string(addr), "port": p.Config.Port, "serverRunning": running,
			"note": "keep the address private: anyone with it can reach this port (TemplateContext still requires sign-in). On the other computer run `templatecontext tunnel forward --address-stdin`. Stop with Ctrl-C."}
		if !running {
			out["warning"] = "the TemplateContext server does not run yet; start it with `templatecontext ensure-running`"
		}
		Print(out, pretty)
	})
	if err != nil {
		return fail(1, "%v", err)
	}
	return nil
}

func tunnelForward(ctx context.Context, a *args, pretty bool) error {
	if a.has("db") {
		return fail(2, "tunnel forward runs on the client computer and never uses a local database; use --url, not --db")
	}
	if a.bools["new-address"] {
		return fail(2, "--new-address belongs to `tunnel serve` on the agent computer")
	}
	var text string
	switch {
	case a.bools["address-stdin"] && len(a.pos) == 1:
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		text = line
	case !a.bools["address-stdin"] && len(a.pos) == 2:
		text = a.pos[1]
	default:
		return fail(2, "usage: templatecontext tunnel forward (ADDRESS | --address-stdin) [--url http://127.0.0.1:PORT]")
	}
	addr, err := tunnel.ParseAddress(strings.TrimSpace(text))
	if err != nil {
		return fail(2, "%v", err)
	}
	raw, fromFlag := a.flags["url"], a.has("url")
	if !fromFlag {
		raw = os.Getenv("TEMPLATECONTEXT_URL")
	}
	if raw == "" {
		raw = fmt.Sprintf("http://127.0.0.1:%d", bundle.DefaultPort)
	}
	base, err := normalizeURL(raw)
	if err != nil {
		return err
	}
	u, _ := url.Parse(base)
	port, _ := strconv.Atoi(u.Port())
	host := u.Hostname()
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ln, err := tunnel.Listen(host, port)
	if err != nil {
		return fail(1, "%v", err)
	}
	Print(map[string]any{"url": base, "forwarding": true,
		"note": "the agent computer's TemplateContext is reachable at this URL while this runs; sign in with `templatecontext login --url " + base + " --code-stdin`. Stop with Ctrl-C."}, pretty)
	if err := tunnel.Forward(ctx, addr, ln, port); err != nil {
		return fail(1, "the forward stopped: %v", err)
	}
	return nil
}
