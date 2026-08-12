package main

import "uncodeapi/internal/proxy"

// githubProxySource mô tả một repo GitHub chứa các file proxy list,
// mỗi protocol được map sang tên file tương ứng trong repo đó.
type githubProxySource struct {
	BaseURL string            // raw URL đến thư mục chứa các file (không bao gồm tên file)
	Files   map[string]string // protocol -> filename
}

// githubProxySources là danh sách các repo GitHub được dùng để fetch free proxy.
// Gộp theo repo giúp tránh lặp base URL và dễ thêm/bớt nguồn.
var githubProxySources = []githubProxySource{
	{
		BaseURL: "https://raw.githubusercontent.com/proxifly/free-proxy-list/main/proxies/protocols",
		Files: map[string]string{
			"socks5": "socks5/data.txt",
			"socks4": "socks4/data.txt",
			"http":   "http/data.txt",
			"https":  "https/data.txt",
		},
	},
	{
		BaseURL: "https://raw.githubusercontent.com/zloi-user/hideip.me/main",
		Files: map[string]string{
			"socks5": "socks5.txt",
			"socks4": "socks4.txt",
			"http":   "http.txt",
			"https":  "https.txt",
		},
	},
	{
		BaseURL: "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master",
		Files: map[string]string{
			"socks5": "socks5.txt",
			"socks4": "socks4.txt",
			"http":   "http.txt",
		},
	},
	{
		BaseURL: "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies",
		Files: map[string]string{
			"socks5": "socks5.txt",
			"socks4": "socks4.txt",
			"http":   "http.txt",
		},
	},
	{
		BaseURL: "https://raw.githubusercontent.com/ShiftyTR/Proxy-List/master",
		Files: map[string]string{
			"socks5": "socks5.txt",
			"socks4": "socks4.txt",
			"http":   "http.txt",
			"https":  "https.txt",
		},
	},
	{
		BaseURL: "https://raw.githubusercontent.com/hookzof/socks5_list/master",
		Files: map[string]string{
			"socks5": "proxy.txt",
		},
	},
	{
		BaseURL: "https://raw.githubusercontent.com/roosterkid/openproxylist/main",
		Files: map[string]string{
			"socks5": "SOCKS5_RAW.txt",
			"socks4": "SOCKS4_RAW.txt",
			"https":  "HTTPS_RAW.txt",
		},
	},
	{
		BaseURL: "https://raw.githubusercontent.com/iplocate/free-proxy-list/refs/heads/main/protocols",
		Files: map[string]string{
			"socks5": "socks5.txt",
			"socks4": "socks4.txt",
			"http":   "http.txt",
			"https":  "https.txt",
		},
	},
	{
		BaseURL: "https://raw.githubusercontent.com/r00tee/Proxy-List/main",
		Files: map[string]string{
			"socks5": "Socks5.txt",
			"socks4": "Socks4.txt",
			"https":  "Https.txt",
		},
	},
	{
		BaseURL: "https://raw.githubusercontent.com/SevenworksDev/proxy-list/refs/heads/main/proxies",
		Files: map[string]string{
			"socks5": "socks5.txt",
			"socks4": "socks4.txt",
			"http":   "http.txt",
			"https":  "https.txt",
		},
	},
}

// defaultProxyFetchers trả về danh sách các fetcher mặc định được dùng bởi
// MultiFetcher, gồm ProxyScrape, FreeProxyList và toàn bộ GitHub proxy sources.
func defaultProxyFetchers() []proxy.Fetcher {
	fetchers := []proxy.Fetcher{
		// ProxyScrape Sources (HTTP, SOCKS4, SOCKS5)
		&proxy.ProxyScrape{Protocol: "socks5"},
		&proxy.ProxyScrape{Protocol: "socks4"},
		&proxy.ProxyScrape{Protocol: "http"},

		// FreeProxyList (HTTP)
		proxy.FreeProxyList{},
	}

	for _, src := range githubProxySources {
		for proto, file := range src.Files {
			fetchers = append(fetchers, proxy.GitHubProxyList{
				URL:      src.BaseURL + "/" + file,
				Protocol: proto,
			})
		}
	}

	return fetchers
}
