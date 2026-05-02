package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestCompactHandlerFormatsSingleLine(t *testing.T) {
	buffer := &bytes.Buffer{}
	handler := newCompactHandler(buffer, slog.LevelInfo)
	record := slog.NewRecord(time.Date(2026, 5, 2, 12, 30, 45, 0, time.UTC), slog.LevelInfo, "登录成功", 0)
	record.AddAttrs(
		slog.String("source", "login"),
		slog.Uint64("session", 11),
		slog.String("server", "127.0.0.1:7000"),
	)

	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("handle log record: %v", err)
	}

	got := buffer.String()
	if !strings.Contains(got, "[2026-05-02 12:30:45][INFO][login]登录成功") {
		t.Fatalf("unexpected log prefix: %q", got)
	}
	if !strings.Contains(got, "session=11") || !strings.Contains(got, "server=127.0.0.1:7000") {
		t.Fatalf("unexpected log attrs: %q", got)
	}
}
