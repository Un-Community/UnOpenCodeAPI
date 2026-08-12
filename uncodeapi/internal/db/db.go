package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
	"uncodeapi/internal/logger"
)

type DB struct {
	conn *sql.DB
}

func GetDBPath() string {
	execPath, err := os.Executable()
	if err != nil {
		return "unoca.db"
	}
	return filepath.Join(filepath.Dir(execPath), "unoca.db")
}

func InitDB() (*DB, error) {
	dbPath := GetDBPath()
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// Optimize SQLite performance & disk lifespan
	_, _ = conn.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=NORMAL;
		PRAGMA temp_store=MEMORY;
	`)

	d := &DB{conn: conn}
	if err := d.migrate(); err != nil {
		return nil, err
	}

	// Prune old data BEFORE app startup banner
	d.PruneOldData()

	return d, nil
}

func (d *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS proxy_cache (
		address TEXT PRIMARY KEY,
		protocol TEXT,
		country TEXT,
		flag TEXT,
		latency_ms INTEGER,
		updated_at DATETIME
	);

	CREATE TABLE IF NOT EXISTS hourly_usage (
		date TEXT,
		hour INTEGER,
		requests INTEGER DEFAULT 0,
		input_tokens INTEGER DEFAULT 0,
		output_tokens INTEGER DEFAULT 0,
		PRIMARY KEY (date, hour)
	);

	CREATE TABLE IF NOT EXISTS daily_heatmap (
		date_str TEXT PRIMARY KEY,
		requests INTEGER DEFAULT 0
	);
	`
	_, err := d.conn.Exec(schema)
	return err
}

// PruneOldData deletes expired data on startup:
// - Hourly API calls & Proxy cache -> deleted when date changes (older than today)
// - Heatmap graph -> deleted when year changes (older than current year)
func (d *DB) PruneOldData() {
	now := time.Now()
	todayStr := now.Format("2006-01-02")
	thisYearStr := now.Format("2006")

	// 1. Delete hourly usage & proxy cache older than today
	res1, _ := d.conn.Exec("DELETE FROM hourly_usage WHERE date < ?", todayStr)
	res2, _ := d.conn.Exec("DELETE FROM proxy_cache WHERE DATE(updated_at) < ?", todayStr)

	// 2. Delete Heatmap graph entries older than current year
	res3, _ := d.conn.Exec("DELETE FROM daily_heatmap WHERE SUBSTR(date_str, 1, 4) < ?", thisYearStr)

	n1, _ := res1.RowsAffected()
	n2, _ := res2.RowsAffected()
	n3, _ := res3.RowsAffected()

	if n1 > 0 || n2 > 0 || n3 > 0 {
		logger.Info("DB Prune: cleared %d hourly logs, %d proxy cache, %d heatmap logs", n1, n2, n3)
	}
}

func (d *DB) SaveProxyCache(addr, protocol, country, flag string, latency int64) {
	query := `
	INSERT INTO proxy_cache (address, protocol, country, flag, latency_ms, updated_at)
	VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT(address) DO UPDATE SET
		protocol=excluded.protocol,
		country=excluded.country,
		flag=excluded.flag,
		latency_ms=excluded.latency_ms,
		updated_at=excluded.updated_at;
	`
	_, _ = d.conn.Exec(query, addr, protocol, country, flag, latency, time.Now())
}

func (d *DB) RecordRequest(inTokens, outTokens uint64) {
	now := time.Now()
	todayStr := now.Format("2006-01-02")
	hour := now.Hour()

	// 1. Update hourly usage
	queryHourly := `
	INSERT INTO hourly_usage (date, hour, requests, input_tokens, output_tokens)
	VALUES (?, ?, 1, ?, ?)
	ON CONFLICT(date, hour) DO UPDATE SET
		requests = requests + 1,
		input_tokens = input_tokens + excluded.input_tokens,
		output_tokens = output_tokens + excluded.output_tokens;
	`
	_, _ = d.conn.Exec(queryHourly, todayStr, hour, inTokens, outTokens)

	// 2. Update daily heatmap (independent of detailed request logs)
	queryHeatmap := `
	INSERT INTO daily_heatmap (date_str, requests)
	VALUES (?, 1)
	ON CONFLICT(date_str) DO UPDATE SET
		requests = requests + 1;
	`
	_, _ = d.conn.Exec(queryHeatmap, todayStr)
}

func (d *DB) LoadHourlyUsage(dateStr string) ([24]uint64, uint64, uint64, uint64) {
	var hourly [24]uint64
	var totalReqs, totalIn, totalOut uint64

	rows, err := d.conn.Query("SELECT hour, requests, input_tokens, output_tokens FROM hourly_usage WHERE date = ?", dateStr)
	if err != nil {
		return hourly, totalReqs, totalIn, totalOut
	}
	defer rows.Close()

	for rows.Next() {
		var h int
		var req, inTok, outTok uint64
		if err := rows.Scan(&h, &req, &inTok, &outTok); err == nil && h >= 0 && h < 24 {
			hourly[h] = req
			totalReqs += req
			totalIn += inTok
			totalOut += outTok
		}
	}
	return hourly, totalReqs, totalIn, totalOut
}

func (d *DB) LoadHeatmap() map[string]uint64 {
	out := make(map[string]uint64)
	rows, err := d.conn.Query("SELECT date_str, requests FROM daily_heatmap")
	if err != nil {
		return out
	}
	defer rows.Close()

	for rows.Next() {
		var dateStr string
		var req uint64
		if err := rows.Scan(&dateStr, &req); err == nil {
			out[dateStr] = req
		}
	}
	return out
}

func (d *DB) Close() {
	if d.conn != nil {
		_ = d.conn.Close()
	}
}
