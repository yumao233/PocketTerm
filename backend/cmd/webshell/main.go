// Command webshell is the process entry point for the Webshell backend. It loads
// configuration, brings up the in-process SSH server (loopback only), the PTY
// manager, the storage + service + handler layers, the gin router, and finally
// the HTTP(S) server. It blocks on the HTTP server and shuts everything down
// gracefully on SIGINT/SIGTERM.
//
// Configuration is loaded from config.json in the working directory; if absent, a
// default with fresh random secrets is generated and persisted (see config.Load).
//
// Usage:
//
//	./webshell            # reads ./config.json (auto-generated on first run)
//	cd /opt/webshell && ./bin/webshell
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wangkun/webshell/internal/config"
	"github.com/wangkun/webshell/internal/model"
	"github.com/wangkun/webshell/internal/pty"
	"github.com/wangkun/webshell/internal/router"
	"github.com/wangkun/webshell/internal/service"
	"github.com/wangkun/webshell/internal/ssh"
)

func main() {
	// 1. Load configuration. config.json is auto-generated with random secrets
	// on first run (see config.Load).
	cfg, err := config.Load("config.json")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// 2. Ensure the storage directory exists (stores write JSON files into it).
	if err := os.MkdirAll(cfg.Storage.Dir, 0o755); err != nil {
		log.Fatalf("create storage dir %s: %v", cfg.Storage.Dir, err)
	}

	// 3. Initialize the JSON-file stores.
	users, err := model.NewUserStore(cfg.Storage.Dir)
	if err != nil {
		log.Fatalf("init user store: %v", err)
	}
	templates, err := model.NewTemplateStore(cfg.Storage.Dir)
	if err != nil {
		log.Fatalf("init template store: %v", err)
	}
	ssoTokens, err := model.NewSsoTokenStore(cfg.Storage.Dir)
	if err != nil {
		log.Fatalf("init sso token store: %v", err)
	}

	// 3b. SSO token cleanup. Expired (and thus unusable) SSO tokens are purged
	// periodically so the data file doesn't grow unbounded across runs. We do a
	// best-effort sweep immediately at startup (clears stale tokens left over
	// from a previous process) and then every hour. The goroutine stops cleanly
	// on shutdown via ctx, which is cancelled when SIGINT/SIGTERM arrives.
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	go func() {
		// Best-effort initial sweep — ignore errors (a transient I/O failure
		// just means stale tokens linger until the next tick).
		if err := ssoTokens.DeleteExpired(); err != nil {
			log.Printf("sso token initial cleanup: %v", err)
		}
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := ssoTokens.DeleteExpired(); err != nil {
					log.Printf("sso token cleanup: %v", err)
				}
			case <-shutdownCtx.Done():
				return
			}
		}
	}()

	// 4. Start the local (loopback) SSH server. The manager dials it as a client,
	// so it MUST be listening before we construct dependencies that assume a live
	// backend. Start is blocking (it Serve's), so run it in a goroutine.
	sshSrv, err := ssh.NewServer(ssh.Config{
		Listen:      cfg.SSH.Listen,
		Passcode:    cfg.SSH.Passcode,
		HostKeyFile: cfg.SSH.HostSignerFile,
	})
	if err != nil {
		log.Fatalf("init ssh server: %v", err)
	}
	go func() {
		if err := sshSrv.Start(); err != nil {
			log.Printf("ssh server stopped: %v", err)
		}
	}()

	// Wait until the SSH server is accepting connections. NewServer does NOT bind
	// (binding happens inside Start), so we poll-dial the listen address with a
	// short backoff. Failing fast here is preferable to every PTY Start hitting a
	// connection refused on the first request.
	if err := waitForListen(cfg.SSH.Listen, 50, 50*time.Millisecond); err != nil {
		log.Fatalf("ssh server did not become ready on %s: %v", cfg.SSH.Listen, err)
	}

	// 5. PTY manager — dials the local SSH server using the shared passcode.
	// Wire the SSH server's in-process PTY resizer so window-size changes apply
	// to the real PTY immediately (the SSH window-change request alone is not
	// applied to the PTY by gliderlabs/ssh v0.3.8).
	ptyMgr := pty.NewManager(pty.SSHDialConfig{
		Addr:     cfg.SSH.Listen,
		Passcode: cfg.SSH.Passcode,
	})
	ptyMgr.SetPTYSizer(sshSrv)

	// 6. Services.
	authSvc := service.NewAuthService(users, ssoTokens, cfg.JWT.Secret, time.Duration(cfg.JWT.TTLMinutes)*time.Minute)
	tplSvc := service.NewTemplateService(templates)
	ptySvc := service.NewPtyService(ptyMgr, tplSvc)
	// SysInfoService kicks off a background warmup of the gopsutil CPU sampler
	// so the first /system/stats request returns a real percentage rather than
	// 0; the warmup is non-blocking and failure-tolerant.
	sysInfoSvc := service.NewSysInfoService()

	// 7. Router (gin engine) — wires all routes and middlewares.
	engine := router.New(router.Deps{
		Cfg:         cfg,
		AuthSvc:     authSvc,
		TemplateSvc: tplSvc,
		PtySvc:      ptySvc,
		PtyManager:  ptyMgr,
		SysInfoSvc:  sysInfoSvc,
	})

	// 8. HTTP(S) server. We use http.Server directly (rather than
	// http.ListenAndServe) so we can Shutdown gracefully on signal.
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	httpSrv := &http.Server{
		Addr:    addr,
		Handler: engine,
	}

	sslEnabled := cfg.Server.SSL.Enabled
	go func() {
		var serveErr error
		if sslEnabled {
			serveErr = httpSrv.ListenAndServeTLS(cfg.Server.SSL.CertFile, cfg.Server.SSL.KeyFile)
		} else {
			serveErr = httpSrv.ListenAndServe()
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			log.Fatalf("http server: %v", serveErr)
		}
	}()

	log.Printf("webshell listening on %s (ssl=%v), ssh backend on %s", addr, sslEnabled, cfg.SSH.Listen)

	// 9. Graceful shutdown on SIGINT / SIGTERM. Signal the cleanup goroutine to
	// stop (shutdownCancel), close the SSH server (unbinding its listener and
	// tearing down in-flight sessions) and Shutdown the HTTP server with a 5s
	// grace period.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Printf("shutting down")

	// Stop the SSO token cleanup goroutine (and any other shutdownCtx watcher).
	shutdownCancel()

	// Close every live PTY session so no child process outlives this process.
	// Without this, the SSH server's listener would close but spawned shells
	// would keep running as orphans. Best-effort: per-session Close is idempotent.
	ptyMgr.CloseAll()
	log.Printf("pty sessions closed")

	if err := sshSrv.Close(); err != nil {
		log.Printf("ssh server close: %v", err)
	}

	httpShutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(httpShutdownCtx); err != nil {
		log.Printf("http server shutdown: %v", err)
	}
}

// waitForListen repeatedly dial-probes addr until a TCP connection succeeds,
// indicating some server (here, the local SSH server) is accepting connections.
// It tries up to `attempts` times, sleeping `delay` between attempts. Returns
// nil once the dial succeeds, or the last error after all attempts are spent.
//
// We use net.Dial with a tiny timeout rather than relying on the server's Addr()
// because Start binds the listener inside its own goroutine and our only external
// signal that binding succeeded is "connections are now accepted".
func waitForListen(addr string, attempts int, delay time.Duration) error {
	var lastErr error
	for i := 0; i < attempts; i++ {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(delay)
	}
	return lastErr
}
