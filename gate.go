package main

// Vikunja の前に置く、IPゲート兼プロセス監視。
//
// 環境変数（すべて任意。DB関連は、観察モードなら無くても動く）:
//   PORT                        公開側ポート（Renderが設定）
//   GATE_MODE                   observe（既定）/ enforce
//   GATE_UPSTREAM               既定 127.0.0.1:3457
//   GATE_HEALTH_PATH            既定 /gate/healthz（IP制限の対象外。ゲート自身が応答）
//   GATE_UPSTREAM_HEALTH_PATH   例 /api/v1/info。空ならポートへの接続だけを確認
//   GATE_HEALTH_REQUIRE_POLICY  1 のとき、許可リスト未読み込みでも healthz を 503 にする
//   GATE_EXACT_ALLOW            IP制限の例外。"GET /path,POST /path2" のように、完全一致で
//   GATE_REDIRECT_URL           既定 https://gate.jyrac.stki.org/
//   GATE_DENIED_URL             既定 https://gate.jyrac.stki.org/denied（?ip=<IP> を付けて飛ばす）
//   GATE_LOG_ALLOWED            1 のとき、許可したリクエストも記録する（観察用）
//   GATE_DB_HOST / GATE_DB_PORT(6543) / GATE_DB_NAME(postgres) / GATE_DB_USER / GATE_DB_PASSWORD
//   GATE_DB_SSLMODE(require) / GATE_DB_SSLROOTCERT
//
// GATE_ で始まる環境変数は、Vikunja の子プロセスには渡さない。

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ---- 補助 ----

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseExact(s string) map[string]bool {
	m := map[string]bool{}
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			m[item] = true
		}
	}
	return m
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

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

func dropEnvPrefix(env []string, prefix string) []string {
	out := make([]string, 0, len(env))
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return out
}

// clientIP は、CF-Connecting-IP がちょうど1つで、IPとして有効なときだけ採用する。
func clientIP(r *http.Request) (netip.Addr, bool) {
	vals := r.Header.Values("CF-Connecting-IP")
	if len(vals) != 1 {
		return netip.Addr{}, false
	}
	a, err := netip.ParseAddr(strings.TrimSpace(vals[0]))
	if err != nil || a.Zone() != "" {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

// isNavigation は、ブラウザがページを開こうとしているときだけ true。
// API呼び出し、fetch、WebSocket、iframe、POST は false（リダイレクトすると壊れるため）。
func isNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate" && r.Header.Get("Sec-Fetch-Dest") == "document"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// cleanExact は、例外パスの完全一致をすり抜けるような表記を弾く。
func cleanExact(r *http.Request) bool {
	return r.URL.RawPath == "" &&
		!strings.ContainsAny(r.URL.Path, ";\\") &&
		path.Clean(r.URL.Path) == r.URL.Path
}

// ---- ヘルスチェック（ゲート自身が応答する。IP制限の対象外）----

type healthChecker struct {
	store         *Store
	upstream      string
	path          string
	requirePolicy bool
	client        *http.Client

	mu        sync.Mutex
	checkedAt time.Time
	lastOK    bool
}

func newHealthChecker(store *Store, upstream, path string, requirePolicy bool) *healthChecker {
	return &healthChecker{
		store:         store,
		upstream:      upstream,
		path:          path,
		requirePolicy: requirePolicy,
		client: &http.Client{
			Timeout: 3 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (h *healthChecker) probe() bool {
	if h.path == "" {
		conn, err := net.DialTimeout("tcp", h.upstream, 2*time.Second)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}
	resp, err := h.client.Get("http://" + h.upstream + h.path)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// check は結果を5秒だけ使い回す（公開されたパスなので、叩かれても上流への確認が増えないように）。
func (h *healthChecker) check() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.checkedAt.IsZero() && time.Since(h.checkedAt) < 5*time.Second {
		return h.lastOK
	}
	h.lastOK = h.probe()
	h.checkedAt = time.Now()
	return h.lastOK
}

func (h *healthChecker) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.check() || (h.requirePolicy && h.store.Get() == nil) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte("ok"))
	}
}

// ---- ゲート本体 ----

// deniedLocation は、拒否ページのURLに、検証済みのIPを ?ip= で付ける（URLエンコード済み）。
// 受け取る静的ページ側では、この値を「信用できない入力」として扱うこと（textContent で表示する）。
func deniedLocation(base string, ip netip.Addr) string {
	if !ip.IsValid() {
		return base
	}
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	q.Set("ip", ip.String())
	u.RawQuery = q.Encode()
	return u.String()
}

type Gate struct {
	store       *Store
	next        http.Handler
	health      *healthChecker
	healthPath  string
	exactAllow  map[string]bool
	redirectURL string
	deniedURL   string
	enforce     bool
	logAllowed  bool
}

func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == g.healthPath {
		g.health.serve(w, r)
		return
	}

	now := time.Now()
	ip, ok := clientIP(r)
	d := decide(g.store.Get(), ip, ok, now)

	// 例外パス: 拒否リストのIPと、CFを通っていないリクエストには適用しない。
	if !d.Allowed && d.Reason != "denylist" && d.Reason != "no_valid_cf_ip" &&
		g.exactAllow[r.Method+" "+r.URL.Path] && cleanExact(r) {
		d = Decision{Allowed: true, Reason: "exact_exception"}
	}

	if !d.Allowed {
		tag := "GATE_WOULD_DENY"
		if g.enforce {
			tag = "GATE_DENY"
		}
		log.Printf("%s reason=%s ip=%q cf=%q method=%q path=%q ua=%q",
			tag, d.Reason, ip.String(),
			strings.Join(r.Header.Values("CF-Connecting-IP"), ","),
			r.Method, r.URL.Path, trunc(r.UserAgent(), 120))
		if g.enforce {
			g.reject(w, r, d, ip)
			return
		}
	} else if g.logAllowed {
		log.Printf("GATE_ALLOW reason=%s ip=%q method=%q path=%q",
			d.Reason, ip.String(), r.Method, r.URL.Path)
	}

	g.next.ServeHTTP(w, r)
}

func (g *Gate) reject(w http.ResponseWriter, r *http.Request, d Decision, ip netip.Addr) {
	w.Header().Set("Cache-Control", "no-store")
	switch d.Reason {
	case "policy_not_loaded":
		// 起動直後やDB障害のときは、案内ページへ飛ばさない
		w.Header().Set("Retry-After", "30")
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
	case "denylist":
		if isNavigation(r) {
			http.Redirect(w, r, deniedLocation(g.deniedURL, ip), http.StatusFound)
			return
		}
		http.Error(w, "Forbidden", http.StatusForbidden)
	case "not_in_allowlist":
		if isNavigation(r) {
			http.Redirect(w, r, g.redirectURL, http.StatusFound)
			return
		}
		http.Error(w, "Forbidden", http.StatusForbidden)
	default: // no_valid_cf_ip など
		http.Error(w, "Forbidden", http.StatusForbidden)
	}
}

// newProxy は、クライアントが付けた転送系ヘッダーをそのまま渡さず、検証済みのIPで付け直す。
func newProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host // Host は公開ドメインのまま渡す
			pr.Out.Header.Del("Forwarded")
			pr.Out.Header.Del("X-Real-Ip")
			if ip, ok := clientIP(pr.In); ok {
				pr.Out.Header.Set("X-Forwarded-For", ip.String())
			} else {
				pr.Out.Header.Del("X-Forwarded-For")
			}
			if v := pr.In.Header.Get("X-Forwarded-Proto"); v == "https" || v == "http" {
				pr.Out.Header.Set("X-Forwarded-Proto", v)
			}
			// CF-Connecting-IP は、アプリがログなどに使うため、そのまま残す。
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy error: %v", err)
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		},
	}
}

// ---- 起動と子プロセスの監視 ----

func main() {
	port := getenv("PORT", "3456")
	upstream := getenv("GATE_UPSTREAM", "127.0.0.1:3457")
	enforce := os.Getenv("GATE_MODE") == "enforce"

	db := DBConfig{
		Host:        os.Getenv("GATE_DB_HOST"),
		Port:        getenv("GATE_DB_PORT", "6543"),
		Name:        getenv("GATE_DB_NAME", "postgres"),
		User:        os.Getenv("GATE_DB_USER"),
		Password:    os.Getenv("GATE_DB_PASSWORD"),
		SSLMode:     getenv("GATE_DB_SSLMODE", "require"),
		SSLRootCert: os.Getenv("GATE_DB_SSLROOTCERT"),
	}
	hasDB := db.Host != "" && db.User != "" && db.Password != ""
	if enforce && !hasDB {
		log.Fatal("GATE_MODE=enforce には GATE_DB_HOST / GATE_DB_USER / GATE_DB_PASSWORD が必要です")
	}

	store := &Store{}
	ctx, stop := context.WithCancel(context.Background())
	if hasDB {
		go store.Run(ctx, db, 30*time.Second)
	} else {
		log.Printf("GATE_NO_DB GATE_DB_* が未設定のため、許可リストを読みません（観察のみ）")
	}

	mode := "observe"
	if enforce {
		mode = "enforce"
	}
	log.Printf("GATE_START mode=%s upstream=%s", mode, upstream)

	gate := &Gate{
		store: store,
		next:  newProxy(&url.URL{Scheme: "http", Host: upstream}),
		health: newHealthChecker(store, upstream,
			os.Getenv("GATE_UPSTREAM_HEALTH_PATH"),
			os.Getenv("GATE_HEALTH_REQUIRE_POLICY") == "1"),
		healthPath:  getenv("GATE_HEALTH_PATH", "/gate/healthz"),
		exactAllow:  parseExact(os.Getenv("GATE_EXACT_ALLOW")),
		redirectURL: getenv("GATE_REDIRECT_URL", "https://gate.jyrac.stki.org/"),
		deniedURL:   getenv("GATE_DENIED_URL", "https://gate.jyrac.stki.org/denied"),
		enforce:     enforce,
		logAllowed:  os.Getenv("GATE_LOG_ALLOWED") == "1",
	}

	server := &http.Server{
		Addr:              "0.0.0.0:" + port,
		Handler:           gate,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("GATE_LISTENING addr=0.0.0.0:%s", port)
		serverErr <- server.ListenAndServe()
	}()

	cmd := exec.Command("/app/vikunja/vikunja")
	// GATE_* は、Vikunja には渡さない（DBの資格情報などを、アプリの環境に出さない）
	cmd.Env = setEnv(dropEnvPrefix(os.Environ(), "GATE_"), "VIKUNJA_SERVICE_INTERFACE", upstream)
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

	exitCode := 0
	appExited := false

	select {
	case s := <-sig:
		log.Printf("received signal %s", s)

	case err := <-appDone:
		appExited = true
		exitCode = 1
		log.Printf("Vikunja exited: %v", err)

	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP server exited: %v", err)
			exitCode = 1
		}
	}

	stop()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = server.Shutdown(shutCtx)
	cancel()

	if !appExited {
		_ = cmd.Process.Signal(syscall.SIGTERM)

		select {
		case err := <-appDone:
			log.Printf("Vikunja stopped: %v", err)

		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-appDone
		}
	}

	os.Exit(exitCode)
}
