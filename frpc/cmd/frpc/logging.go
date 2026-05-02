package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

type compactHandler struct {
	writer io.Writer
	level  slog.Leveler
	attrs  []slog.Attr
	groups []string
	mu     *sync.Mutex
}

func newCompactHandler(writer io.Writer, level slog.Leveler) slog.Handler {
	return &compactHandler{
		writer: writer,
		level:  level,
		mu:     &sync.Mutex{},
	}
}

func (h *compactHandler) Enabled(_ context.Context, level slog.Level) bool {
	minLevel := slog.LevelInfo
	if h != nil && h.level != nil {
		minLevel = h.level.Level()
	}
	return level >= minLevel
}

func (h *compactHandler) Handle(_ context.Context, record slog.Record) error {
	if h == nil {
		return nil
	}

	source := "client"
	extras := make([]string, 0, record.NumAttrs()+len(h.attrs))
	collect := func(groups []string, attr slog.Attr) {
		source = appendLogAttr(&extras, groups, attr, source)
	}

	for _, attr := range h.attrs {
		collect(h.groups, attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		collect(h.groups, attr)
		return true
	})

	message := record.Message
	if len(extras) > 0 {
		message += " " + strings.Join(extras, " ")
	}

	timestamp := record.Time
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	line := fmt.Sprintf("[%s][%s][%s]%s\n", timestamp.Format("2006-01-02 15:04:05"), levelLabel(record.Level), source, message)

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.writer, line)
	return err
}

func (h *compactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &next
}

func (h *compactHandler) WithGroup(name string) slog.Handler {
	next := *h
	next.groups = append(append([]string(nil), h.groups...), name)
	return &next
}

func appendLogAttr(extras *[]string, groups []string, attr slog.Attr, source string) string {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return source
	}
	if attr.Value.Kind() == slog.KindGroup {
		nextGroups := groups
		if attr.Key != "" {
			nextGroups = append(append([]string(nil), groups...), attr.Key)
		}
		for _, item := range attr.Value.Group() {
			source = appendLogAttr(extras, nextGroups, item, source)
		}
		return source
	}

	key := attr.Key
	if len(groups) > 0 {
		key = strings.Join(append(append([]string(nil), groups...), attr.Key), ".")
	}
	if key == "source" {
		return valueToString(attr.Value)
	}
	if key == "" {
		*extras = append(*extras, valueToString(attr.Value))
		return source
	}

	*extras = append(*extras, key+"="+valueToString(attr.Value))
	return source
}

func levelLabel(level slog.Level) string {
	switch {
	case level <= slog.LevelDebug:
		return "DEBUG"
	case level < slog.LevelWarn:
		return "INFO"
	case level < slog.LevelError:
		return "WARN"
	default:
		return "ERROR"
	}
}

func valueToString(value slog.Value) string {
	switch value.Kind() {
	case slog.KindString:
		return value.String()
	case slog.KindInt64:
		return strconv.FormatInt(value.Int64(), 10)
	case slog.KindUint64:
		return strconv.FormatUint(value.Uint64(), 10)
	case slog.KindFloat64:
		return strconv.FormatFloat(value.Float64(), 'f', -1, 64)
	case slog.KindBool:
		return strconv.FormatBool(value.Bool())
	case slog.KindDuration:
		return value.Duration().String()
	case slog.KindTime:
		return value.Time().Format(time.RFC3339)
	case slog.KindAny:
		return fmt.Sprint(value.Any())
	default:
		return value.String()
	}
}
