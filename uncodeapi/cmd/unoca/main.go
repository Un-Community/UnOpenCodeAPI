// unoca — UnOpenCodeAPI
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"uncodeapi/internal/api"
	"uncodeapi/internal/config"
	"uncodeapi/internal/db"
	"uncodeapi/internal/logger"
	"uncodeapi/internal/opencode"
	"uncodeapi/internal/proxy"
	"uncodeapi/internal/web"
)

func main() {
	cfg := config.Load()
	logger.Init(logger.LevelInfo)

	// Init SQLite DB (unoca.db) & prune expired data BEFORE banner()
	database, err := db.InitDB()
	if err != nil {
		fmt.Fprintln(os.Stderr, "database init error:", err)
	} else {
		defer database.Close()
	}

	fmt.Println(banner())

	// 1. Proxy pool + manager
	pool := proxy.NewPool(cfg)
	fetcher := &proxy.MultiFetcher{
		Fetchers: defaultProxyFetchers(),
	}
	mgr := proxy.NewManager(cfg, pool, fetcher)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go mgr.Run(ctx)

	// 2. OpenCode client & API server
	oc := opencode.NewClient(cfg, pool)
	apiSrv := api.NewServer(cfg, oc, pool, database)

	// Show service URLs / API info
	addr := fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
	fmt.Println(infoBox("Service URLs", [][2]string{
		{"Dashboard", addr + "/dashboard/endpoint"},
		{"API base", addr + "/v1"},
		{"Models", addr + "/v1/models"},
		{"Chat", addr + "/v1/chat/completions"},
		{"Health", addr + "/healthz"},
		{"API Key", apiSrv.APIKey()},
	}))
	fmt.Println()

	// Combined Mux (Single Port 20120)
	mainMux := http.NewServeMux()

	// Register API endpoints (/v1/models, /v1/chat/completions, /healthz)
	apiSrv.Routes(mainMux)

	// Register Admin UI endpoints (/dashboard/*, /api/*, /static/*, / -> redirect /dashboard/endpoint)
	admin, err := web.NewAdmin(cfg, pool, oc, mgr, apiSrv, apiSrv.APIKey())
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin init error:", err)
		os.Exit(1)
	}
	admin.Routes(mainMux)

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           mainMux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("Server running on http://localhost:%d", cfg.Port)
		logger.Info("Dashboard: http://localhost:%d/dashboard/endpoint", cfg.Port)
		logger.Info("API Key: %s", apiSrv.APIKey())
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error: %v", err)
		}
	}()

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	logger.Info("shutting down...")
	mgr.Stop()
	shutdownCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	_ = server.Shutdown(shutdownCtx)
}

// infoBox renders a small ASCII box containing key/value pairs.
func infoBox(title string, rows [][2]string) string {
	const (
		dim    = "\033[2m"
		cyan   = "\033[36m"
		bold   = "\033[1m"
		green  = "\033[38;2;34;255;136m"
		orange = "\033[38;2;255;140;26m"
		reset  = "\033[0m"
	)

	// innerWidth = number of cells between the two vertical bars (│ ... │).
	keyWidth := 0
	for _, r := range rows {
		if len(r[0]) > keyWidth {
			keyWidth = len(r[0])
		}
	}
	innerWidth := 1 + len(title) // "  <title>" (leading space + title)
	for _, r := range rows {
		w := 1 + keyWidth + 1 + len(r[1]) // " " + key + " " + value
		if w > innerWidth {
			innerWidth = w
		}
	}

	rule := strings.Repeat("─", innerWidth)
	top := "  " + orange + "┌" + rule + "┐" + reset
	bottom := "  " + orange + "└" + rule + "┘" + reset

	headerLine := "  " + orange + "│" + reset + " "
	headerLine += bold + orange + title + reset
	headerLine += strings.Repeat(" ", innerWidth-1-len(title)) + orange + "│" + reset

	var sb strings.Builder
	sb.WriteString(top + "\n")
	sb.WriteString(headerLine + "\n")
	for _, r := range rows {
		line := "  " + orange + "│" + reset + " "
		line += dim + green + fmt.Sprintf("%-*s", keyWidth, r[0]) + reset
		line += dim + " " + reset + cyan + r[1] + reset
		tail := innerWidth - 1 - keyWidth - 1 - len(r[1])
		if tail < 0 {
			tail = 0
		}
		line += strings.Repeat(" ", tail) + orange + "│" + reset
		sb.WriteString(line + "\n")
	}
	sb.WriteString(bottom)
	return sb.String()
}

// hex parses a "#RRGGBB" color string into r, g, b components (0-255).
func hex(c string) (int, int, int) {
	var r, g, b int
	if _, err := fmt.Sscanf(c, "#%02x%02x%02x", &r, &g, &b); err != nil {
		return 255, 255, 255
	}
	return r, g, b
}

// ansiRGB returns a 24-bit foreground ANSI escape sequence.
func ansiRGB(r, g, b int) string {
	return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
}

// gradient paints `text` with a smooth color gradient from `from` to `to`,
// skipping whitespace so leading/trailing spaces don't break alignment.
func gradient(text, from, to string) string {
	r1, g1, b1 := hex(from)
	r2, g2, b2 := hex(to)

	total := 0
	for _, r := range text {
		if r != ' ' && r != '\n' && r != '\t' {
			total++
		}
	}
	if total == 0 {
		return text
	}
	if total == 1 {
		return ansiRGB(r1, g1, b1) + text + "\033[0m"
	}

	var sb strings.Builder
	visible := 0
	for _, r := range text {
		if r == ' ' || r == '\n' || r == '\t' {
			sb.WriteRune(r)
			continue
		}
		t := float64(visible) / float64(total-1)
		rr := int(float64(r1) + float64(r2-r1)*t)
		gg := int(float64(g1) + float64(g2-g1)*t)
		bb := int(float64(b1) + float64(b2-b1)*t)
		sb.WriteString(ansiRGB(rr, gg, bb))
		sb.WriteRune(r)
		visible++
	}
	sb.WriteString("\033[0m")
	return sb.String()
}

func banner() string {
	const subtext = `UnOpenCodeAPI v0.1 — rotate free proxies, talk to OpenCode Zen.`

	art := `
    ██    ██ ███    ██  ██████  ██████  █████
    ██    ██ ████   ██ ██    ██ ██     ██   ██
    ██    ██ ██ ██  ██ ██    ██ ██     ███████
    ██    ██ ██  ██ ██ ██    ██ ██     ██   ██
    ███████  ██   ████  ██████  ██████ ██   ██
	`

	// Smooth gradient: green (#22ff88) -> orange (#ff8c1a) across visible chars.
	return gradient(art, "#22ff88", "#ff8c1a") + "\n\n" + subtext + "\033[0m\n"
}
