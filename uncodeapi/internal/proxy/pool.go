package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"uncodeapi/internal/config"
	"uncodeapi/internal/logger"
)

type Status string

const (
	StatusAlive     Status = "alive"
	StatusDead      Status = "dead"
	StatusSlow      Status = "slow"
	StatusUnknown   Status = "unknown"
	StatusRateLimit Status = "rate_limit"
	StatusTimeout   Status = "timeout"
)

type Proxy struct {
	Address   string    `json:"address"`
	Protocol  string    `json:"protocol"` // socks5, socks4, https, http
	Country   string    `json:"country"`  // ISO 2-letter code (e.g. "CN", "US")
	Flag      string    `json:"flag"`     // Unicode Flag Emoji (e.g. "🇨🇳")
	Status    Status    `json:"status"`
	LatencyMs int64     `json:"latency_ms"`
	LastCheck time.Time `json:"last_check"`
	LastUsed  time.Time `json:"last_used"`
	Fails     int       `json:"fails"`
	Successes int64     `json:"successes"`
}

func CountryToFlag(countryCode string) string {
	countryCode = strings.ToUpper(strings.TrimSpace(countryCode))
	if len(countryCode) != 2 {
		return "🌐"
	}
	r1 := rune(0x1F1E6) + rune(countryCode[0]-'A')
	r2 := rune(0x1F1E6) + rune(countryCode[1]-'A')
	return string([]rune{r1, r2})
}

func (p *Proxy) URL() string {
	proto := strings.ToLower(p.Protocol)
	if proto == "" {
		proto = "http"
	}
	return proto + "://" + p.Address
}

type Pool struct {
	cfg *config.Config

	mu      sync.RWMutex
	proxies []*Proxy
	counter uint64

	validating      bool
	validatedCount  int64
	totalToValidate int64
}

func NewPool(cfg *config.Config) *Pool {
	return &Pool{cfg: cfg}
}

func (p *Pool) Set(items []ProxyItem) {
	p.mu.Lock()
	defer p.mu.Unlock()

	existing := make(map[string]*Proxy)
	for _, pp := range p.proxies {
		existing[pp.Address] = pp
	}
	newList := make([]*Proxy, 0, len(items))
	for _, item := range items {
		if e, ok := existing[item.Address]; ok {
			if e.Status == StatusDead {
				e.Status = StatusUnknown
				e.Fails = 0
			}
			e.Protocol = item.Protocol
			e.LastCheck = time.Time{}
			newList = append(newList, e)
		} else {
			newList = append(newList, &Proxy{
				Address:   item.Address,
				Protocol:  item.Protocol,
				Flag:      "🌐",
				Status:    StatusUnknown,
				LastCheck: time.Time{},
			})
		}
	}
	p.proxies = newList
	logger.Info("proxy pool: %d entries", len(newList))
}

func (p *Pool) Pick() *Proxy {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var candidates []*Proxy
	for _, pp := range p.proxies {
		if pp.Status == StatusAlive || pp.Status == StatusUnknown || pp.Status == StatusSlow {
			candidates = append(candidates, pp)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	idx := atomic.AddUint64(&p.counter, 1) - 1
	return candidates[int(idx%uint64(len(candidates)))]
}

func (p *Pool) MarkResult(addr string, err error, latency time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pp := range p.proxies {
		if pp.Address != addr {
			continue
		}
		pp.LastUsed = time.Now()
		pp.LastCheck = time.Now()
		pp.LatencyMs = latency.Milliseconds()
		if err != nil {
			pp.Fails++
			errStr := strings.ToLower(err.Error())
			if strings.Contains(errStr, "rate limit") || strings.Contains(errStr, "429") {
				pp.Status = StatusRateLimit
			} else if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline exceeded") {
				pp.Status = StatusTimeout
			} else if pp.Fails >= 2 {
				pp.Status = StatusDead
			}
			return
		}
		pp.Fails = 0
		pp.Successes++
		if latency > 5*time.Second {
			pp.Status = StatusSlow
		} else {
			pp.Status = StatusAlive
		}
		return
	}
}

func (p *Pool) List() []Proxy {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Proxy, len(p.proxies))
	for i, pp := range p.proxies {
		out[i] = *pp
	}
	return out
}

func (p *Pool) Stats() (total, alive, dead int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, pp := range p.proxies {
		total++
		switch pp.Status {
		case StatusAlive, StatusSlow:
			alive++
		case StatusDead, StatusRateLimit, StatusTimeout:
			dead++
		}
	}
	return
}

func (p *Pool) SortedByLatency() []Proxy {
	list := p.List()
	sort.Slice(list, func(i, j int) bool { return list[i].LatencyMs < list[j].LatencyMs })
	return list
}

func (p *Pool) ValidationProgress() (validating bool, tested, total int64) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.validating, atomic.LoadInt64(&p.validatedCount), p.totalToValidate
}

func (p *Pool) ValidateAll(ctx context.Context) {
	p.mu.Lock()
	if p.validating {
		p.mu.Unlock()
		return
	}
	toTest := make([]*Proxy, 0)
	for _, pp := range p.proxies {
		if pp.Status != StatusAlive || time.Since(pp.LastCheck) > 5*time.Minute {
			toTest = append(toTest, pp)
		}
	}
	p.validating = true
	p.totalToValidate = int64(len(toTest))
	atomic.StoreInt64(&p.validatedCount, 0)
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.validating = false
		p.mu.Unlock()
	}()

	if len(toTest) == 0 {
		return
	}
	logger.Info("validating %d proxies...", len(toTest))

	sem := make(chan struct{}, p.cfg.ProxyMaxConcurrent)
	var wg sync.WaitGroup
	for _, pp := range toTest {
		pp := pp
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() {
				<-sem
				atomic.AddInt64(&p.validatedCount, 1)
			}()
			p.validate(ctx, pp)
		}()
	}
	wg.Wait()
}

var testPrompts = []string{
	"1+1=?",
	"What colors are on the United States flag?",
	"What is the capital of France?",
	"Reply with 'OK'",
}

func getRandomPrompt() string {
	return testPrompts[time.Now().UnixNano()%int64(len(testPrompts))]
}

func (p *Pool) validate(ctx context.Context, pp *Proxy) {
	proxyURL, err := url.Parse(pp.URL())
	if err != nil {
		p.MarkResult(pp.Address, err, 0)
		return
	}

	tr := &http.Transport{
		Proxy:                 http.ProxyURL(proxyURL),
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
		}).DialContext,
	}
	cli := &http.Client{Transport: tr, Timeout: 6 * time.Second}
	start := time.Now()

	// STEP 1: Proxy connectivity & Country lookup via ipinfo.io/json
	ipinfoReq, _ := http.NewRequestWithContext(ctx, "GET", "https://ipinfo.io/json", nil)
	ipinfoReq.Header.Set("User-Agent", "curl/7.88.1")

	ipinfoResp, err := cli.Do(ipinfoReq)
	step1Latency := time.Since(start)
	if err != nil {
		// Timeout vs Connection failure distinction
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline exceeded") {
			p.mu.Lock()
			pp.Status = StatusTimeout
			pp.Fails++
			p.mu.Unlock()
		} else {
			p.MarkResult(pp.Address, err, step1Latency)
		}
		return
	}
	defer ipinfoResp.Body.Close()

	if ipinfoResp.StatusCode == 200 {
		var geo struct {
			Country string `json:"country"`
		}
		if err := json.NewDecoder(ipinfoResp.Body).Decode(&geo); err == nil && geo.Country != "" {
			p.mu.Lock()
			pp.Country = geo.Country
			pp.Flag = CountryToFlag(geo.Country)
			p.mu.Unlock()
		}
	}

	// STEP 2: AI Model Validation (OpenCode Chat Completions)
	prompt := getRandomPrompt()
	payload := map[string]any{
		"model": p.cfg.OpenCodeModel,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens": 10,
	}
	bodyBytes, _ := json.Marshal(payload)

	aiReq, _ := http.NewRequestWithContext(ctx, "POST", p.cfg.OpenCodeBaseURL+"/zen/v1/chat/completions", bytes.NewReader(bodyBytes))
	aiReq.Header.Set("Content-Type", "application/json")
	aiReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	aiReq.Header.Set("Authorization", "Bearer public")
	aiReq.Header.Set("Connection", "close")

	aiStart := time.Now()
	aiResp, err := cli.Do(aiReq)
	totalLatency := time.Since(aiStart)

	if err != nil {
		p.MarkResult(pp.Address, err, totalLatency)
		return
	}
	defer aiResp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(aiResp.Body, 4096))
	bodyStr := string(respBody)

	if aiResp.StatusCode == 429 || strings.Contains(bodyStr, "FreeUsageLimitError") || strings.Contains(bodyStr, "Rate limit exceeded") {
		logger.Warn("Validator: proxy %s hit rate limit (%s)", pp.Address, bodyStr)
		p.MarkResult(pp.Address, fmt.Errorf("rate limit: %s", bodyStr), totalLatency)
		return
	}

	if aiResp.StatusCode >= 400 {
		p.MarkResult(pp.Address, fmt.Errorf("status %d: %s", aiResp.StatusCode, bodyStr), totalLatency)
		return
	}

	p.MarkResult(pp.Address, nil, totalLatency)
}
