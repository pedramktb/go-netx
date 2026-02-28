package netx_test

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"

	netx "github.com/pedramktb/go-netx"
)

// maxWriteConn wraps a net.Conn and exposes a MaxWrite limit, tracking each
// individual Write call so tests can verify chunking behaviour.
type maxWriteConn struct {
	net.Conn
	mu     sync.Mutex
	limit  uint16
	writes [][]byte // each Write payload recorded
}

func (c *maxWriteConn) MaxWrite() uint16 { return c.limit }

func (c *maxWriteConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	buf := make([]byte, len(b))
	copy(buf, b)
	c.writes = append(c.writes, buf)
	c.mu.Unlock()
	return c.Conn.Write(b)
}

func (c *maxWriteConn) Writes() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.writes))
	copy(out, c.writes)
	return out
}

func TestSplitConn_SplitsLargeWrite(t *testing.T) {
	clientRaw, serverRaw := net.Pipe()
	t.Cleanup(func() { _ = clientRaw.Close(); _ = serverRaw.Close() })

	const limit = 8
	tracked := &maxWriteConn{Conn: clientRaw, limit: limit}
	client, err := netx.NewSplitConn(tracked)
	if err != nil {
		t.Fatalf("NewSplitConn: %v", err)
	}

	data := bytes.Repeat([]byte("x"), 25) // 25 bytes → ceil(25/8) = 4 writes

	done := make(chan error, 1)
	received := make([]byte, len(data))
	go func() {
		_, err := io.ReadFull(serverRaw, received)
		done <- err
	}()

	n, err := client.Write(data)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(data) {
		t.Fatalf("wrote %d bytes, want %d", n, len(data))
	}

	if err := <-done; err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if !bytes.Equal(received, data) {
		t.Fatalf("data mismatch: got %q want %q", received, data)
	}

	// Verify each individual Write was no larger than limit.
	for i, w := range tracked.Writes() {
		if len(w) > limit {
			t.Errorf("Write[%d] has %d bytes, exceeds limit %d", i, len(w), limit)
		}
	}
	// 25 bytes / 8 = 3 full chunks + 1 remainder → 4 writes
	if got := len(tracked.Writes()); got != 4 {
		t.Errorf("expected 4 write calls, got %d", got)
	}
}

func TestSplitConn_SmallWritePassthrough(t *testing.T) {
	clientRaw, serverRaw := net.Pipe()
	t.Cleanup(func() { _ = clientRaw.Close(); _ = serverRaw.Close() })

	const limit = 64
	tracked := &maxWriteConn{Conn: clientRaw, limit: limit}
	client, err := netx.NewSplitConn(tracked)
	if err != nil {
		t.Fatalf("NewSplitConn: %v", err)
	}

	data := []byte("small") // 5 bytes < limit

	done := make(chan error, 1)
	received := make([]byte, len(data))
	go func() {
		_, err := io.ReadFull(serverRaw, received)
		done <- err
	}()

	if _, err := client.Write(data); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("ReadFull: %v", err)
	}

	if got := len(tracked.Writes()); got != 1 {
		t.Errorf("expected 1 write call, got %d", got)
	}
}

func TestSplitConn_NoMaxWritePassthrough(t *testing.T) {
	// A plain net.Conn with no MaxWrite — NewSplitConn should return it unchanged.
	clientRaw, serverRaw := net.Pipe()
	t.Cleanup(func() { _ = clientRaw.Close(); _ = serverRaw.Close() })

	wrapped, err := netx.NewSplitConn(clientRaw)
	if err == nil {
		t.Fatalf("expected error for connection with no MaxWrite, got nil")
	}
	if wrapped != nil {
		t.Fatalf("expected nil connection on error, got non-nil")
	}
}
