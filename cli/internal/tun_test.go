package internal

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a goroutine-safe writer: runTun emits on its own goroutine while
// the test reads from another.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// readyLine returns the first captured line containing the NETX_READY marker, or
// "" if none has been emitted yet.
func (b *syncBuffer) readyLine() string {
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.Contains(line, "NETX_READY") {
			return line
		}
	}
	return ""
}

// TestRunTunEmitsReadyMarker asserts that, on a healthy start, runTun emits the
// secret-free "NETX_READY" listener-bound marker through the out writer the
// moment the listener is bound — the signal the embedding client latches on to
// start forwarding without waiting out a fixed startup timeout. It exercises the
// full slog -> cfg.out chain (root.go points slog's default handler at cfg.out),
// which is exactly what the c-shared library wraps into the C callback.
//
// Not parallel: Run installs a process-global slog default, restored on cleanup.
func TestRunTunEmitsReadyMarker(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	out := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan int, 1)
	go func() {
		// --from binds an ephemeral loopback UDP port; --to is dialed lazily on the
		// first packet, so no reachable upstream is needed for the bind/marker.
		done <- Run(ctx, cancel,
			WithArgs([]string{"tun",
				"--from", "udp://127.0.0.1:0",
				"--to", "udp://127.0.0.1:9999",
			}),
			WithOut(out),
			WithErr(out),
		)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for out.readyLine() == "" {
		if time.Now().After(deadline) {
			t.Fatalf("NETX_READY not emitted within timeout; captured:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	if line := out.readyLine(); !strings.Contains(line, "listen=") {
		t.Errorf("NETX_READY line missing listen= address: %q", line)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("Run returned non-zero exit code %d after clean cancel", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s after cancel")
	}
}
