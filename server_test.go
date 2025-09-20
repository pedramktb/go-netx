package netx_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pedramktb/go-netx"
)

func TestRouteReplacement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	logger := &memLogger{}
	var s netx.Server[string]
	s.Logger = logger

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(ctx, ln) }()

	// First handler (will be replaced)
	s.SetRoute("id", func(_ context.Context, conn net.Conn, closed func()) (bool, io.Closer) {
		// Always match
		_, _ = conn.Write([]byte("h1"))
		_ = conn.Close()
		closed()
		return true, conn
	})

	c1, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial1: %v", err)
	}
	_ = c1.SetReadDeadline(time.Now().Add(2 * time.Second))
	b1 := make([]byte, 2)
	if _, err := bufio.NewReader(c1).Read(b1); err != nil {
		t.Fatalf("read1: %v", err)
	}
	if string(b1) != "h1" {
		t.Fatalf("expected 'h1', got %q", string(b1))
	}
	_ = c1.Close()

	// Replace with a new handler under the same ID
	s.SetRoute("id", func(_ context.Context, conn net.Conn, closed func()) (bool, io.Closer) {
		_, _ = conn.Write([]byte("h2"))
		_ = conn.Close()
		closed()
		return true, conn
	})

	c2, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial2: %v", err)
	}
	_ = c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	b2 := make([]byte, 2)
	if _, err := bufio.NewReader(c2).Read(b2); err != nil {
		t.Fatalf("read2: %v", err)
	}
	if string(b2) != "h2" {
		t.Fatalf("expected 'h2', got %q", string(b2))
	}
	_ = c2.Close()

	// Shutdown and ensure Serve returns ErrServerClosed
	if err := s.Close(); err != nil && !errors.Is(err, netx.ErrServerClosed) {
		t.Fatalf("server close: %v", err)
	}
	select {
	case got := <-errCh:
		if !errors.Is(got, netx.ErrServerClosed) {
			t.Fatalf("serve returned %v, want ErrServerClosed", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not exit after Close()")
	}
}

func TestRemoveRouteThenUnhandled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	logger := &memLogger{}
	var s netx.Server[string]
	s.Logger = logger

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(ctx, ln) }()

	s.SetRoute("id", func(_ context.Context, conn net.Conn, closed func()) (bool, io.Closer) {
		// This would match if present
		return true, conn
	})
	s.RemoveRoute("id")

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Give some time for the server to process and drop the connection
	_ = c.SetDeadline(time.Now().Add(500 * time.Millisecond))
	// Expect read to hit EOF or error quickly because server drops immediately
	buf := make([]byte, 1)
	_, rErr := c.Read(buf)
	if rErr == nil {
		t.Fatalf("expected read error/EOF on unhandled connection, got nil")
	}
	_ = c.Close()

	// Ensure log captured unhandled drop
	time.Sleep(100 * time.Millisecond)
	logger.mu.Lock()
	found := false
	for _, e := range logger.entries {
		if e == "DEBUG: unhandled connection, dropping..." || e == "DEBUG: unhandled connection, dropping connection" || e == "DEBUG: no routes configured, dropping connection" {
			found = true
			break
		}
	}
	logger.mu.Unlock()
	if !found {
		t.Fatalf("expected unhandled/no routes log entry, got: %#v", logger.entries)
	}

	// Cleanup
	_ = s.Close()
	select {
	case got := <-errCh:
		if !errors.Is(got, netx.ErrServerClosed) {
			t.Fatalf("serve returned %v, want ErrServerClosed", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not exit after Close()")
	}
}

func TestCloseClosesActiveConnections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	logger := &memLogger{}
	var s netx.Server[string]
	s.Logger = logger

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(ctx, ln) }()

	// Handler that matches and returns immediately, leaving the connection open
	s.SetRoute("id", func(_ context.Context, conn net.Conn, closed func()) (bool, io.Closer) {
		return true, conn
	})

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// Give the server a moment to register the connection
	time.Sleep(100 * time.Millisecond)

	// Closing the server should close the client connection too
	if err := s.Close(); err != nil && !errors.Is(err, netx.ErrServerClosed) {
		t.Fatalf("server close: %v", err)
	}

	_ = c.SetWriteDeadline(time.Now().Add(1 * time.Second))
	if _, err := c.Write([]byte("ping")); err == nil {
		// If write succeeded, a subsequent read should fail/EOF
		_ = c.SetReadDeadline(time.Now().Add(1 * time.Second))
		buf := make([]byte, 4)
		if _, rErr := c.Read(buf); rErr == nil {
			t.Fatalf("expected read error/EOF after server Close, got nil")
		}
	}
	_ = c.Close()

	select {
	case got := <-errCh:
		if !errors.Is(got, netx.ErrServerClosed) {
			t.Fatalf("serve returned %v, want ErrServerClosed", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not exit after Close()")
	}
}

func TestConcurrentSetRouteNoLoss(t *testing.T) {
	t.Parallel()
	var s netx.Server[int]
	ctx := context.Background()
	logger := &memLogger{}
	s.Logger = logger

	// Concurrently add many distinct routes
	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			s.SetRoute(i, func(_ context.Context, conn net.Conn, closed func()) (bool, io.Closer) {
				return false, conn
			})
		}()
	}
	wg.Wait()

	// Inspect routes by adding a special route that we can observe by attempting connections
	// Since routes are internal, we verify indirectly by racing a RemoveRoute for each id and
	// ensure it doesn't panic and after removals, a connection is unhandled with no crash.
	for i := 0; i < n; i++ {
		s.RemoveRoute(i)
	}

	// Start the server with no routes and ensure it handles gracefully under connections
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(ctx, ln) }()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.SetDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1)
	_, _ = c.Read(buf) // should be dropped
	_ = c.Close()

	_ = s.Close()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not exit after Close()")
	}
}

func TestShutdownGraceful(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	logger := &memLogger{}
	var s netx.Server[string]
	s.Logger = logger

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(ctx, ln) }()

	// Handler that reads until client closes then signals closed()
	s.SetRoute("id", func(_ context.Context, conn net.Conn, closed func()) (bool, io.Closer) {
		go func() {
			buf := make([]byte, 1024)
			for {
				if _, err := conn.Read(buf); err != nil {
					break
				}
			}
			closed()
		}()
		return true, conn
	})

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// Initiate graceful shutdown with sufficient timeout; while waiting, close client
	sdCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Shutdown(sdCtx) }()

	// Give server a moment, then close client to simulate graceful completion
	time.Sleep(50 * time.Millisecond)
	_ = c.Close()

	if err := <-done; err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	select {
	case got := <-errCh:
		if !errors.Is(got, netx.ErrServerClosed) {
			t.Fatalf("serve returned %v, want ErrServerClosed", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not exit after Shutdown()")
	}
}

func TestShutdownTimeoutForcesClose(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	logger := &memLogger{}
	var s netx.Server[string]
	s.Logger = logger

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(ctx, ln) }()

	// Handler reads an initial byte to ensure it blocks until client writes;
	// after reading, it returns and the server will track the connection.
	ready := make(chan struct{})
	s.SetRoute("id", func(_ context.Context, conn net.Conn, closed func()) (bool, io.Closer) {
		go func() {
			// signal that handler is installed
			close(ready)
			buf := make([]byte, 1)
			_, _ = io.ReadFull(conn, buf)
			// do not call closed(); keep connection open for shutdown to force-close
		}()
		return true, conn
	})

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// Wait until handler goroutine is ready, then write the sync byte so handler returns and server tracks the conn
	<-ready
	if _, err := c.Write([]byte{0x1}); err != nil {
		t.Fatalf("client sync write: %v", err)
	}
	// Small delay to let server add to tracked conns deterministically
	time.Sleep(10 * time.Millisecond)

	// Shutdown with short timeout; expect context deadline error
	sdCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = s.Shutdown(sdCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want DeadlineExceeded", err)
	}
	if time.Since(start) < 80*time.Millisecond { // guard against returning too early
		t.Fatalf("Shutdown returned too quickly")
	}

	// Connection should be closed by server force-close
	_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 1)
	if _, rErr := c.Read(buf); rErr == nil {
		t.Fatalf("expected read error after forced shutdown")
	}
	_ = c.Close()

	// Serve should exit after Shutdown forced close
	select {
	case got := <-errCh:
		if !errors.Is(got, netx.ErrServerClosed) {
			t.Fatalf("serve returned %v, want ErrServerClosed", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not exit after forced Shutdown()")
	}
}
