package netx_test

import (
	"bytes"
	"net"
	"testing"
	"time"

	netx "github.com/pedramktb/go-netx"
)

func newDNSTPair(t *testing.T) (client net.Conn, server net.Conn) {
	t.Helper()
	// net.Pipe() provides a synchronous, in-memory, full duplex network connection.
	// Both ends implement the Conn interface.
	// Reads and writes on the pipe are matched one to one.
	cr, sr := net.Pipe()
	t.Cleanup(func() { _ = cr.Close(); _ = sr.Close() })

	domain := "tunnel.example.com"

	client = netx.NewDNSTClientConn(cr, domain)
	server = netx.NewDNSTServerConn(sr, domain)

	return client, server
}

func TestDNST_Roundtrip(t *testing.T) {
	c, s := newDNSTPair(t)

	// Test Client -> Server (encoded in Question)
	msg1 := []byte("hello server")
	go func() {
		if _, err := c.Write(msg1); err != nil {
			t.Errorf("client write error: %v", err)
		}
	}()

	// Give time for write to happen (not strictly necessary with net.Pipe but safe)
	// Actually net.Pipe write blocks until read, so we must Read in main thread or vice versa.
	
	buf1 := make([]byte, len(msg1)+10) // buffer larger than msg
	n1, err := s.Read(buf1)
	if err != nil {
		t.Fatalf("server read error: %v", err)
	}
	if n1 != len(msg1) {
		t.Errorf("server read len mismatch: got %d, want %d", n1, len(msg1))
	}
	if !bytes.Equal(buf1[:n1], msg1) {
		t.Errorf("server read content mismatch: got %q, want %q", buf1[:n1], msg1)
	}

	// Test Server -> Client (encoded in TXT Answer)
	msg2 := []byte("hello client")
	go func() {
		if _, err := s.Write(msg2); err != nil {
			t.Errorf("server write error: %v", err)
		}
	}()

	buf2 := make([]byte, len(msg2)+10)
	n2, err := c.Read(buf2)
	if err != nil {
		t.Fatalf("client read error: %v", err)
	}
	if n2 != len(msg2) {
		t.Errorf("client read len mismatch: got %d, want %d", n2, len(msg2))
	}
	if !bytes.Equal(buf2[:n2], msg2) {
		t.Errorf("client read content mismatch: got %q, want %q", buf2[:n2], msg2)
	}
}

func TestDNST_LargePacket(t *testing.T) {
	c, s := newDNSTPair(t)

	// Calculate max payload size roughly.
	// Domain "tunnel.example.com" len is 18.
	// Client Write limitation: base32(data) <= 255 - 18 - 1 = 236.
	// max raw bytes ~= 236 * 5 / 8 = 147 bytes.

	payload := bytes.Repeat([]byte("A"), 140)

	errCh := make(chan error, 1)
	go func() {
		_, err := c.Write(payload)
		errCh <- err
	}()

	// If server read buffer is too small, this might fail with "dns: buffer too small" or "unpack error"
	buf := make([]byte, 1024)
	n, err := s.Read(buf)
	if err != nil {
		t.Fatalf("server read error: %v", err)
	}
	if n != len(payload) {
		t.Errorf("server read len %d, want %d", n, len(payload))
	}
	if !bytes.Equal(buf[:n], payload) {
		t.Errorf("server read content mismatch")
	}

	select {
	case writeErr := <-errCh:
		if writeErr != nil {
			t.Errorf("client write error: %v", writeErr)
		}
	case <-time.After(100 * time.Millisecond):
		t.Errorf("timed out waiting for write result")
	}
}
