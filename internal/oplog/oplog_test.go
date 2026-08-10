package oplog

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestDisabledRecorderDoesNotCreateFile(t *testing.T) {
	home := t.TempDir()
	path := Path(home)
	recorder := NewRecorder(Config{
		Enabled:      false,
		Path:         path,
		MaxFileBytes: 1024,
		MaxFiles:     2,
	})

	recorder.Record(context.Background(), Event{Type: "command", Command: "ti help"})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no log file, got err=%v", err)
	}
}

func TestRecorderWritesJSONL(t *testing.T) {
	home := t.TempDir()
	path := Path(home)
	recorder := NewRecorder(Config{
		Enabled:      true,
		Path:         path,
		MaxFileBytes: 1024,
		MaxFiles:     2,
	})

	recorder.Record(context.Background(), Event{
		Timestamp:  time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC),
		Type:       "command",
		Command:    "ti db create-db-cluster",
		FlagNames:  []string{"db-cluster-name", "dry-run"},
		ExitCode:   0,
		DurationMS: 12,
	})

	events := readEvents(t, path)
	if len(events) != 1 {
		t.Fatalf("expected one event, got %d", len(events))
	}
	if events[0].Command != "ti db create-db-cluster" || events[0].FlagNames[0] != "db-cluster-name" {
		t.Fatalf("unexpected event: %#v", events[0])
	}
	if data, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	} else if string(data) == "" || data[len(data)-1] != '\n' {
		t.Fatalf("expected newline-delimited JSON object, got %q", string(data))
	}
}

func TestRecorderRotatesByTotalFileCount(t *testing.T) {
	home := t.TempDir()
	path := Path(home)
	recorder := NewRecorder(Config{
		Enabled:      true,
		Path:         path,
		MaxFileBytes: 1,
		MaxFiles:     2,
	})

	for i := 0; i < 4; i++ {
		recorder.Record(context.Background(), Event{Type: "command", Command: "ti help"})
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("current log missing: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated log missing: %v", err)
	}
	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Fatalf("expected only current plus one rotated file, got err=%v", err)
	}
}

func readEvents(t *testing.T, path string) []Event {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	var events []Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}
