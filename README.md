# unoca (UnOpenCodeAPI)

`unoca` (UnOpenCodeAPI) is a high-performance Go proxy and router designed to forward requests to the OpenCode API without requiring a personal API Key. By leveraging free public proxies and continuously verifying their validity and rate-limit status, `unoca` provides a seamless, OpenAI-compatible API interface.

---

## 🌟 Key Features

- **OpenAI Compatible Endpoint**: Exposes OpenAI-compatible routes (`/v1/models` and `/v1/chat/completions`) on a single port (**`20120`**).
- **Public Proxy Fetching & Parsing**: Scrapes proxies from 13+ public sources (ProxyScrape, free-proxy-list, GitHub repositories, etc.) and parses multiple format specs (`socks5://IP:PORT`, `IP:PORT:Country`, etc.).
- **Smart Filtering & Protocol Priority**: Filters private/invalid IPs (`0.0.0.0`, `127.x.x.x`, `10.x.x.x`, `192.168.x.x`) and prioritizes protocols: **SOCKS5 > SOCKS4 > HTTPS > HTTP**.
- **Two-Step Proxy Validation**:
  1. **Connection & GeoIP Check**: Verifies connectivity via `https://ipinfo.io/json` (5s timeout) to acquire ISO country flags and separate dead proxies from timeouts.
  2. **AI Prompt Validation**: Sends sample test prompts (`1+1=?`, etc.) to OpenCode API to check actual availability (`alive` vs `rate_limit`).
- **Embedded Admin Dashboard**: Access a full UI at `http://localhost:20120/dashboard/endpoint` featuring 4 tabs:
  - **Endpoint**: Quick setup guides, base URL, and active proxy stats.
  - **Usage**: Detailed token counting, estimated cost display, a **Smoothed Area Chart** (00:00–23:59 daily usage), and a **Contribution Heatmap Graph** (365-day tracking).
  - **System Logs**: Internal runtime and API error logs.
  - **Proxy Pools**: Filterable, sortable, and paginated proxy table with country flags and latency metrics.
- **Embedded SQLite Storage (`unoca.db`)**: Stores proxy pool state and usage data via CGO-free pure Go SQLite (`modernc.org/sqlite`). Automatic prune routines clean up old logs before application start.
- **Auto & Manual Proxy Scans**: Automatic scan cycle every 24 hours with an instant "Refresh Proxies" trigger in the admin UI.

---

## 🚀 Quick Start

### Prerequisites

- **Go**: `1.22` or `1.25` installed.

### Installation & Execution

1. Clone the repository and navigate to the source directory:
   ```bash
   cd d:\project\UnOpenCodeAPI\uncodeapi
   ```

2. Tidy dependencies:
   ```bash
   go mod tidy
   ```

3. Run directly or build the binary:
   ```bash
   # Run directly
   go run cmd/unoca/main.go

   # Or build binary
   go build -o unoca.exe cmd/unoca/main.go
   ./unoca.exe
   ```

Upon startup, `unoca` creates `unoca.db` in the working directory and serves the proxy and Web UI at `http://localhost:20120`.

---

## 📖 Usage Guide

### OpenAI Client Configuration

Configure any OpenAI SDK or client (e.g., Python `openai`, LangChain, NextChat) to point to `unoca`:

- **Base URL**: `http://localhost:20120/v1`
- **API Key**: Any dummy string (e.g., `sk-unoca-free`)

#### Example (cURL)

```bash
curl http://localhost:20120/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-unoca-free" \
  -d '{
    "model": "opencode-free",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

---

## 🛠 Web Admin Dashboard

Open `http://localhost:20120` in your web browser to open the Admin UI:

- **Endpoint**: Displays the active Base URL `http://localhost:20120/v1` and connection test buttons.
- **Usage**: Shows Input/Output token counts, estimated costs, Smoothed Area Chart (daily 24h timeline), and Contribution Heatmap Graph (yearly activity).
- **System Logs**: Displays internal logs, including HTTP 429 rate limit events.
- **Proxy Pools**: Provides pagination, search, protocol filters, and manual proxy refreshing.

---

## 📁 Database & Cleanup (`unoca.db`)

`unoca` uses a local SQLite database named `unoca.db` placed alongside `unoca.exe`:

- **WAL Mode**: Optimized for low IO overhead.
- **Automatic Prune**: Before printing the startup banner, `unoca` automatically purges expired log lines and outdated daily usage records.

---

## � Comparison: `unoca` vs 9router & OmniRoute

While all three tools aim to provide API access to OpenCode Zen, `unoca` operates on a completely different core design:

- **Focus & Simplicity**:
  - **9router / OmniRoute**: Centralized router/proxy platforms aggregating multiple cloud services, often requiring account sign-ups, credit cards, or unofficial/internal API keys.
  - **`unoca`**: Exclusively focused on **public OpenCode Zen endpoints** using IP-based rate limiting without needing any account or API keys.
- **Proactive & Automated Proxy Management**:
  - **`unoca`**: Automatically fetches and ingests **100,000+ proxies** from 13+ public sources, proactively testing and validating each proxy directly against OpenCode Zen rate limits (`FreeUsageLimitError`/`429`).
  - **9router / OmniRoute**: Passive design requiring manual proxy entry and management by the user.

---

## 📄 License

This project is licensed under the **Apache License 2.0**. See the [LICENSE](file:///d:/project/UnOpenCodeAPI/LICENSE) file for details.
