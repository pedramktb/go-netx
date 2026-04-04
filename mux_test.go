package netx_test

import (
	"bytes"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	netx "github.com/pedramktb/go-netx"
)

// helper: start a TCP listener that accepts connections
// and feeds them through a channel for test coordination.
func tcpListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func TestMux_SingleConnection(t *testing.T) {
	ln := tcpListener(t)
	lc := netx.NewMux(ln)
	defer lc.Close()

	msg := []byte("hello listener conn")

	go func() {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Errorf("dial: %v", err)
			return
		}
		defer c.Close()
		if _, err := c.Write(msg); err != nil {
			t.Errorf("write: %v", err)
		}
	}()

	buf := make([]byte, 256)
	var tag any
	n, err := lc.ReadTagged(buf, &tag)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(buf[:n], msg) {
		t.Fatalf("got %q, want %q", buf[:n], msg)
	}
	if tag == nil {
		t.Fatal("expected non-nil tag")
	}
	if _, ok := tag.(net.Conn); !ok {
		t.Fatalf("tag is %T, want net.Conn", tag)
	}
}

func TestMux_WriteBack(t *testing.T) {
	ln := tcpListener(t)
	lc := netx.NewMux(ln)
	defer lc.Close()

	request := []byte("ping")
	response := []byte("pong")

	var wg sync.WaitGroup

	wg.Go(func() {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Errorf("dial: %v", err)
			return
		}
		defer c.Close()

		if _, err := c.Write(request); err != nil {
			t.Errorf("write: %v", err)
			return
		}

		buf := make([]byte, 256)
		n, err := c.Read(buf)
		if err != nil {
			t.Errorf("read response: %v", err)
			return
		}
		if !bytes.Equal(buf[:n], response) {
			t.Errorf("response: got %q, want %q", buf[:n], response)
		}
	})

	// Server side: read request, write response back on the same underlying conn.
	buf := make([]byte, 256)
	var tag any
	n, err := lc.ReadTagged(buf, &tag)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(buf[:n], request) {
		t.Fatalf("request: got %q, want %q", buf[:n], request)
	}
	if _, err := lc.WriteTagged(response, tag); err != nil {
		t.Fatalf("write: %v", err)
	}

	wg.Wait()
}

func TestMux_MultipleConnections(t *testing.T) {
	ln := tcpListener(t)
	lc := netx.NewMux(ln)
	defer lc.Close()

	messages := []string{"first", "second", "third"}

	go func() {
		for _, msg := range messages {
			c, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Errorf("dial: %v", err)
				return
			}
			if _, err := c.Write([]byte(msg)); err != nil {
				t.Errorf("write %q: %v", msg, err)
				c.Close()
				return
			}
			c.Close()
		}
	}()

	seen := make(map[string]bool)
	for range messages {
		buf := make([]byte, 256)
		var tag any
		n, err := lc.ReadTagged(buf, &tag)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		seen[string(buf[:n])] = true
	}
	for _, want := range messages {
		if !seen[want] {
			t.Fatalf("message %q not received", want)
		}
	}
}

func TestMux_ConcurrentConnections(t *testing.T) {
	// Multiple connections may be open simultaneously; each write must arrive
	// on the same connection the read came from.
	ln := tcpListener(t)
	lc := netx.NewMux(ln)
	defer lc.Close()

	const n = 4
	var wg sync.WaitGroup
	wg.Add(n)

	for i := range n {
		payload := []byte{byte('A' + i)}
		go func(payload []byte) {
			defer wg.Done()
			c, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Errorf("dial: %v", err)
				return
			}
			defer c.Close()
			if _, err := c.Write(payload); err != nil {
				t.Errorf("write: %v", err)
				return
			}
			buf := make([]byte, 8)
			nn, err := c.Read(buf)
			if err != nil {
				t.Errorf("read response: %v", err)
				return
			}
			if !bytes.Equal(buf[:nn], bytes.ToLower(payload)) {
				t.Errorf("got %q, want %q", buf[:nn], bytes.ToLower(payload))
			}
		}(payload)
	}

	// Server: echo lowercased back on the exact source connection.
	for i := range n {
		buf := make([]byte, 256)
		var tag any
		nn, err := lc.ReadTagged(buf, &tag)
		if err != nil {
			t.Fatalf("ReadTagged %d: %v", i, err)
		}
		resp := bytes.ToLower(buf[:nn])
		if _, err := lc.WriteTagged(resp, tag); err != nil {
			t.Fatalf("WriteTagged %d: %v", i, err)
		}
	}

	wg.Wait()
}

func TestMux_RequestResponseAcrossConnections(t *testing.T) {
	ln := tcpListener(t)
	lc := netx.NewMux(ln)
	defer lc.Close()

	rounds := 3
	var wg sync.WaitGroup
	wg.Add(rounds)

	go func() {
		for i := range rounds {
			c, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Errorf("dial %d: %v", i, err)
				wg.Done()
				continue
			}

			msg := []byte{byte('A' + i)}
			if _, err := c.Write(msg); err != nil {
				t.Errorf("write %d: %v", i, err)
				c.Close()
				wg.Done()
				continue
			}

			buf := make([]byte, 256)
			n, err := c.Read(buf)
			if err != nil {
				t.Errorf("read resp %d: %v", i, err)
				c.Close()
				wg.Done()
				continue
			}
			if !bytes.Equal(buf[:n], bytes.ToLower(msg)) {
				t.Errorf("round %d: got %q, want %q", i, buf[:n], bytes.ToLower(msg))
			}
			c.Close()
			wg.Done()
		}
	}()

	for i := range rounds {
		buf := make([]byte, 256)
		var tag any
		n, err := lc.ReadTagged(buf, &tag)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		resp := bytes.ToLower(buf[:n])
		if _, err := lc.WriteTagged(resp, tag); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	wg.Wait()
}

func TestMux_Close(t *testing.T) {
	ln := tcpListener(t)
	lc := netx.NewMux(ln)

	if err := lc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Read after close should return error
	buf := make([]byte, 256)
	var tag any
	_, err := lc.ReadTagged(buf, &tag)
	if err == nil {
		t.Fatal("expected error on read after close")
	}

	// Write after close should return error
	_, err = lc.WriteTagged([]byte("data"), nil)
	if err == nil {
		t.Fatal("expected error on write after close")
	}

	// Double close should not panic
	if err := lc.Close(); err != nil {
		t.Fatalf("double close: %v", err)
	}
}

func TestMux_WriteInvalidTag(t *testing.T) {
	ln := tcpListener(t)
	lc := netx.NewMux(ln)
	defer lc.Close()

	// WriteTagged with a nil or non-net.Conn tag should return an error.
	_, err := lc.WriteTagged([]byte("data"), nil)
	if err == nil {
		t.Fatal("expected error on WriteTagged with nil tag")
	}
	_, err = lc.WriteTagged([]byte("data"), "not-a-conn")
	if err == nil {
		t.Fatal("expected error on WriteTagged with wrong tag type")
	}
}

func TestMux_LocalAddr(t *testing.T) {
	ln := tcpListener(t)
	lc := netx.NewMux(ln)
	defer lc.Close()

	if lc.LocalAddr().String() != ln.Addr().String() {
		t.Fatalf("LocalAddr: got %v, want %v", lc.LocalAddr(), ln.Addr())
	}
}

func TestMux_Deadlines(t *testing.T) {
	ln := tcpListener(t)
	lc := netx.NewMux(ln)
	defer lc.Close()

	// Set a very short read deadline before any connection exists.
	if err := lc.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}

	buf := make([]byte, 256)
	var tag any
	// No connection is dialled — ReadTagged should time out via the mux-level deadline.
	_, err := lc.ReadTagged(buf, &tag)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("expected timeout, got %v", err)
	}
}
