package netx_test

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/pedramktb/go-netx"
)

func TestTunRelayBidirectional(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	logger := &memLogger{}
	var m netx.TunMaster[string]
	m.Logger = logger

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- m.Serve(ctx, ln) }()

	peerCh := make(chan net.Conn, 1)
	m.SetRoute("id", func(connCtx context.Context, conn net.Conn) (bool, context.Context, netx.Tun) {
		a, b := net.Pipe() // a is server-side peer, b is test-side peer
		peerCh <- b
		return true, connCtx, netx.Tun{Logger: logger, Conn: conn, Peer: a}
	})

	// Connect client
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))

	// Get the peer end created by handler
	peer := <-peerCh
	t.Cleanup(func() { _ = peer.Close() })
	_ = peer.SetDeadline(time.Now().Add(2 * time.Second))

	// client -> peer
	msg1 := []byte("hello from client")
	if _, err := c.Write(msg1); err != nil {
		t.Fatalf("client write: %v", err)
	}
	buf := make([]byte, len(msg1))
	if _, err := io.ReadFull(peer, buf); err != nil {
		t.Fatalf("peer read: %v", err)
	}
	if string(buf) != string(msg1) {
		t.Fatalf("mismatch: got %q want %q", string(buf), string(msg1))
	}

	// peer -> client
	msg2 := []byte("hello from peer")
	if _, err := peer.Write(msg2); err != nil {
		t.Fatalf("peer write: %v", err)
	}
	buf2 := make([]byte, len(msg2))
	if _, err := io.ReadFull(c, buf2); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(buf2) != string(msg2) {
		t.Fatalf("mismatch: got %q want %q", string(buf2), string(msg2))
	}

	// Close ends to let tunnel finish
	_ = c.Close()
	_ = peer.Close()

	// Graceful shutdown should complete quickly
	sdCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Shutdown(sdCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
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

func TestTunShutdownTimeoutForcesClose(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	logger := &memLogger{}
	var m netx.TunMaster[string]
	m.Logger = logger

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- m.Serve(ctx, ln) }()

	peerCh := make(chan net.Conn, 1)
	ready := make(chan struct{})
	m.SetRoute("id", func(connCtx context.Context, conn net.Conn) (bool, context.Context, netx.Tun) {
		a, b := net.Pipe()
		close(ready)
		peerCh <- b
		return true, connCtx, netx.Tun{Logger: logger, Conn: conn, Peer: a}
	})

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	peer := <-peerCh
	_ = peer.SetDeadline(time.Now().Add(2 * time.Second))

	// Ensure the tunnel is set up and tracked before shutdown
	<-ready
	// Prove relay is active by sending a byte through the tunnel
	if _, err := c.Write([]byte("x")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	b := make([]byte, 1)
	if _, err := io.ReadFull(peer, b); err != nil {
		t.Fatalf("peer read: %v", err)
	}
	// Do not close endpoints; trigger forced shutdown
	sdCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := m.Shutdown(sdCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want DeadlineExceeded", err)
	}

	// After forced shutdown, reads/writes should error
	if _, err := c.Write([]byte("x")); err == nil {
		// expect error
		_ = c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		b := make([]byte, 1)
		if _, rErr := c.Read(b); rErr == nil {
			t.Fatalf("expected client read error after forced shutdown")
		}
	}
	if _, err := peer.Write([]byte("y")); err == nil {
		_ = peer.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		b := make([]byte, 1)
		if _, rErr := peer.Read(b); rErr == nil {
			t.Fatalf("expected peer read error after forced shutdown")
		}
	}
	_ = c.Close()
	_ = peer.Close()

	select {
	case got := <-errCh:
		if !errors.Is(got, netx.ErrServerClosed) {
			t.Fatalf("serve returned %v, want ErrServerClosed", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not exit after Shutdown()")
	}
}
