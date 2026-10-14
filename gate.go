package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(env)+1)

	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}

	return append(result, prefix+value)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3456"
	}

	target, err := url.Parse("http://127.0.0.1:3457")
	if err != nil {
		log.Fatal(err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy error: %v", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		remoteIP := r.RemoteAddr
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			remoteIP = host
		}

		log.Printf(
			"IP_OBSERVE method=%q path=%q remote_addr=%q remote_ip=%q x_forwarded_for=%q x_real_ip=%q cf_connecting_ip=%q forwarded=%q user_agent=%q",
			r.Method,
			r.URL.Path,
			r.RemoteAddr,
			remoteIP,
			r.Header.Get("X-Forwarded-For"),
			r.Header.Get("X-Real-IP"),
			r.Header.Get("CF-Connecting-IP"),
			r.Header.Get("Forwarded"),
			r.UserAgent(),
		)

		proxy.ServeHTTP(w, r)
	})

	server := &http.Server{
		Addr:              "0.0.0.0:" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("GATE_LISTENING addr=0.0.0.0:%s", port)
		serverErr <- server.ListenAndServe()
	}()

	cmd := exec.Command("/app/vikunja/vikunja")
	cmd.Env = setEnv(
		os.Environ(),
		"VIKUNJA_SERVICE_INTERFACE",
		"127.0.0.1:3457",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		log.Fatalf("start Vikunja: %v", err)
	}
	log.Printf("VIKUNJA_STARTED pid=%d", cmd.Process.Pid)

	appDone := make(chan error, 1)
	go func() {
		appDone <- cmd.Wait()
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sig)

	var appErr error
	appExited := false

	select {
	case s := <-sig:
		log.Printf("received signal %s", s)

	case appErr = <-appDone:
		appExited = true
		log.Printf("Vikunja exited: %v", appErr)

	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP server exited: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)

	if !appExited {
		_ = cmd.Process.Signal(syscall.SIGTERM)

		select {
		case appErr = <-appDone:
			log.Printf("Vikunja stopped: %v", appErr)

		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-appDone
		}
	}
}