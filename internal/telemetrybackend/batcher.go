package telemetrybackend

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Sink receives one already-sanitized telemetry batch.
type Sink interface {
	Name() string
	Write(context.Context, []Event) error
}

// Metrics contains process-local counters exposed through the private metrics
// endpoint. It never stores event values or identifiers.
type Metrics struct {
	AcceptedEvents   atomic.Uint64
	RejectedRequests atomic.Uint64
	RateLimited      atomic.Uint64
	DroppedEvents    atomic.Uint64
	FlushedEvents    atomic.Uint64
	TiDBSuccesses    atomic.Uint64
	TiDBFailures     atomic.Uint64
}

type queuedEvent struct {
	event Event
	bytes int
}

// Batcher owns one bounded queue and exactly one flush loop.
type Batcher struct {
	config  Config
	sinks   []Sink
	logger  *slog.Logger
	metrics *Metrics

	mu           sync.Mutex
	queue        []queuedEvent
	pendingBytes int
	closed       bool
	started      bool
	wake         chan struct{}
	stop         chan struct{}
	done         chan struct{}
}

func NewBatcher(config Config, sinks []Sink, logger *slog.Logger, metrics *Metrics) *Batcher {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if metrics == nil {
		metrics = &Metrics{}
	}
	return &Batcher{
		config:  config,
		sinks:   append([]Sink(nil), sinks...),
		logger:  logger,
		metrics: metrics,
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (b *Batcher) Start() {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return
	}
	b.started = true
	b.mu.Unlock()
	go b.loop()
}

// Enqueue returns false immediately when the bounded queue cannot accept every
// event. Requests are accepted atomically; partial request batches are never
// queued.
func (b *Batcher) Enqueue(events []Event) bool {
	if len(events) == 0 {
		return false
	}
	queued := make([]queuedEvent, len(events))
	totalBytes := 0
	for index, event := range events {
		size := estimateEventBytes(event)
		queued[index] = queuedEvent{event: cloneEvent(event), bytes: size}
		totalBytes += size
	}

	b.mu.Lock()
	if b.closed || len(b.queue)+len(queued) > b.config.BufferMaxEvents {
		b.mu.Unlock()
		b.metrics.DroppedEvents.Add(uint64(len(events)))
		return false
	}
	b.queue = append(b.queue, queued...)
	b.pendingBytes += totalBytes
	shouldWake := len(b.queue) >= b.config.FlushMaxEvents ||
		b.pendingBytes >= b.config.FlushMaxBytes
	b.mu.Unlock()

	b.metrics.AcceptedEvents.Add(uint64(len(events)))
	if shouldWake {
		b.signal()
	}
	return true
}

func (b *Batcher) Pending() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.queue)
}

// Close stops accepting events and drains the queue within the configured
// shutdown timeout.
func (b *Batcher) Close(ctx context.Context) error {
	b.mu.Lock()
	if !b.started {
		b.closed = true
		b.started = true
		close(b.done)
		b.mu.Unlock()
		return nil
	}
	if !b.closed {
		b.closed = true
		close(b.stop)
	}
	b.mu.Unlock()

	select {
	case <-b.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *Batcher) loop() {
	ticker := time.NewTicker(b.config.FlushInterval)
	defer ticker.Stop()
	defer close(b.done)

	for {
		select {
		case <-b.wake:
			for b.thresholdReached() {
				b.flushNext(context.Background())
			}
		case <-ticker.C:
			b.flushNext(context.Background())
		case <-b.stop:
			ctx, cancel := context.WithTimeout(context.Background(), b.config.ShutdownDrainTimeout)
			b.flushAll(ctx)
			cancel()
			return
		}
	}
}

func (b *Batcher) flushAll(ctx context.Context) {
	for b.Pending() > 0 && ctx.Err() == nil {
		b.flushNext(ctx)
	}
	if remaining := b.Pending(); remaining > 0 {
		b.metrics.DroppedEvents.Add(uint64(remaining))
		b.logger.Error("telemetry shutdown drain timed out", "dropped_events", remaining)
		b.discardPending()
	}
}

func (b *Batcher) flushNext(parent context.Context) {
	batch := b.takeBatch()
	if len(batch) == 0 {
		return
	}
	started := time.Now()
	for _, sink := range b.sinks {
		ctx, cancel := context.WithTimeout(parent, b.config.SinkTimeout)
		err := sink.Write(ctx, batch)
		cancel()
		if err != nil {
			b.recordSinkResult(sink.Name(), false)
			b.logger.Error(
				"telemetry sink write failed",
				"sink", sink.Name(),
				"event_count", len(batch),
				"duration_ms", time.Since(started).Milliseconds(),
			)
			continue
		}
		b.recordSinkResult(sink.Name(), true)
		b.logger.Info(
			"telemetry sink write completed",
			"sink", sink.Name(),
			"event_count", len(batch),
			"duration_ms", time.Since(started).Milliseconds(),
		)
	}
	b.metrics.FlushedEvents.Add(uint64(len(batch)))
}

func (b *Batcher) recordSinkResult(name string, success bool) {
	switch name {
	case "tidb":
		if success {
			b.metrics.TiDBSuccesses.Add(1)
		} else {
			b.metrics.TiDBFailures.Add(1)
		}
	}
}

func (b *Batcher) takeBatch() []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) == 0 {
		return nil
	}

	count := 0
	bytes := 0
	for count < len(b.queue) && count < b.config.FlushMaxEvents {
		nextBytes := b.queue[count].bytes
		if count > 0 && bytes+nextBytes > b.config.FlushMaxBytes {
			break
		}
		bytes += nextBytes
		count++
	}
	batch := make([]Event, count)
	for index := 0; index < count; index++ {
		batch[index] = b.queue[index].event
	}
	copy(b.queue, b.queue[count:])
	b.queue = b.queue[:len(b.queue)-count]
	b.pendingBytes -= bytes
	return batch
}

func (b *Batcher) thresholdReached() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.queue) >= b.config.FlushMaxEvents ||
		b.pendingBytes >= b.config.FlushMaxBytes
}

func (b *Batcher) discardPending() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queue = nil
	b.pendingBytes = 0
}

func (b *Batcher) signal() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func cloneEvent(event Event) Event {
	event.FlagNames = append([]string(nil), event.FlagNames...)
	return event
}
