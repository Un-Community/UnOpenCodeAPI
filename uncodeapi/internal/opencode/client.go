package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"uncodeapi/internal/config"
	"uncodeapi/internal/logger"
	"uncodeapi/internal/proxy"
)

const Bearer = "Bearer public"

// ChatRequest is the OpenAI-style chat completion request body.
type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	Tools       []Tool    `json:"tools,omitempty"`
	ToolChoice  any       `json:"tool_choice,omitempty"`
}

type Message struct {
	Role    string         `json:"role"`
	Content MessageContent `json:"content"`
	Name    string         `json:"name,omitempty"`
}

type MessageContent string

func (mc *MessageContent) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*mc = MessageContent(s)
		return nil
	}

	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &parts); err == nil {
		var fullText strings.Builder
		for i, p := range parts {
			if p.Text != "" {
				if i > 0 {
					fullText.WriteString("\n")
				}
				fullText.WriteString(p.Text)
			}
		}
		*mc = MessageContent(fullText.String())
		return nil
	}

	*mc = MessageContent(string(data))
	return nil
}

func (mc MessageContent) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(mc))
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// ChatResponse is the OpenAI-style non-streaming response.
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Model is one entry from /v1/models.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ModelsResponse is /v1/models response.
type ModelsResponse struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// ChatStream is the callback for each SSE chunk parsed.
type ChatStream func(chunk string, data []byte) error

// Client talks to opencode.ai/zen via a rotating proxy pool.
type Client struct {
	cfg  *config.Config
	pool *proxy.Pool
	cli  *http.Client
}

func NewClient(cfg *config.Config, pool *proxy.Pool) *Client {
	return &Client{cfg: cfg, pool: pool}
}

// doWithRetry performs an HTTP request through successive proxies from the pool
// until one succeeds or all fail. For streaming the first attempt is used.
func (c *Client) doWithRetry(ctx context.Context, req *http.Request, body []byte, streaming bool) (*http.Response, *proxy.Proxy, error) {
	if c.pool == nil {
		// Direct mode (no proxy)
		cli := &http.Client{Timeout: c.cfg.OpenCodeTimeout}
		req2 := req.Clone(ctx)
		if body != nil {
			req2.Body = io.NopCloser(bytes.NewReader(body))
			req2.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
			req2.ContentLength = int64(len(body))
		}
		resp, err := cli.Do(req2)
		return resp, nil, err
	}

	// Try up to N different proxies.
	tries := 4
	if streaming {
		tries = 1 // streaming connections can't easily retry mid-stream
	}
	var lastErr error
	for i := 0; i < tries; i++ {
		p := c.pool.Pick()
		if p == nil {
			lastErr = errors.New("no proxy available")
			break
		}
		tr := &http.Transport{
			Proxy:                 http.ProxyURL(mustParseURL(p.URL())),
			DisableKeepAlives:     true, // Prevent 'unexpected EOF' on reused proxy sockets
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			DialContext: (&net.Dialer{
				Timeout:   15 * time.Second,
				KeepAlive: 0,
			}).DialContext,
		}
		cli := &http.Client{Transport: tr, Timeout: c.cfg.OpenCodeTimeout}
		req2 := req.Clone(ctx)
		req2.RequestURI = ""
		if body != nil {
			req2.Body = io.NopCloser(bytes.NewReader(body))
			req2.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
			req2.ContentLength = int64(len(body))
		}
		start := time.Now()
		resp, err := cli.Do(req2)
		latency := time.Since(start)
		if err != nil {
			logger.Warn("proxy %s error: %v", p.Address, err)
			c.pool.MarkResult(p.Address, err, latency)
			lastErr = err
			continue
		}
		// 429 = rate limit; treat as proxy failure so we rotate.
		if resp.StatusCode == 429 {
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			msg := string(bodyBytes)
			logger.Warn("[%d]: %s (Proxy: %s)", resp.StatusCode, msg, p.Address)
			c.pool.MarkResult(p.Address, fmt.Errorf("rate limit: %s", msg), latency)
			lastErr = fmt.Errorf("rate limit: %s", msg)
			continue
		}
		if resp.StatusCode != 200 {
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			msg := string(bodyBytes)
			logger.Error("[%d]: %s (Proxy: %s)", resp.StatusCode, msg, p.Address)
			c.pool.MarkResult(p.Address, fmt.Errorf("status %d: %s", resp.StatusCode, msg), latency)
			lastErr = fmt.Errorf("upstream %d: %s", resp.StatusCode, msg)
			continue
		}
		c.pool.MarkResult(p.Address, nil, latency)
		return resp, p, nil
	}
	return nil, nil, lastErr
}

func mustParseURL(s string) *url.URL {
	u, _ := url.Parse(s)
	return u
}

// ChatCompletion does a non-streaming chat completion.
func (c *Client) ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, *proxy.Proxy, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, err
	}
	httpReq, _ := http.NewRequestWithContext(ctx, "POST",
		c.cfg.OpenCodeBaseURL+"/zen/v1/chat/completions", nil)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", Bearer)
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	httpReq.Header.Set("x-opencode-client", "unoca")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Accept-Encoding", "identity")
	httpReq.Header.Set("Connection", "close")

	resp, usedProxy, err := c.doWithRetry(ctx, httpReq, body, false)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, usedProxy, fmt.Errorf("upstream %d: %s", resp.StatusCode, string(b))
	}
	var out ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, usedProxy, err
	}
	return &out, usedProxy, nil
}

// ChatCompletionStream streams chunks via SSE. Returns the proxy used.
func (c *Client) ChatCompletionStream(ctx context.Context, req *ChatRequest, cb ChatStream) (*proxy.Proxy, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, _ := http.NewRequestWithContext(ctx, "POST",
		c.cfg.OpenCodeBaseURL+"/zen/v1/chat/completions", nil)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", Bearer)
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	httpReq.Header.Set("x-opencode-client", "unoca")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Accept-Encoding", "identity")
	httpReq.Header.Set("Connection", "close")

	resp, usedProxy, err := c.doWithRetry(ctx, httpReq, body, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return usedProxy, fmt.Errorf("upstream %d: %s", resp.StatusCode, string(b))
	}
	return usedProxy, readSSE(resp.Body, cb)
}

func readSSE(r io.Reader, cb ChatStream) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*64), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "" || data == "[DONE]" {
			continue
		}
		if err := cb(line, []byte(data)); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// ListModels calls /zen/v1/models via the pool.
func (c *Client) ListModels(ctx context.Context) (*ModelsResponse, error) {
	httpReq, _ := http.NewRequestWithContext(ctx, "GET",
		c.cfg.OpenCodeBaseURL+"/zen/v1/models", nil)
	httpReq.Header.Set("Authorization", Bearer)
	httpReq.Header.Set("User-Agent", "unoca/1.0")
	resp, _, err := c.doWithRetry(ctx, httpReq, nil, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, string(b))
	}
	var out ModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}
