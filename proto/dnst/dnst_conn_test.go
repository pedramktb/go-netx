package netx

import (
	"bytes"
	"net"
	"testing"
	"time"
)

func TestDNST_EndToEnd(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer l.Close()

	errCh := make(chan error, 1)

	// Server Logic
	go func() {
		conn, err := l.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()

		serverConn := NewDNSTServerConn(conn, "example.com")

		// Read request
		buf := make([]byte, 1024)
		var tag any
		n, err := serverConn.ReadTagged(buf, &tag)
		if err != nil {
			errCh <- err
			return
		}
		readData := buf[:n]

		// Echo back
		_, err = serverConn.WriteTagged(readData, tag)
		if err != nil {
			errCh <- err
			return
		}
		close(errCh)
	}()

	// Client Logic
	// Wait a moment for server to be ready (though Listen is synchronous, Accept is in goroutine)
	// Actually Dial will wait/retry a bit usually, but local is fast.

	conn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatalf("Failed to dial: %v", err)
	}
	defer conn.Close()

	// Wrap with DNST Client
	clientConn := NewDNSTClientConn(conn, "example.com")

	message := []byte("hello world")
	_, err = clientConn.Write(message)
	if err != nil {
		t.Fatalf("Failed to write to client: %v", err)
	}

	buf := make([]byte, 1024)
	clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := clientConn.Read(buf)
	if err != nil {
		t.Fatalf("Failed to read from client: %v", err)
	}

	if !bytes.Equal(message, buf[:n]) {
		t.Errorf("Expected %s, got %s", message, buf[:n])
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Server error: %v", err)
		}
	case <-time.After(1 * time.Second):
		// No error from server
	}
}

func TestDNST_PacketSize(t *testing.T) {
	p1, p2 := net.Pipe()

	// Server on p1
	serverConn := NewDNSTServerConn(p1, "tunnel.com")

	// Client on p2
	clientConn := NewDNSTClientConn(p2, "tunnel.com")

	data := []byte("test data payload")

	go func() {
		defer p1.Close()
		buf := make([]byte, 1024)
		var tag any
		n, err := serverConn.ReadTagged(buf, &tag)
		if err != nil {
			return
		}
		// Echo
		serverConn.WriteTagged(buf[:n], tag)
	}()

	defer p2.Close()
	_, err := clientConn.Write(data)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	buf := make([]byte, 1024)
	n, err := clientConn.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if !bytes.Equal(data, buf[:n]) {
		t.Errorf("Packet content mismatch. Want %s, Got %s", data, buf[:n])
	}
}
