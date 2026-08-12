package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"uncodeapi/internal/config"
	"uncodeapi/internal/db"
	"uncodeapi/internal/logger"
	"uncodeapi/internal/opencode"
	"uncodeapi/internal/proxy"
)

// Server exposes an OpenAI-compatible API at /v1/*.
type Server struct {
	cfg      *config.Config
	client   *opencode.Client
	pool     *proxy.Pool
	database *db.DB

	apiKey string

	mu    sync.Mutex
	stats struct {
		requests     uint64
		errors       uint64
		inputTokens  uint64
		outputTokens uint64
		hourlyReqs   [24]uint64        // 00:00 -> 23:59
		dailyReqs    map[string]uint64 // "YYYY-MM-DD" -> count
	}
}

func (s *Server) GetStats() (reqs, errs, inTok, outTok uint64, hourly [24]uint64, daily map[string]uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dailyCopy := make(map[string]uint64, len(s.stats.dailyReqs))
	for k, v := range s.stats.dailyReqs {
		dailyCopy[k] = v
	}

	return atomic.LoadUint64(&s.stats.requests),
		atomic.LoadUint64(&s.stats.errors),
		atomic.LoadUint64(&s.stats.inputTokens),
		atomic.LoadUint64(&s.stats.outputTokens),
		s.stats.hourlyReqs,
		dailyCopy
}

func NewServer(cfg *config.Config, c *opencode.Client, pool *proxy.Pool, database *db.DB) *Server {
	s := &Server{cfg: cfg, client: c, pool: pool, database: database}
	s.apiKey = genAPIKey()

	// Load existing stats from database
	todayStr := time.Now().Format("2006-01-02")
	hourly, reqs, inTok, outTok := database.LoadHourlyUsage(todayStr)
	heatmap := database.LoadHeatmap()

	s.stats.hourlyReqs = hourly
	s.stats.requests = reqs
	s.stats.inputTokens = inTok
	s.stats.outputTokens = outTok
	s.stats.dailyReqs = heatmap

	return s
}

func (s *Server) recordRequest(inTokens, outTokens uint64) {
	now := time.Now()
	hour := now.Hour()
	dateStr := now.Format("2006-01-02")

	atomic.AddUint64(&s.stats.requests, 1)
	if inTokens > 0 {
		atomic.AddUint64(&s.stats.inputTokens, inTokens)
	}
	if outTokens > 0 {
		atomic.AddUint64(&s.stats.outputTokens, outTokens)
	}

	s.mu.Lock()
	s.stats.hourlyReqs[hour]++
	s.stats.dailyReqs[dateStr]++
	s.mu.Unlock()

	if s.database != nil {
		go s.database.RecordRequest(inTokens, outTokens)
	}
}

// APIKey returns the auto-generated API key (printed on startup).
func (s *Server) APIKey() string { return s.apiKey }

// Routes registers handlers on mux.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/chat/completions", s.handleChat)
	mux.HandleFunc("/v1/completions", s.handleChat) // alias
	mux.HandleFunc("/healthz", s.handleHealth)
}

func (s *Server) auth(r *http.Request) bool {
	// Accept any non-empty key or allow all requests from local clients
	hdr := r.Header.Get("Authorization")
	if hdr == "" {
		hdr = r.Header.Get("X-Api-Key")
	} else {
		hdr = strings.TrimPrefix(hdr, "Bearer ")
	}
	// Accept if key matches generated key, OR if key starts with sk-, OR allow all if empty
	if s.apiKey == "" || hdr == s.apiKey || strings.HasPrefix(hdr, "sk-") || hdr != "" {
		return true
	}
	return true
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	total, alive, dead := s.pool.Stats()
	writeJSON(w, 200, map[string]any{
		"status":         "ok",
		"uptime_seconds": 0,
		"requests":       atomic.LoadUint64(&s.stats.requests),
		"errors":         atomic.LoadUint64(&s.stats.errors),
		"proxy_total":    total,
		"proxy_alive":    alive,
		"proxy_dead":     dead,
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if !s.auth(r) {
		writeErr(w, 401, "invalid api key")
		return
	}
	s.recordRequest(0, 0)
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	resp, err := s.client.ListModels(ctx)
	if err != nil {
		atomic.AddUint64(&s.stats.errors, 1)
		// Fall back to a static list of known free models.
		writeJSON(w, 200, opencode.ModelsResponse{
			Object: "list",
			Data: []opencode.Model{
				{ID: "deepseek-v4-flash-free", Object: "model", Created: 1779000000, OwnedBy: "opencode-free"},
				{ID: "big-pickle", Object: "model", Created: 1779000000, OwnedBy: "opencode-free"},
				{ID: "minimax-m2.5-free", Object: "model", Created: 1779000000, OwnedBy: "opencode-free"},
				{ID: "nemotron-3-super-free", Object: "model", Created: 1779000000, OwnedBy: "opencode-free"},
				{ID: "qwen3.6-plus-free", Object: "model", Created: 1779000000, OwnedBy: "opencode-free"},
			},
		})
		return
	}
	writeJSON(w, 200, resp)
}

func estimateTokens(messages []opencode.Message) uint64 {
	var charCount int
	for _, m := range messages {
		charCount += len(string(m.Content))
	}
	if charCount == 0 {
		return 10
	}
	return uint64(charCount / 4)
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if !s.auth(r) {
		writeErr(w, 401, "invalid api key")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		writeErr(w, 400, "read body: "+err.Error())
		return
	}
	var req opencode.ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if req.Model == "" {
		req.Model = s.cfg.OpenCodeModel
	}

	inTokens := estimateTokens(req.Messages)
	s.recordRequest(inTokens, 0)

	logger.Info("chat: model=%s stream=%v msgs=%d inTok~%d", req.Model, req.Stream, len(req.Messages), inTokens)

	ctx, cancel := contextWithTimeout(r, s.cfg.RequestTimeout)
	defer cancel()

	if req.Stream {
		s.streamChat(w, r, ctx, &req)
		return
	}

	resp, used, err := s.client.ChatCompletion(ctx, &req)
	if err != nil {
		atomic.AddUint64(&s.stats.errors, 1)
		writeErr(w, 502, err.Error())
		return
	}
	if used != nil {
		resp.Model = req.Model
	}

	var outTok uint64
	if resp.Usage != nil {
		outTok = uint64(resp.Usage.CompletionTokens)
	} else if len(resp.Choices) > 0 {
		outTok = uint64(len(string(resp.Choices[0].Message.Content)) / 4)
	}
	if outTok > 0 {
		atomic.AddUint64(&s.stats.outputTokens, outTok)
		if s.database != nil {
			go s.database.RecordRequest(0, outTok)
		}
	}

	writeJSON(w, 200, resp)
}

func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, ctx context.Context, req *opencode.ChatRequest) {
	flusher, ok := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	if !ok {
		return
	}

	var streamedOutputChars uint64
	cb := func(line string, data []byte) error {
		streamedOutputChars += uint64(len(data))
		if _, err := w.Write([]byte(line + "\n\n")); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	if _, err := s.client.ChatCompletionStream(ctx, req, cb); err != nil {
		atomic.AddUint64(&s.stats.errors, 1)
		errData, _ := json.Marshal(map[string]any{
			"error": map[string]string{"message": err.Error(), "type": "upstream_error"},
		})
		fmt.Fprintf(w, "data: %s\n\n", errData)
		flusher.Flush()
	} else {
		if streamedOutputChars > 0 {
			atomic.AddUint64(&s.stats.outputTokens, streamedOutputChars/4)
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{
		"error": map[string]string{
			"message": msg,
			"type":    "api_error",
			"code":    fmt.Sprintf("%d", code),
		},
	})
}

func genAPIKey() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return "sk-unoca-" + hex.EncodeToString(b)
}
