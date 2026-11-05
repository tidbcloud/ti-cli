// Package interrupt preserves repeated process signals while canceling work on
// the first signal. Runtime execution forwards the signals to its companion.
package interrupt

import (
	"context"
	"os"
	"os/signal"
	"sync"
)

type signalsKey struct{}
type signalCause struct{}

func (signalCause) Error() string { return "process interrupted" }

// NotifyContext cancels on the first signal and keeps receiving subsequent
// signals until stop. Callers must call stop to release the signal handler.
func NotifyContext(parent context.Context, sig ...os.Signal) (context.Context, func()) {
	incoming := make(chan os.Signal, 2)
	signal.Notify(incoming, sig...)
	ctx, stop := notifyContext(parent, incoming)
	return ctx, func() {
		signal.Stop(incoming)
		stop()
	}
}

func notifyContext(parent context.Context, incoming <-chan os.Signal) (context.Context, func()) {
	outgoing := make(chan os.Signal, 2)
	ctx, cancel := context.WithCancelCause(context.WithValue(parent, signalsKey{}, (<-chan os.Signal)(outgoing)))
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done); cancel(context.Canceled) }) }
	go func() {
		for {
			select {
			case s := <-incoming:
				cancel(signalCause{})
				select {
				case outgoing <- s:
				case <-done:
					return
				case <-parent.Done():
					return
				}
			case <-done:
				return
			case <-parent.Done():
				return
			}
		}
	}()
	return ctx, stop
}

// Signals returns the process signals associated with ctx, or nil for a plain
// context. A consumer must not also forward the first signal via ctx.Done.
func Signals(ctx context.Context) <-chan os.Signal {
	signals, _ := ctx.Value(signalsKey{}).(<-chan os.Signal)
	return signals
}

// FromSignal reports whether a process signal caused ctx cancellation.
func FromSignal(ctx context.Context) bool {
	_, ok := context.Cause(ctx).(signalCause)
	return ok
}
