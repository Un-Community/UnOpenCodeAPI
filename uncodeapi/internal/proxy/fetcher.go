package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"uncodeapi/internal/logger"
)

type ProxyItem struct {
	Address  string
	IP       string
	Port     string
	Protocol string // socks5, socks4, https, http
}

func protocolPriority(p string) int {
	switch strings.ToLower(p) {
	case "socks5":
		return 4
	case "socks4":
		return 3
	case "https":
		return 2
	case "http":
		return 1
	default:
		return 0
	}
}

type Fetcher interface {
	Name() string
	Fetch(ctx context.Context) ([]ProxyItem, error)
}

// Support formats like:
// - "socks5://208.102.51.6:58208"
// - "208.102.51.6:58208:United States"
// - "208.102.51.6:58208"
var ipPortRegex = regexp.MustCompile(`(?:(socks5|socks4|https|http)://)?(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}):(\d{2,5})(?::[^\s\n\r]+)?`)

// ProxyScrape fetches proxies by protocol.
type ProxyScrape struct {
	Protocol string // http, socks4, socks5
}

func (p *ProxyScrape) Name() string {
	proto := p.Protocol
	if proto == "" {
		proto = "http"
	}
	return "proxyscrape:" + proto
}

func (p *ProxyScrape) Fetch(ctx context.Context) ([]ProxyItem, error) {
	proto := p.Protocol
	if proto == "" {
		proto = "http"
	}
	url := fmt.Sprintf("https://api.proxyscrape.com/v2/?request=displayproxies&protocol=%s&timeout=10000&country=all&ssl=all&anonymity=all", proto)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", "unoca/1.0")
	cli := &http.Client{Timeout: 30 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseIPPortItems(string(b), proto), nil
}

// FreeProxyList fetches from free-proxy-list.net (HTML scrape).
type FreeProxyList struct{}

func (FreeProxyList) Name() string { return "free-proxy-list" }

func (FreeProxyList) Fetch(ctx context.Context) ([]ProxyItem, error) {
	url := "https://free-proxy-list.net/"
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	cli := &http.Client{Timeout: 30 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseIPPortItems(string(b), "http"), nil
}

// GitHubProxyList fetches from github raw text.
type GitHubProxyList struct {
	URL      string
	Protocol string
}

func (g GitHubProxyList) Name() string {
	if idx := strings.LastIndex(g.URL, "/"); idx >= 0 {
		return "github:" + g.Protocol + ":" + g.URL[idx+1:]
	}
	return "github:" + g.Protocol
}

func (g GitHubProxyList) Fetch(ctx context.Context) ([]ProxyItem, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", g.URL, nil)
	req.Header.Set("User-Agent", "unoca/1.0")
	cli := &http.Client{Timeout: 30 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*64), 1024*1024)
	proto := g.Protocol
	if proto == "" {
		proto = "http"
	}

	var textBuilder strings.Builder
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		textBuilder.WriteString(line)
		textBuilder.WriteString("\n")
	}
	return parseIPPortItems(textBuilder.String(), proto), scanner.Err()
}

func parseIPPortItems(text, defaultProto string) []ProxyItem {
	matches := ipPortRegex.FindAllStringSubmatch(text, -1)
	out := make([]ProxyItem, 0, len(matches))
	seenIPs := make(map[string]ProxyItem)

	for _, m := range matches {
		prefixProto := m[1]
		ipStr := m[2]
		portStr := m[3]

		if isBogusOrPrivateIP(ipStr, portStr) {
			continue
		}

		proto := defaultProto
		if prefixProto != "" {
			proto = prefixProto
		}

		item := ProxyItem{
			Address:  ipStr + ":" + portStr,
			IP:       ipStr,
			Port:     portStr,
			Protocol: strings.ToLower(proto),
		}

		if existing, ok := seenIPs[ipStr]; ok {
			if protocolPriority(item.Protocol) > protocolPriority(existing.Protocol) {
				seenIPs[ipStr] = item
			}
		} else {
			seenIPs[ipStr] = item
		}
	}

	for _, item := range seenIPs {
		out = append(out, item)
	}
	return out
}

func isBogusOrPrivateIP(ipStr, portStr string) bool {
	if ipStr == "0.0.0.0" || ipStr == "255.255.255.255" {
		return true
	}
	if portStr == "0" || portStr == "65536" {
		return true
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return true
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return true
	}
	if ip4[0] == 0 || ip4[0] == 127 || ip4[0] == 10 {
		return true
	}
	if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
		return true
	}
	if ip4[0] == 192 && ip4[1] == 168 {
		return true
	}
	if ip4[0] == 169 && ip4[1] == 254 {
		return true
	}
	if ip4[0] >= 224 {
		return true
	}
	return false
}

// MultiFetcher combines multiple sources and deduplicates by IP with Protocol Priority (SOCKS5 > SOCKS4 > HTTPS > HTTP).
type MultiFetcher struct {
	Fetchers []Fetcher
}

func (m *MultiFetcher) Fetch(ctx context.Context) []ProxyItem {
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		out     []ProxyItem
		seenIPs = make(map[string]ProxyItem)
	)
	for _, f := range m.Fetchers {
		wg.Add(1)
		go func(f Fetcher) {
			defer wg.Done()
			ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			list, err := f.Fetch(ctx2)
			if err != nil {
				logger.Warn("fetcher %s error: %v", f.Name(), err)
				return
			}
			logger.Info("fetcher %s: got %d proxies", f.Name(), len(list))
			mu.Lock()
			for _, item := range list {
				if existing, ok := seenIPs[item.IP]; ok {
					if protocolPriority(item.Protocol) > protocolPriority(existing.Protocol) {
						seenIPs[item.IP] = item
					}
				} else {
					seenIPs[item.IP] = item
				}
			}
			mu.Unlock()
		}(f)
	}
	wg.Wait()

	for _, item := range seenIPs {
		out = append(out, item)
	}
	return out
}
