package netx_test

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	netx "github.com/pedramktb/go-netx"
)

// --- Message-oriented pipe for testing PollConn ---
// Unlike net.Pipe, this sends complete messages via channels so that
// empty writes still trigger a round-trip (required by PollConn).

type msgPipeAddr string

func (a msgPipeAddr) Network() string { return "msgpipe" }
func (a msgPipeAddr) String() string  { return string(a) }

type msgPipeConn struct {
	rCh    <-chan []byte
	wCh    chan<- []byte
	local  net.Addr
	remote net.Addr
	done   chan struct{}
	once   sync.Once
}

func newMsgPipe() (*msgPipeConn, *msgPipeConn) {
	ch1 := make(chan []byte, 16)
	ch2 := make(chan []byte, 16)

	c1 := &msgPipeConn{
		rCh: ch2, wCh: ch1,
		local: msgPipeAddr("c1"), remote: msgPipeAddr("c2"),
		done: make(chan struct{}),
	}
	c2 := &msgPipeConn{
		rCh: ch1, wCh: ch2,
		local: msgPipeAddr("c2"), remote: msgPipeAddr("c1"),
		done: make(chan struct{}),
	}
	return c1, c2
}

func (c *msgPipeConn) Read(b []byte) (int, error) {
	select {
	case data, ok := <-c.rCh:
		if !ok {
			return 0, io.EOF
		}
		return copy(b, data), nil
	case <-c.done:
		return 0, net.ErrClosed
	}
}

func (c *msgPipeConn) Write(b []byte) (int, error) {
	data := make([]byte, len(b))
	copy(data, b)
	select {
	case c.wCh <- data:
		return len(b), nil
	case <-c.done:
		return 0, net.ErrClosed
	}
}

func (c *msgPipeConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return nil
}

func (c *msgPipeConn) LocalAddr() net.Addr                { return c.local }
func (c *msgPipeConn) RemoteAddr() net.Addr               { return c.remote }
func (c *msgPipeConn) SetDeadline(t time.Time) error      { return nil }
func (c *msgPipeConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *msgPipeConn) SetWriteDeadline(t time.Time) error { return nil }

// reqRespServer runs a request-response loop: reads a request, calls handler, writes the response.
func reqRespServer(conn *msgPipeConn, handler func(req []byte) []byte) {
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		resp := handler(buf[:n])
		if _, err := conn.Write(resp); err != nil {
			return
		}
	}
}

// --- Tests ---

func TestPollConn_Echo(t *testing.T) {
	clientConn, serverConn := newMsgPipe()

	go reqRespServer(serverConn, func(req []byte) []byte {
		return req // echo
	})

	pc := netx.NewPollConn(clientConn, netx.WithPollInterval(10*time.Millisecond))
	defer pc.Close()

	msg := []byte("hello world")
	if _, err := pc.Write(msg); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	buf := make([]byte, 1024)
	_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := pc.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if !bytes.Equal(buf[:n], msg) {
		t.Errorf("Expected %q, got %q", msg, buf[:n])
	}
}

func TestPollConn_ServerInitiated(t *testing.T) {
	clientConn, serverConn := newMsgPipe()

	serverMsg := []byte("server says hello")
	sent := false

	go reqRespServer(serverConn, func(req []byte) []byte {
		if !sent {
			sent = true
			return serverMsg
		}
		return nil // empty response for subsequent polls
	})

	pc := netx.NewPollConn(clientConn, netx.WithPollInterval(10*time.Millisecond))
	defer pc.Close()

	// Don't write anything — just read. Polling should pick up server data.
	buf := make([]byte, 1024)
	_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := pc.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if !bytes.Equal(buf[:n], serverMsg) {
		t.Errorf("Expected %q, got %q", serverMsg, buf[:n])
	}
}

func TestPollConn_MultipleMessages(t *testing.T) {
	clientConn, serverConn := newMsgPipe()

	go reqRespServer(serverConn, func(req []byte) []byte {
		return req // echo
	})

	pc := netx.NewPollConn(clientConn, netx.WithPollInterval(10*time.Millisecond))
	defer pc.Close()

	messages := []string{"msg1", "msg2", "msg3", "msg4", "msg5"}
	for i, msg := range messages {
		if _, err := pc.Write([]byte(msg)); err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}

		buf := make([]byte, 1024)
		_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := pc.Read(buf)
		if err != nil {
			t.Fatalf("Read %d failed: %v", i, err)
		}
		if string(buf[:n]) != msg {
			t.Errorf("Message %d: expected %q, got %q", i, msg, buf[:n])
		}
	}
}

func TestPollConn_Close(t *testing.T) {
	clientConn, serverConn := newMsgPipe()

	go reqRespServer(serverConn, func(req []byte) []byte {
		return req
	})

	pc := netx.NewPollConn(clientConn, netx.WithPollInterval(10*time.Millisecond))

	// Let the poll loop start
	time.Sleep(20 * time.Millisecond)

	if err := pc.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Read should fail
	buf := make([]byte, 1024)
	_ = pc.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, err := pc.Read(buf)
	if err == nil {
		t.Error("Expected error reading from closed PollConn")
	}

	// Write should fail
	_, err = pc.Write([]byte("data"))
	if err == nil {
		t.Error("Expected error writing to closed PollConn")
	}
}

func TestPollConn_ReadDeadline(t *testing.T) {
	clientConn, serverConn := newMsgPipe()

	// Server never responds with data (always empty)
	go reqRespServer(serverConn, func(req []byte) []byte {
		return nil
	})

	pc := netx.NewPollConn(clientConn, netx.WithPollInterval(10*time.Millisecond))
	defer pc.Close()

	buf := make([]byte, 1024)
	_ = pc.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	_, err := pc.Read(buf)
	if err == nil {
		t.Error("Expected deadline exceeded error")
	}
}

func TestPollConn_ConcurrentReadWrite(t *testing.T) {
	clientConn, serverConn := newMsgPipe()

	go reqRespServer(serverConn, func(req []byte) []byte {
		return req // echo
	})

	pc := netx.NewPollConn(clientConn, netx.WithPollInterval(10*time.Millisecond))
	defer pc.Close()

	const numMessages = 10
	received := make(chan string, numMessages)

	// Reader goroutine
	go func() {
		buf := make([]byte, 1024)
		for {
			_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, err := pc.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				received <- string(buf[:n])
			}
		}
	}()

	// Writer goroutine
	for i := range numMessages {
		msg := []byte("concurrent-msg")
		_ = i
		if _, err := pc.Write(msg); err != nil {
			t.Fatalf("Write failed: %v", err)
		}
		// Small delay to ensure ordering in the request-response cycle
		time.Sleep(15 * time.Millisecond)
	}

	// Collect responses
	deadline := time.After(5 * time.Second)
	count := 0
	for count < numMessages {
		select {
		case <-received:
			count++
		case <-deadline:
			t.Fatalf("Timed out waiting for responses, got %d/%d", count, numMessages)
		}
	}
}

func TestPollConn_SmallReadBuffer(t *testing.T) {
	clientConn, serverConn := newMsgPipe()

	go reqRespServer(serverConn, func(req []byte) []byte {
		return req
	})

	pc := netx.NewPollConn(clientConn, netx.WithPollInterval(10*time.Millisecond))
	defer pc.Close()

	msg := []byte("hello world, this is a longer message")
	if _, err := pc.Write(msg); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Read with a small buffer — should get partial data, then remainder
	var result []byte
	buf := make([]byte, 5)
	for len(result) < len(msg) {
		_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := pc.Read(buf)
		if err != nil {
			t.Fatalf("Read failed after %d bytes: %v", len(result), err)
		}
		result = append(result, buf[:n]...)
	}

	if !bytes.Equal(result, msg) {
		t.Errorf("Expected %q, got %q", msg, result)
	}
}

// --- PollConn + PollServerConn integration tests over net.Pipe ---
// These tests use a real net.Pipe (stream, no message boundaries) together with
// FrameConn to preserve message boundaries, paired with PollServerConn so that
// the server properly handles empty polls and sends data back to the client.

func newPollPair(t *testing.T, opts ...netx.PollConnOption) (client net.Conn, server net.Conn) {
	t.Helper()
	rawClient, rawServer := net.Pipe()
	// Wrap both sides in FrameConn to give message-boundary semantics over the stream,
	// then in PollConn / PollServerConn for the request-response protocol.
	client = netx.NewPollConn(netx.NewFrameConn(rawClient), opts...)
	server = netx.NewPollServerConn(netx.NewFrameConn(rawServer), opts...)
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

func TestPollServerConn_Echo(t *testing.T) {
	client, server := newPollPair(t, netx.WithPollInterval(10*time.Millisecond))

	msg := []byte("hello from client")

	// Server: read the message and echo it back.
	go func() {
		buf := make([]byte, 1024)
		n, err := server.Read(buf)
		if err != nil {
			return
		}
		server.Write(buf[:n])
	}()

	if _, err := client.Write(msg); err != nil {
		t.Fatalf("client Write: %v", err)
	}

	buf := make([]byte, 1024)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("client Read: %v", err)
	}
	if !bytes.Equal(buf[:n], msg) {
		t.Errorf("echo: expected %q, got %q", msg, buf[:n])
	}
}

func TestPollServerConn_ServerInitiated(t *testing.T) {
	client, server := newPollPair(t, netx.WithPollInterval(10*time.Millisecond))

	serverMsg := []byte("server push")

	// Server: write data without waiting for a client message.
	go func() {
		time.Sleep(15 * time.Millisecond) // let the poll loop start
		server.Write(serverMsg)
	}()

	buf := make([]byte, 1024)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("client Read: %v", err)
	}
	if !bytes.Equal(buf[:n], serverMsg) {
		t.Errorf("server push: expected %q, got %q", serverMsg, buf[:n])
	}
}

func TestPollServerConn_BidirectionalExchange(t *testing.T) {
	client, server := newPollPair(t, netx.WithPollInterval(10*time.Millisecond))

	const rounds = 5
	done := make(chan struct{})

	// Server: echo loop.
	go func() {
		defer close(done)
		buf := make([]byte, 1024)
		for i := 0; i < rounds; i++ {
			_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, err := server.Read(buf)
			if err != nil {
				return
			}
			server.Write(buf[:n])
		}
	}()

	buf := make([]byte, 1024)
	for i := 0; i < rounds; i++ {
		msg := []byte("round")
		if _, err := client.Write(msg); err != nil {
			t.Fatalf("round %d Write: %v", i, err)
		}
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := client.Read(buf)
		if err != nil {
			t.Fatalf("round %d Read: %v", i, err)
		}
		if !bytes.Equal(buf[:n], msg) {
			t.Errorf("round %d: expected %q, got %q", i, msg, buf[:n])
		}
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("server echo loop timed out")
	}
}

func TestPollServerConn_ClosePropagatesToClient(t *testing.T) {
	client, server := newPollPair(t, netx.WithPollInterval(10*time.Millisecond))

	go func() {
		time.Sleep(20 * time.Millisecond)
		server.Close()
	}()

	buf := make([]byte, 1024)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err := client.Read(buf)
	if err == nil {
		t.Error("expected error after server close, got nil")
	}
}

// TestPollServerConn_IdleTimeout verifies that WithPollTimeout causes the server
// connection to close when the client stops polling. This replicates the WireGuard
// reconnect scenario: the underlying transport (demux session) stays open but the
// poll client goes silent; the server must detect this and tear down the old virtual
// session so that the reconnecting client gets a fresh one.
func TestPollServerConn_IdleTimeout(t *testing.T) {
	rawClient, rawServer := net.Pipe()
	defer rawClient.Close()

	const timeout = 50 * time.Millisecond
	server := netx.NewPollServerConn(rawServer, netx.WithPollTimeout(timeout))
	defer server.Close()

	// rawClient is held open (live connection) but sends nothing, simulating a
	// peer that has stopped polling while the transport layer stays up.

	buf := make([]byte, 64)
	start := time.Now()
	// Give the test a generous outer deadline to avoid hanging on regressions.
	_ = server.SetReadDeadline(time.Now().Add(10 * timeout))
	_, err := server.Read(buf)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected error from server after idle timeout, got nil")
	}
	if elapsed > 5*timeout {
		t.Errorf("server took too long to detect idle: %v (timeout=%v)", elapsed, timeout)
	}
}
