package oplog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	logFileMode = 0o600
	logDirMode  = 0o700
)

type Config struct {
	Enabled      bool
	Path         string
	MaxFileBytes int64
	MaxFiles     int
}

type Event struct {
	Timestamp     time.Time `json:"ts"`
	Type          string    `json:"type"`
	Version       string    `json:"version,omitempty"`
	Commit        string    `json:"commit,omitempty"`
	Profile       string    `json:"profile,omitempty"`
	RegionCode    string    `json:"region_code,omitempty"`
	Command       string    `json:"command,omitempty"`
	FlagNames     []string  `json:"flag_names,omitempty"`
	DurationMS    int64     `json:"duration_ms,omitempty"`
	ExitCode      int       `json:"exit_code,omitempty"`
	ErrorCode     string    `json:"error_code,omitempty"`
	ErrorCategory string    `json:"error_category,omitempty"`
	Service       string    `json:"service,omitempty"`
	Operation     string    `json:"operation,omitempty"`
	Method        string    `json:"method,omitempty"`
	StatusCode    int       `json:"status_code,omitempty"`
	RequestID     string    `json:"request_id,omitempty"`
}

type Recorder interface {
	Record(context.Context, Event)
}

type noopRecorder struct{}

func (noopRecorder) Record(context.Context, Event) {}

type contextKey struct{}

func Path(homeDir string) string {
	return filepath.Join(homeDir, ".tdc", "logs", "tdc.jsonl")
}

func WithRecorder(ctx context.Context, recorder Recorder) context.Context {
	if recorder == nil {
		recorder = noopRecorder{}
	}
	return context.WithValue(ctx, contextKey{}, recorder)
}

func FromContext(ctx context.Context) Recorder {
	if ctx == nil {
		return noopRecorder{}
	}
	recorder, ok := ctx.Value(contextKey{}).(Recorder)
	if !ok || recorder == nil {
		return noopRecorder{}
	}
	return recorder
}

func NewRecorder(cfg Config) Recorder {
	if !cfg.Enabled || strings.TrimSpace(cfg.Path) == "" {
		return noopRecorder{}
	}
	return &jsonlRecorder{cfg: cfg}
}

type jsonlRecorder struct {
	cfg Config
	mu  sync.Mutex
}

func (r *jsonlRecorder) Record(ctx context.Context, event Event) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if event.Type == "" {
		event.Type = "event"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.rotateIfNeeded(); err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.cfg.Path), logDirMode); err != nil {
		return
	}
	file, err := os.OpenFile(r.cfg.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, logFileMode)
	if err != nil {
		return
	}
	defer file.Close()
	_ = file.Chmod(logFileMode)
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(event)
}

func (r *jsonlRecorder) rotateIfNeeded() error {
	info, err := os.Stat(r.cfg.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() < r.cfg.MaxFileBytes {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.cfg.Path), logDirMode); err != nil {
		return err
	}
	maxRotated := r.cfg.MaxFiles - 1
	if maxRotated <= 0 {
		return os.Remove(r.cfg.Path)
	}
	_ = os.Remove(rotatedPath(r.cfg.Path, maxRotated))
	for i := maxRotated - 1; i >= 1; i-- {
		src := rotatedPath(r.cfg.Path, i)
		dst := rotatedPath(r.cfg.Path, i+1)
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}
	_ = os.Rename(r.cfg.Path, rotatedPath(r.cfg.Path, 1))
	return nil
}

func rotatedPath(path string, index int) string {
	return fmt.Sprintf("%s.%d", path, index)
}

func SortedFlagNames(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
