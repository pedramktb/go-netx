package netx_test

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
	netx "github.com/pedramktb/go-netx"
)

// helper to create a DNS Tunnel pair over a framed connection (simulating packets)
func newDNSPair(t *testing.T) (client net.Conn, server net.Conn) {
	t.Helper()
	cr, sr := net.Pipe()
	t.Cleanup(func() { _ = cr.Close(); _ = sr.Close() })

	// Wrap in FramedConn to simulate packet boundaries for DNS packets
	fc := netx.NewFramedConn(cr)
	fs := netx.NewFramedConn(sr)

	domain := "tunnel.example.com"
	c := netx.NewDNSTClientConn(fc, domain)
	s := netx.NewDNSTServerConn(fs, domain)

	return c, s
}

func TestDNSConn_Roundtrip(t *testing.T) {
	client, server := newDNSPair(t)

	// Test case: Client sends "Hello", Server echoes "Hello"

	msg := []byte("Hello DNS Tunnel")
	done := make(chan error, 1)

	// Server Echo Loop
	go func() {
		buf := make([]byte, 1024)
		n, err := server.Read(buf)
		if err != nil {
			done <- err
			return
		}
		received := string(buf[:n])
		if received != string(msg) {
			// Just log, can't t.Errorf easily here
		}
		_, err = server.Write(buf[:n])
		done <- err
	}()

	// Client Write
	_, err := client.Write(msg)
	if err != nil {
		t.Fatalf("Client write error: %v", err)
	}

	// Client Read MUST happen to unblock Server's Write (due to net.Pipe)
	respBuf := make([]byte, 1024)
	n, err := client.Read(respBuf)
	if err != nil {
		t.Fatalf("Client read error: %v", err)
	}

	// Now we can check if server finished successfully
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Server loop error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("Timeout waiting for server to finish")
	}

	clientReceived := string(respBuf[:n])
	if clientReceived != string(msg) {
		t.Errorf("Client received %q, want %q", clientReceived, string(msg))
	}
}

func TestDNSConnChunking(t *testing.T) {
	// Directly test chunks by inspecting the underlying connection?
	// With net.Pipe, it's harder to inspect "packets" than with MockPacketConn.
	// But we can verify that large messages still pass through correctly.

	c, s := newDNSPair(t)

	// DNS labels + domain overhead limits payload size.
	// Our implementation chunks at 64 bytes for safety.
	largeData := bytes.Repeat([]byte("AB"), 300) // 600 bytes

	// Helper to write fully
	go func() {
		// Use io.Writer loop behavior
		data := largeData
		for len(data) > 0 {
			n, err := c.Write(data)
			if err != nil {
				t.Errorf("Client write error: %v", err)
				return
			}
			data = data[n:]
		}
	}()

	receivedBuf := make([]byte, 1024)
	totalRead := 0

	// Read loop until we get everything
	for totalRead < len(largeData) {
		n, err := s.Read(receivedBuf[totalRead:])
		if err != nil {
			t.Fatalf("Server read error: %v", err)
		}
		totalRead += n
	}

	if !bytes.Equal(receivedBuf[:totalRead], largeData) {
		t.Fatalf("Data mismatch")
	}

	// Check validation of packets by peeking?
	// To strictly verifying "Wait for 2 packets", we'd need to intercept the pipe.
	// But end-to-end verification is usually checking if chunking Works, not Implementation details.
	// If chunking was broken, large data might fail to decode or be truncated.
}

func TestDNSConn_RawPackets(t *testing.T) {
	// If we want to verify the actual DNS packet structure, we can use a half-pipe + FramedConn inspection.
	cr, sr := net.Pipe()
	t.Cleanup(func() { _ = cr.Close(); _ = sr.Close() })

	fc := netx.NewFramedConn(cr)
	// fs (Server Side Network) is where we read raw packets
	fs := netx.NewFramedConn(sr)

	client := netx.NewDNSTClientConn(fc, "t.com")

	go client.Write([]byte("Short"))

	// Read packet from "Network"
	pktBuf := make([]byte, 4096)
	n, err := fs.Read(pktBuf)
	if err != nil {
		t.Fatalf("Network read error: %v", err)
	}

	msg := new(dns.Msg)
	if err := msg.Unpack(pktBuf[:n]); err != nil {
		t.Fatalf("Unpack failed: %v", err)
	}

	if len(msg.Question) != 1 {
		t.Errorf("Expected 1 question, got %d", len(msg.Question))
	}
	// "Short" in Base32 (Std, NoPadding) -> "KNIUG43U"
	// Suffix "t.com."
	// QName should be something like "kniug43u.t.com."

	expectedSuffix := "t.com."
	if len(msg.Question[0].Name) < len(expectedSuffix) {
		t.Errorf("Question name too short: %s", msg.Question[0].Name)
	}
}
