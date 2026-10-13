// Package mcplog is the shared stderr logger. stdout carries the JSON-RPC
// stdio transport, so every log line must go to stderr.
package mcplog

import (
	"fmt"
	"log"
	"os"
	"strings"
)

var debugEnabled = func() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DEBUG")))
	return v == "true" || v == "1" || v == "yes"
}()

var logger = log.New(os.Stderr, "", 0)

func emit(level, format string, args ...any) {
	logger.Printf("[MCP-DrawIO] [%s] %s", level, fmt.Sprintf(format, args...))
}

// Debug logs only when DEBUG=true/1/yes (mirrors logger.ts).
func Debug(format string, args ...any) {
	if debugEnabled {
		emit("DEBUG", format, args...)
	}
}

func Info(format string, args ...any)  { emit("INFO", format, args...) }
func Warn(format string, args ...any)  { emit("WARN", format, args...) }
func Error(format string, args ...any) { emit("ERROR", format, args...) }
