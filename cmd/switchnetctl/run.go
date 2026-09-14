package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/health"
)

// run.go implements `switchnetctl run [service...]`: build and start the Go
// services as native processes, in one terminal, for interactive use.
//
// This is deliberately NOT a supervisor. An unexpected exit is reported loudly
// and the others are left running -- a crash should be visible immediately
// while someone is actively iterating, not silently retried. Unattended
// restart-on-crash is deploy/systemd's job; duplicating that logic here would
// mean two different places deciding whether a crash is real.

// runServices is every service `run` knows how to build and start, in the
// same order the Makefile and scripts/update.sh already use. switchnetctl
// itself is a CLI, not a service, so it is not in this list.
var runServices = []string{"dns", "dauth", "aauth", "baas", "stubs", "natcheck", "npln", "dashboard", "toolbox"}

// shutdownGrace is how long a service gets to exit after SIGTERM before run
// gives up and sends SIGKILL. internal/service's own shutdown timeout is
// shorter than this, so this should never be the thing that actually fires.
const shutdownGrace = 8 * time.Second

// healthWait bounds how long run waits for every started service to answer
// /healthz before giving up and just naming the ones still not ready.
const healthWait = 20 * time.Second

// runCmd is `switchnetctl run`'s entry point, called from main's dispatch.
func runCmd(cfg config.Config, configPath string, args []string) {
	services := resolveRunServices(args)

	fmt.Fprintf(os.Stderr, "==> building: %s\n", strings.Join(services, " "))
	for _, s := range services {
		if err := buildService(s); err != nil {
			fmt.Fprintf(os.Stderr, "switchnetctl run: build %s: %v\n", s, err)
			os.Exit(1)
		}
	}

	out := &prefixedOutput{w: os.Stdout}

	type child struct {
		name string
		cmd  *exec.Cmd
	}
	var children []child
	exited := make(chan struct {
		name string
		err  error
	}, len(services))

	for _, s := range services {
		cmd, err := startService(s, configPath, out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "switchnetctl run: start %s: %v\n", s, err)
			for _, c := range children {
				_ = c.cmd.Process.Signal(syscall.SIGTERM)
			}
			os.Exit(1)
		}
		children = append(children, child{s, cmd})
	}
	for _, c := range children {
		go func(c child) {
			err := c.cmd.Wait()
			exited <- struct {
				name string
				err  error
			}{c.name, err}
		}(c)
	}

	fmt.Fprintln(os.Stderr, "==> waiting for /healthz")
	waitHealthy(cfg, services)
	fmt.Fprintln(os.Stderr, "==> up -- Ctrl-C to stop everything")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	running := len(children)
	shuttingDown := false
	var killTimer <-chan time.Time
	worst := 0

	for running > 0 {
		select {
		case <-ctx.Done():
			if !shuttingDown {
				shuttingDown = true
				fmt.Fprintln(os.Stderr, "\n==> stopping")
				for _, c := range children {
					_ = c.cmd.Process.Signal(syscall.SIGTERM)
				}
				timer := time.NewTimer(shutdownGrace)
				defer timer.Stop()
				killTimer = timer.C
			}

		case <-killTimer:
			fmt.Fprintln(os.Stderr, "==> some services did not stop in time, killing them")
			for _, c := range children {
				_ = c.cmd.Process.Kill()
			}
			killTimer = nil

		case ev := <-exited:
			running--
			if code := exitCode(ev.err); code > worst {
				worst = code
			}
			if !shuttingDown {
				fmt.Fprintf(os.Stderr, "\n!!! %s exited unexpectedly: %v !!!\n", ev.name, exitDetail(ev.err))
			}
		}
	}

	os.Exit(worst)
}

// resolveRunServices validates the requested service names against
// runServices, or returns every service when none were named.
func resolveRunServices(args []string) []string {
	if len(args) == 0 {
		return runServices
	}
	for _, s := range args {
		known := false
		for _, r := range runServices {
			if s == r {
				known = true
				break
			}
		}
		if !known {
			fmt.Fprintf(os.Stderr, "switchnetctl run: unknown service %q (know: %s)\n",
				s, strings.Join(runServices, ", "))
			os.Exit(2)
		}
	}
	return args
}

// buildService compiles one service to bin/<name>. Go's build cache makes a
// no-op rebuild sub-second, so this always runs rather than guessing
// staleness from file times -- a guess would get embedded assets wrong the
// same way scripts/update.sh's own comment about internal/dashboard/assets
// already warns about.
func buildService(name string) error {
	cmd := exec.Command("go", "build", "-o", filepath.Join("bin", name), "./cmd/"+name)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// startService execs an already-built binary, streaming its output through
// out with its name prefixed. No environment overrides are passed -- the
// point of `run` is that the config file is sufficient by itself.
func startService(name, configPath string, out *prefixedOutput) (*exec.Cmd, error) {
	bin, err := filepath.Abs(filepath.Join("bin", name))
	if err != nil {
		return nil, err
	}

	var args []string
	if configPath != "" {
		args = []string{"-config", configPath}
	}
	cmd := exec.Command(bin, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go streamPrefixed(out, name, stdout)
	go streamPrefixed(out, name, stderr)
	return cmd, nil
}

// prefixedOutput serializes writes from every service's output goroutines so
// concurrent lines from different services cannot interleave mid-line.
type prefixedOutput struct {
	mu sync.Mutex
	w  io.Writer
}

func streamPrefixed(out *prefixedOutput, name string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		out.mu.Lock()
		fmt.Fprintf(out.w, "[%-9s] %s\n", name, scanner.Text())
		out.mu.Unlock()
	}
}

// httpProbeAddr returns the loopback address to probe for a service's
// /healthz, or "" for one that has none: dns and natcheck are UDP, and
// npln's health endpoint only exists once npln.admin_token is set -- that is
// the only thing that starts its admin listener at all (see cmd/npln).
func httpProbeAddr(cfg config.Config, service string) string {
	switch service {
	case "dauth":
		return upstreamAddr(cfg.DAuth.Listen)
	case "aauth":
		return upstreamAddr(cfg.AAuth.Listen)
	case "baas":
		return upstreamAddr(cfg.BAAS.Listen)
	case "stubs":
		return upstreamAddr(cfg.Stubs.Listen)
	case "dashboard":
		return upstreamAddr(cfg.Dashboard.Listen)
	case "toolbox":
		return upstreamAddr(cfg.Toolbox.Listen)
	case "npln":
		if cfg.NPLN.AdminToken == "" {
			return ""
		}
		return upstreamAddr(cfg.NPLN.AdminListen)
	default: // dns, natcheck: no HTTP health endpoint to probe.
		return ""
	}
}

// waitHealthy polls every started service's /healthz until it answers or
// healthWait elapses, printing each as it becomes ready. It never fails run
// outright -- a service still starting is reported, not fatal, since its own
// output above already explains why.
func waitHealthy(cfg config.Config, services []string) {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(healthWait)

	pending := map[string]string{}
	for _, s := range services {
		if addr := httpProbeAddr(cfg, s); addr != "" {
			pending[s] = addr
		}
	}

	for len(pending) > 0 && time.Now().Before(deadline) {
		for s, addr := range pending {
			resp, err := client.Get("http://" + addr + health.Path)
			if err != nil {
				continue
			}
			_ = resp.Body.Close()
			if resp.StatusCode < 500 {
				fmt.Fprintf(os.Stderr, "    %s ok\n", s)
				delete(pending, s)
			}
		}
		if len(pending) > 0 {
			time.Sleep(300 * time.Millisecond)
		}
	}
	for s := range pending {
		fmt.Fprintf(os.Stderr, "    %s did not answer /healthz within %s -- check its output above\n",
			s, healthWait)
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

// exitDetail formats a child's exit for the "exited unexpectedly" banner,
// since a nil error (clean exit 0 that nobody asked for) is still worth
// flagging, not silence.
func exitDetail(err error) string {
	if err == nil {
		return "exit code 0"
	}
	return err.Error()
}
