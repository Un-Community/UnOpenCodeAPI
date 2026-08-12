package logger

import (
	"fmt"
	"log"
	"sync"
	"time"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

type LogEntry struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
}

type Logger struct {
	mu     sync.Mutex
	level  Level
	out    *log.Logger
	buf    []LogEntry
	maxBuf int
	subs   map[chan LogEntry]struct{}
}

var defaultLogger = &Logger{
	level:  LevelInfo,
	maxBuf: 500,
	subs:   make(map[chan LogEntry]struct{}),
}

func Init(level Level) {
	defaultLogger.mu.Lock()
	defaultLogger.level = level
	defaultLogger.mu.Unlock()
}

func Subscribe() (chan LogEntry, func()) {
	ch := make(chan LogEntry, 100)
	defaultLogger.mu.Lock()
	defaultLogger.subs[ch] = struct{}{}
	defaultLogger.mu.Unlock()
	return ch, func() {
		defaultLogger.mu.Lock()
		if _, ok := defaultLogger.subs[ch]; ok {
			delete(defaultLogger.subs, ch)
			close(ch)
		}
		defaultLogger.mu.Unlock()
	}
}

func Recent(n int) []LogEntry {
	defaultLogger.mu.Lock()
	defer defaultLogger.mu.Unlock()
	if n <= 0 || n > len(defaultLogger.buf) {
		n = len(defaultLogger.buf)
	}
	out := make([]LogEntry, n)
	copy(out, defaultLogger.buf[len(defaultLogger.buf)-n:])
	return out
}

func (l *Logger) logf(level Level, levelStr, format string, args ...any) {
	l.mu.Lock()
	if level < l.level {
		l.mu.Unlock()
		return
	}
	msg := fmt.Sprintf(format, args...)
	entry := LogEntry{Time: time.Now(), Level: levelStr, Msg: msg}
	l.buf = append(l.buf, entry)
	if len(l.buf) > l.maxBuf {
		l.buf = l.buf[len(l.buf)-l.maxBuf:]
	}
	subs := make([]chan LogEntry, 0, len(l.subs))
	for ch := range l.subs {
		subs = append(subs, ch)
	}
	l.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- entry:
		default:
		}
	}
}

func Debug(f string, a ...any) { defaultLogger.logf(LevelDebug, "DEBUG", f, a...) }
func Info(f string, a ...any)  { defaultLogger.logf(LevelInfo, "INFO", f, a...) }
func Warn(f string, a ...any)  { defaultLogger.logf(LevelWarn, "WARN", f, a...) }
func Error(f string, a ...any) { defaultLogger.logf(LevelError, "ERROR", f, a...) }
