package web

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"sort"
	"strconv"

	"uncodeapi/internal/config"
	"uncodeapi/internal/logger"
	"uncodeapi/internal/opencode"
	"uncodeapi/internal/proxy"
)

//go:embed static/* templates/*
var assets embed.FS

type Trigger interface {
	Refresh(ctx context.Context)
	ValidateAll(ctx context.Context)
}

type StatsProvider interface {
	GetStats() (reqs, errs, inTok, outTok uint64, hourly [24]uint64, daily map[string]uint64)
}

type Admin struct {
	cfg     *config.Config
	pool    *proxy.Pool
	client  *opencode.Client
	trigger Trigger
	stats   StatsProvider
	apiKey  string
	tmpl    *template.Template
}

func NewAdmin(cfg *config.Config, pool *proxy.Pool, client *opencode.Client, trigger Trigger, stats StatsProvider, apiKey string) (*Admin, error) {
	t, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Admin{cfg: cfg, pool: pool, client: client, trigger: trigger, stats: stats, apiKey: apiKey, tmpl: t}, nil
}

func (a *Admin) Routes(mux *http.ServeMux) {
	// Root redirect
	mux.HandleFunc("/", a.handleRoot)

	// Dashboard pages
	mux.HandleFunc("/dashboard/endpoint", a.handleDashboard("endpoint"))
	mux.HandleFunc("/dashboard/usage", a.handleDashboard("usage"))
	mux.HandleFunc("/dashboard/logs", a.handleDashboard("logs"))
	mux.HandleFunc("/dashboard/proxies", a.handleDashboard("proxies"))

	// JSON API
	mux.HandleFunc("/api/stats", a.apiStats)
	mux.HandleFunc("/api/proxies", a.apiProxies)
	mux.HandleFunc("/api/logs", a.apiLogs)
	mux.HandleFunc("/api/refresh", a.apiRefresh)
	mux.HandleFunc("/api/validate", a.apiValidate)

	// Static assets
	staticFS, _ := fs.Sub(assets, "static")
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
}

func (a *Admin) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "/dashboard" || r.URL.Path == "/dashboard/" {
		http.Redirect(w, r, "/dashboard/endpoint", http.StatusFound)
		return
	}
	http.NotFound(w, r)
}

func (a *Admin) handleDashboard(tab string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{
			"Title":     "unoca — UnOpenCodeAPI",
			"ActiveTab": tab,
			"APIKey":    a.apiKey,
			"Port":      a.cfg.Port,
			"BaseURL":   a.cfg.OpenCodeBaseURL,
			"Model":     a.cfg.OpenCodeModel,
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = a.tmpl.ExecuteTemplate(w, "layout.html", data)
	}
}

func (a *Admin) apiStats(w http.ResponseWriter, r *http.Request) {
	total, alive, dead := a.pool.Stats()
	validating, tested, toValidate := a.pool.ValidationProgress()
	list := a.pool.List()
	var totalSuccess, totalFails uint64
	for _, p := range list {
		totalSuccess += uint64(p.Successes)
		totalFails += uint64(p.Fails)
	}

	reqs, errs, inTok, outTok := uint64(0), uint64(0), uint64(0), uint64(0)
	var hourly [24]uint64
	var daily map[string]uint64
	if a.stats != nil {
		reqs, errs, inTok, outTok, hourly, daily = a.stats.GetStats()
	}

	// Standard estimate: $0.0015 per 1K input tokens, $0.002 per 1K output tokens
	estCost := (float64(inTok) / 1000.0 * 0.0015) + (float64(outTok) / 1000.0 * 0.002)

	writeJSON(w, map[string]any{
		"requests":      reqs,
		"errors":        errs,
		"input_tokens":  inTok,
		"output_tokens": outTok,
		"total_tokens":  inTok + outTok,
		"est_cost":      estCost,
		"hourly_reqs":   hourly,
		"daily_reqs":    daily,
		"proxy_total":   total,
		"proxy_alive":   alive,
		"proxy_dead":    dead,
		"proxy_unknown": total - alive - dead,
		"successes":     totalSuccess,
		"fails":         totalFails,
		"recent_logs":   len(logger.Recent(500)),
		"validation": map[string]any{
			"validating":  validating,
			"tested":      tested,
			"to_validate": toValidate,
		},
	})
}

func (a *Admin) apiProxies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 1)
	pageSize := atoiDefault(q.Get("pageSize"), 50)
	if pageSize <= 0 || pageSize > 500 {
		pageSize = 50
	}
	if page <= 0 {
		page = 1
	}
	sortKey := q.Get("sort")
	if sortKey == "" {
		sortKey = "latency"
	}
	dir := q.Get("dir")
	if dir == "" {
		dir = "asc"
	}
	statusFilter := q.Get("status")

	list := a.pool.List()
	if statusFilter != "" {
		filtered := list[:0]
		for _, p := range list {
			if string(p.Status) == statusFilter {
				filtered = append(filtered, p)
			}
		}
		list = filtered
	}

	sortProxies(list, sortKey, dir)

	total := len(list)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	items := list[start:end]
	if items == nil {
		items = []proxy.Proxy{}
	}

	writeJSON(w, map[string]any{
		"items":    items,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
		"pages":    (total + pageSize - 1) / pageSize,
		"sort":     sortKey,
		"dir":      dir,
		"status":   statusFilter,
	})
}

func sortProxies(list []proxy.Proxy, key, dir string) {
	asc := dir != "desc"
	less := func(i, j int) bool {
		switch key {
		case "address":
			return list[i].Address < list[j].Address
		case "protocol":
			return list[i].Protocol < list[j].Protocol
		case "status":
			return string(list[i].Status) < string(list[j].Status)
		case "latency":
			return list[i].LatencyMs < list[j].LatencyMs
		case "fails":
			return list[i].Fails < list[j].Fails
		case "successes":
			return list[i].Successes < list[j].Successes
		case "last_check":
			return list[i].LastCheck.Before(list[j].LastCheck)
		default:
			return list[i].LatencyMs < list[j].LatencyMs
		}
	}
	if !asc {
		old := less
		less = func(i, j int) bool { return old(j, i) }
	}
	sort.SliceStable(list, less)
}

func (a *Admin) apiLogs(w http.ResponseWriter, r *http.Request) {
	entries := logger.Recent(200)
	if entries == nil {
		entries = []logger.LogEntry{}
	}
	writeJSON(w, entries)
}

func (a *Admin) apiRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	go func() {
		a.trigger.Refresh(context.Background())
	}()
	writeJSON(w, map[string]string{"status": "scheduled"})
}

func (a *Admin) apiValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	go func() {
		a.trigger.ValidateAll(context.Background())
	}()
	writeJSON(w, map[string]string{"status": "scheduled"})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
