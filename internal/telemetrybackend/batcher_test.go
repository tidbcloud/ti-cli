package telemetrybackend

import (
	"context"
	"testing"
	"time"
)

func TestBatcherFlushesAtEventThreshold(t *testing.T) {
	cfg := testConfig()
	cfg.FlushMaxEvents = 2
	sink := newRecordingSink("tidb", nil)
	metrics := &Metrics{}
	batcher := NewBatcher(cfg, []Sink{sink}, discardLogger(), metrics)
	batcher.Start()
	defer closeBatcher(t, batcher)

	if !batcher.Enqueue([]Event{testEvent(), testEvent()}) {
		t.Fatal("Enqueue returned false")
	}
	waitForSink(t, sink)
	if sink.eventCount() != 2 {
		t.Fatalf("sink event count = %d, want 2", sink.eventCount())
	}
	if metrics.TiDBSuccesses.Load() != 1 || metrics.TiDBFailures.Load() != 0 {
		t.Fatalf("TiDB metrics = failures %d, successes %d", metrics.TiDBFailures.Load(), metrics.TiDBSuccesses.Load())
	}
}

func TestBatcherRecordsTiDBFailure(t *testing.T) {
	cfg := testConfig()
	cfg.FlushMaxEvents = 1
	tidb := newRecordingSink("tidb", errTestSink)
	metrics := &Metrics{}
	batcher := NewBatcher(cfg, []Sink{tidb}, discardLogger(), metrics)
	batcher.Start()
	defer closeBatcher(t, batcher)

	if !batcher.Enqueue([]Event{testEvent()}) {
		t.Fatal("Enqueue returned false")
	}
	waitForSink(t, tidb)
	if tidb.eventCount() != 1 {
		t.Fatalf("TiDB event count = %d, want 1", tidb.eventCount())
	}
	if metrics.TiDBFailures.Load() != 1 || metrics.TiDBSuccesses.Load() != 0 {
		t.Fatalf("TiDB metrics = failures %d, successes %d", metrics.TiDBFailures.Load(), metrics.TiDBSuccesses.Load())
	}
}

func TestBatcherFlushesAtByteThreshold(t *testing.T) {
	cfg := testConfig()
	cfg.FlushMaxBytes = 1
	sink := newRecordingSink("sink", nil)
	batcher := NewBatcher(cfg, []Sink{sink}, discardLogger(), nil)
	batcher.Start()
	defer closeBatcher(t, batcher)

	if !batcher.Enqueue([]Event{testEvent()}) {
		t.Fatal("Enqueue returned false")
	}
	waitForSink(t, sink)
}

func TestBatcherFlushesAtInterval(t *testing.T) {
	cfg := testConfig()
	cfg.FlushInterval = 20 * time.Millisecond
	sink := newRecordingSink("sink", nil)
	batcher := NewBatcher(cfg, []Sink{sink}, discardLogger(), nil)
	batcher.Start()
	defer closeBatcher(t, batcher)

	if !batcher.Enqueue([]Event{testEvent()}) {
		t.Fatal("Enqueue returned false")
	}
	waitForSink(t, sink)
}

func TestBatcherShutdownDrainsPendingEvents(t *testing.T) {
	cfg := testConfig()
	sink := newRecordingSink("sink", nil)
	batcher := NewBatcher(cfg, []Sink{sink}, discardLogger(), nil)
	batcher.Start()
	if !batcher.Enqueue([]Event{testEvent()}) {
		t.Fatal("Enqueue returned false")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := batcher.Close(ctx); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if sink.eventCount() != 1 {
		t.Fatalf("sink event count = %d, want 1", sink.eventCount())
	}
}

func TestBatcherShutdownDrainIsBounded(t *testing.T) {
	cfg := testConfig()
	cfg.ShutdownDrainTimeout = 20 * time.Millisecond
	cfg.SinkTimeout = time.Second
	sink := newRecordingSink("sink", nil)
	sink.delay = time.Second
	batcher := NewBatcher(cfg, []Sink{sink}, discardLogger(), nil)
	batcher.Start()
	if !batcher.Enqueue([]Event{testEvent()}) {
		t.Fatal("Enqueue returned false")
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := batcher.Close(ctx); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("Close took %v, expected bounded shutdown", elapsed)
	}
}

func TestBatcherRejectsRequestAtomicallyWhenFull(t *testing.T) {
	cfg := testConfig()
	cfg.BufferMaxEvents = 1
	cfg.FlushMaxEvents = 1
	batcher := NewBatcher(cfg, nil, discardLogger(), nil)
	if batcher.Enqueue([]Event{testEvent(), testEvent()}) {
		t.Fatal("oversized enqueue was accepted")
	}
	if batcher.Pending() != 0 {
		t.Fatalf("Pending = %d, want 0", batcher.Pending())
	}
}

func closeBatcher(t *testing.T, batcher *Batcher) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := batcher.Close(ctx); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

func waitForSink(t *testing.T, sink *recordingSink) {
	t.Helper()
	select {
	case <-sink.called:
	case <-time.After(time.Second):
		t.Fatalf("sink %s was not called", sink.name)
	}
}
