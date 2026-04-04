package netx_test

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	netx "github.com/pedramktb/go-netx"
)

// helper: returns a dial function that connects to a TCP listener.
func tcpDialer(t *testing.T, addr string) netx.Dialer {
	t.Helper()
	return func() (net.Conn, error) {
		return net.Dial("tcp", addr)
	}
}

func TestMuxClient_SingleConnection(t *testing.T) {
	ln := tcpListener(t)
	dc := netx.NewMuxClient(tcpDialer(t, ln.Addr().String()))
	defer dc.Close()

	msg := []byte("hello mux client")

	var wg sync.WaitGroup
	wg.Go(func() {
		c, err := ln.Accept()
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer c.Close()
		buf := make([]byte, 256)
		n, err := c.Read(buf)
		if err != nil {
			t.Errorf("read: %v", err)
			return
		}
		// Echo back
		if _, err := c.Write(buf[:n]); err != nil {
			t.Errorf("write: %v", err)
		}
	})

	// Write through the MuxClient (triggers dial)
	if _, err := dc.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, 256)
	n, err := dc.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(buf[:n], msg) {
		t.Fatalf("got %q, want %q", buf[:n], msg)
	}

	wg.Wait()
}

func TestMuxClient_MultipleConnections(t *testing.T) {
	ln := tcpListener(t)
	dc := netx.NewMuxClient(tcpDialer(t, ln.Addr().String()))
	defer dc.Close()

	messages := []string{"first", "second", "third"}

	// Server: accept connections, send a message, and close.
	// Each accepted connection carries one message.
	go func() {
		for _, msg := range messages {
			c, err := ln.Accept()
			if err != nil {
				t.Errorf("accept: %v", err)
				return
			}
			if _, err := c.Write([]byte(msg)); err != nil {
				t.Errorf("write %q: %v", msg, err)
			}
			c.Close()
		}
	}()

	// Client: read all messages through a single MuxClient.
	// MuxClient dials on the first Read (current is nil) and redials
	// transparently whenever it encounters EOF.
	for _, want := range messages {
		buf := make([]byte, 256)
		n, err := dc.Read(buf)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(buf[:n]) != want {
			t.Fatalf("got %q, want %q", buf[:n], want)
		}
	}
}

func TestMuxClient_RequestResponse(t *testing.T) {
	ln := tcpListener(t)
	dc := netx.NewMuxClient(tcpDialer(t, ln.Addr().String()))
	defer dc.Close()

	rounds := 3
	var wg sync.WaitGroup

	// Server: accept one connection and handle multiple request-response
	// rounds on it (connection stays open).
	wg.Go(func() {
		c, err := ln.Accept()
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer c.Close()
		for i := 0; i < rounds; i++ {
			buf := make([]byte, 256)
			n, err := c.Read(buf)
			if err != nil {
				t.Errorf("read %d: %v", i, err)
				return
			}
			if _, err := c.Write(buf[:n]); err != nil {
				t.Errorf("write %d: %v", i, err)
				return
			}
		}
	})

	for i := range rounds {
		msg := []byte{byte('A' + i)}
		if _, err := dc.Write(msg); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		buf := make([]byte, 256)
		n, err := dc.Read(buf)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if !bytes.Equal(buf[:n], msg) {
			t.Fatalf("round %d: got %q, want %q", i, buf[:n], msg)
		}
	}

	wg.Wait()
}

func TestMuxClient_Close(t *testing.T) {
	ln := tcpListener(t)
	dc := netx.NewMuxClient(tcpDialer(t, ln.Addr().String()))

	if err := dc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Read after close should return error
	buf := make([]byte, 256)
	_, err := dc.Read(buf)
	if err == nil {
		t.Fatal("expected error on read after close")
	}

	// Write after close should return error
	_, err = dc.Write([]byte("data"))
	if err == nil {
		t.Fatal("expected error on write after close")
	}

	// Double close should not panic
	if err := dc.Close(); err != nil {
		t.Fatalf("double close: %v", err)
	}
}

func TestMuxClient_WriteTriggersDialOnNoConnection(t *testing.T) {
	ln := tcpListener(t)
	dc := netx.NewMuxClient(tcpDialer(t, ln.Addr().String()))
	defer dc.Close()

	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		// drain
		_, _ = io.Copy(io.Discard, c)
	}()

	// First write should trigger a dial
	if _, err := dc.Write([]byte("trigger")); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestMuxClient_Deadlines(t *testing.T) {
	ln := tcpListener(t)
	dc := netx.NewMuxClient(tcpDialer(t, ln.Addr().String()))
	defer dc.Close()

	// Set a very short read deadline before any connection exists
	if err := dc.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}

	// Accept on server side so the client dial succeeds
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		// Hold open — never send data
		time.Sleep(2 * time.Second)
	}()

	// Trigger dial by writing
	if _, err := dc.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, 256)
	_, err := dc.Read(buf)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestMuxClient_DialError(t *testing.T) {
	dialErr := errors.New("dial failed")
	dc := netx.NewMuxClient(func() (net.Conn, error) {
		return nil, dialErr
	})
	defer dc.Close()

	_, err := dc.Write([]byte("data"))
	if !errors.Is(err, dialErr) {
		t.Fatalf("expected dial error, got %v", err)
	}

	_, err = dc.Read(make([]byte, 256))
	if !errors.Is(err, dialErr) {
		t.Fatalf("expected dial error, got %v", err)
	}
}
