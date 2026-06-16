package internal

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/pedramktb/go-netx/drivers/aesgcm"
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

// findLine returns the first captured line containing needle, or "" if none.
func (b *syncBuffer) findLine(needle string) string {
	for line := range strings.SplitSeq(b.String(), "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

// TestRunTunStartedLine asserts that the info-level "netx tun started" line —
// which the c-shared library forwards to the embedder's log callback as the
// "relay is bound and serving" signal — carries the listen address and the
// protocol chain, and that secret param values are replaced with their
// driver-supplied fingerprints rather than the raw key material.
//
// Exercises the full slog -> cfg.out chain (root.go points slog's default
// handler at cfg.out), which is exactly what the c-shared library wraps.
//
// Not parallel: Run installs a process-global slog default, restored on cleanup.
func TestRunTunStartedLine(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	const keyHex = "00112233445566778899aabbccddeeff"
	out := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan int, 1)
	go func() {
		// --from binds an ephemeral loopback UDP port; --to is dialed lazily on
		// the first packet, so no reachable upstream is needed.
		done <- Run(ctx, cancel,
			WithArgs([]string{"tun",
				"--from", "udp+aesgcm{key=" + keyHex + "}://127.0.0.1:0",
				"--to", "udp+aesgcm{key=" + keyHex + "}://127.0.0.1:9999",
			}),
			WithOut(out),
			WithErr(out),
		)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for out.findLine("netx tun started") == "" {
		if time.Now().After(deadline) {
			t.Fatalf("started line not emitted within timeout; captured:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	line := out.findLine("netx tun started")
	if !strings.Contains(line, "listen=") {
		t.Errorf("started line missing listen= address: %q", line)
	}
	if strings.Contains(line, keyHex) {
		t.Errorf("raw key material leaked into started line: %q", line)
	}
	if !strings.Contains(line, "aesgcm") {
		t.Errorf("protocol chain missing from started line: %q", line)
	}
	if !strings.Contains(line, "key=REDACTED(sha256=") {
		t.Errorf("redacted fingerprint missing from started line: %q", line)
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
