package interrupt

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestRepeatedSignalsSurviveContextCancellation(t *testing.T) {
	incoming := make(chan os.Signal, 2)
	ctx, stop := notifyContext(context.Background(), incoming)
	defer stop()
	for i := 0; i < 2; i++ {
		incoming <- os.Interrupt
		select {
		case sig := <-Signals(ctx):
			if sig != os.Interrupt || !FromSignal(ctx) || ctx.Err() != context.Canceled {
				t.Fatalf("signal=%v cause=%v error=%v", sig, context.Cause(ctx), ctx.Err())
			}
		case <-time.After(time.Second):
			t.Fatal("signal was lost")
		}
	}
	stop() // Cleanup is idempotent.
}

func TestParentCancellationIsNotAProcessSignal(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, stop := notifyContext(parent, make(chan os.Signal))
	defer stop()
	cancel()
	if FromSignal(ctx) || ctx.Err() != context.Canceled {
		t.Fatalf("cause=%v error=%v", context.Cause(ctx), ctx.Err())
	}
}
