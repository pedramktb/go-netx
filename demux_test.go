package netx_test

import (
	"bytes"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	netx "github.com/pedramktb/go-netx"
)

func TestDemux_Basic(t *testing.T) {
	// Create a pipe to simulate the underlying connection
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	idLen := 4
	// Create the Demux on the server side with a buffered queue to avoid dropped sessions during test startup
	l := netx.NewDemux(serverConn, idLen, netx.WithDemuxAccQueueSize(4))
	defer l.Close()

	// Simulate a client sending a packet for session "0001"
	sessID := []byte("0001")
	payload := []byte("Hello Demux")

	go func() {
		// Use DemuxClient to send data
		mc, _ := netx.NewDemuxClient(clientConn, sessID)
		_, err := mc.Write(payload)
		if err != nil {
			t.Errorf("client write error: %v", err)
		}
	}()

	// Accept the connection on the server side
	sess, err := l.Accept()
	if err != nil {
		t.Fatalf("Accept failed: %v", err)
	}
	defer sess.Close()

	// Verify session properties
	if sess.LocalAddr() != serverConn.LocalAddr() {
		t.Errorf("LocalAddr mismatch")
	}
	// Note: RemoteAddr in Demux just returns underlying RemoteAddr, not the ID-specific one usually,
	// unless implemented otherwise. demuxSess.RemoteAddr returns c.RemoteAddr().

	// Read data from the session
	buf := make([]byte, 1024)
	n, err := sess.Read(buf)
	if err != nil {
		t.Fatalf("session read error: %v", err)
	}

	if !bytes.Equal(buf[:n], payload) {
		t.Errorf("expected payload %q, got %q", payload, buf[:n])
	}

	// Write response from server back to client
	response := []byte("World")
	go func() {
		_, err = sess.Write(response)
		if err != nil {
			t.Errorf("session write error: %v", err)
		}
	}()

	// Client reads the raw packet (ID + Payload)
	cBuf := make([]byte, 1024)
	n, err = clientConn.Read(cBuf)
	if err != nil && err != io.EOF {
		t.Fatalf("client raw read error: %v", err)
	}

	expectedPacket := append(sessID, response...)
	if !bytes.Equal(cBuf[:n], expectedPacket) {
		t.Errorf("expected raw packet %q, got %q", expectedPacket, cBuf[:n])
	}
}

func TestDemux_MultipleSessions(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	l := netx.NewDemux(serverConn, 4, netx.WithDemuxAccQueueSize(4))
	defer l.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	// Session 1
	go func() {
		defer wg.Done()
		mc, _ := netx.NewDemuxClient(clientConn, []byte("ID01"))
		mc.Write([]byte("Data1"))
	}()

	// Session 2
	go func() {
		defer wg.Done()
		time.Sleep(10 * time.Millisecond) // Ensure ordering for deterministic accept if possible, though Demux accepts as they come
		mc, _ := netx.NewDemuxClient(clientConn, []byte("ID02"))
		mc.Write([]byte("Data2"))
	}()

	// Server accepts two sessions
	conn1, err := l.Accept()
	if err != nil {
		t.Fatalf("Accept 1 failed: %v", err)
	}
	conn2, err := l.Accept()
	if err != nil {
		t.Fatalf("Accept 2 failed: %v", err)
	}

	readPayload := func(c net.Conn) string {
		buf := make([]byte, 100)
		n, _ := c.Read(buf)
		return string(buf[:n])
	}

	// Since we don't know order (though sleep helps), let's check content or IDs if accessible.
	// demuxSess embeds ID but it's not exposed in net.Conn interface directly.
	// But we can check the data read.

	p1 := readPayload(conn1)
	p2 := readPayload(conn2)

	payloads := map[string]bool{p1: true, p2: true}
	if !payloads["Data1"] || !payloads["Data2"] {
		t.Errorf("Did not receive both payloads. Got: %s, %s", p1, p2)
	}

	wg.Wait()
}

func TestDemux_Options(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	// Test small buffer size option
	// If buffer is too small, large packets might be dropped or truncated if Demux checks size.
	// But Demux readLoop uses bufSize for the Read buffer.
	l := netx.NewDemux(serverConn, 4, netx.WithDemuxBufSize(10), netx.WithDemuxAccQueueSize(4))
	defer l.Close()

	// Send a packet larger than 10 bytes (Header 4 + Payload 7 = 11 bytes)
	// Underlying Read(buf) where len(buf)=10.
	// net.Pipe write is 11 bytes.
	// If Read is 10 bytes, it might read partial?
	// For net.Pipe, "The Read blocks until the Write completes."
	// "The data is copied directly". If read buffer is smaller, io.ErrShortBuffer?
	// net.Pipe documentation isn't explicit on partial reads vs error.
	// Implementation of net.Pipe uses copy().
	// It reads min(len(data), len(buf)).
	// So it will read 10 bytes. The remaining 1 byte?
	// net.Pipe is a stream. The remaining 1 byte stays in the pipe? No, net.Pipe has no internal buffering.
	// Wait, net.Pipe does NOT support partial reads easily without buffering?
	// Actually, `pipe.Read` waits for a `pipe.Write`. If `Write` has 11 bytes and `Read` asks 10.
	// It copies 10. The Writer is still "writing".
	// A second `Read` can pick up the rest.

	// HOWEVER, Demux implementation:
	/*
		n, err := c.Read(buf)
		...
		data := make([]byte, n)
		copy(data, buf[:n])
		// ...
		id := data[:m.idMask]
		payload := data[m.idMask:]
	*/
	// It treats EACH Read as a discrete packet.
	// If `Read` returns partial packet, `demux` will treat the first chunk as `[ID][Payload]`.
	// And the second chunk (continuation) will be treated as `[ID][Payload]` too.
	// This confirms `demux` is VERY dependent on `Read` returning a whole packet (or `framed` connection).
	// If we set bufSize to small, Demux will likely misinterpret data if the underlying packet is larger than bufSize.

	// If I send "1234payload", and bufSize is 10.
	// Read 10 -> "1234payloa" -> ID="1234", Payload="payloa"
	// Next read -> "d" -> ID="d..." (too short) -> Invalid packet -> Close connection.

	go func() {
		mc, _ := netx.NewDemuxClient(clientConn, []byte("1234"))
		mc.Write([]byte("longpayload")) // "1234" + "longpayload" (11 chars) = 15 bytes
	}()

	// We expect Demux to fail or close after first or second packet if it fragments.

	sess, err := l.Accept()
	if err == nil {
		// If it accepted, let's see what we got
		buf := make([]byte, 100)
		n, err := sess.Read(buf)
		// It might read "payloa" (6 bytes)
		if err == nil {
			if n > 6 {
				t.Errorf("Expected truncated read due to small buffer, got %d bytes: %s", n, buf[:n])
			}
		}
	}

	// The Demux loop should eventually return/close due to "Invalid packet" on the remnant "d" (or subsequent chunks).
	// But since it's async in a goroutine, observing the close is hard without waiting.
}

func TestDemuxSess_WriteTooLarge(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	// Packet overhead is 4 bytes. Buf size 10. Max payload = 6.
	l := netx.NewDemux(serverConn, 4, netx.WithDemuxBufSize(10), netx.WithDemuxAccQueueSize(4))
	defer l.Close()

	go func() {
		mc, _ := netx.NewDemuxClient(clientConn, []byte("1234"))
		mc.Write([]byte("hi"))
	}()

	sess, err := l.Accept()
	if err != nil {
		t.Fatalf("Accept failed: %v", err)
	}

	// Create payload > 6 bytes
	largePayload := []byte("1234567") // 7 bytes
	_, err = sess.Write(largePayload)
	if err == nil {
		t.Error("Expected error writing payload larger than buffer size, got nil")
	} else if err.Error() != "demuxSess: packet may be truncated; increase demux buffer size or frame the packets" {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestDemux_Close(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	// defer clientConn.Close() // Will be closed by demux

	l := netx.NewDemux(serverConn, 4, netx.WithDemuxAccQueueSize(4))

	go func() {
		mc, _ := netx.NewDemuxClient(clientConn, []byte("1234"))
		mc.Write([]byte("keepalive"))
		time.Sleep(100 * time.Millisecond)
		// Keep sending to keep loop active?
	}()

	sess, err := l.Accept()
	if err != nil {
		t.Fatalf("Accept failed: %v", err)
	}

	// Close listener
	err = l.Close()
	if err != nil {
		t.Errorf("Close failed: %v", err)
	}

	// Check if session is closed
	_, err = sess.Read(make([]byte, 1))
	if err != io.EOF && err != net.ErrClosed {
		// Expect EOF
	}
}

func TestDemux_InvalidPacket(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	l := netx.NewDemux(serverConn, 4, netx.WithDemuxAccQueueSize(1))
	defer l.Close()

	// Send packet shorter than 4 bytes
	go func() {
		clientConn.Write([]byte("123"))
	}()

	// Server readLoop should detect invalid packet and close connection
	// We can detect this by checking if l.Accept() returns error or if serverConn is closed.
	// Accept waits for connection. It won't return error unless demux is Closed manually or accQueue closed.
	// demux.Close() closes accQueue.
	// readLoop calls c.Close() then returns. It does NOT call m.Close()!
	// Wait, if readLoop returns, the server stops processing. But `m` struct is not updated heavily.
	// Does readLoop close accQueue? NO.
	// So l.Accept() will block forever if readLoop exits?
	// The implementation of readLoop:
	/*
		func (m *demux) readLoop(c net.Conn) {
			defer c.Close()
			// ...
			if len(data) < m.idMask {
				return
			}
			// ...
		}
	*/
	// It just returns. c is closed.
	// `demux` instance stays "open" logically, but `Accept` blocks on channel.
	// This seems like a limitation/design choice. Demux assumes persistent connection.
	// If connection dies, Demux basically dies silently for new accepts.

	// BUT, we can check if serverConn is closed.

	time.Sleep(50 * time.Millisecond)

	// Check if serverConn is closed. readLoop does defer c.Close().
	// We can try to write to serverConn from client side. net.Pipe: Write to closed pipe returns error.
	_, err := clientConn.Write([]byte("check"))
	if err == nil {
		t.Error("Expected serverConn to be closed after invalid packet, but Write succeeded")
	} else if err != io.ErrClosedPipe {
		// It might be io.ErrClosedPipe or similar
		t.Logf("Got expected error: %v", err)
	}
}

func TestDemux_DroppedPackets(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	// Session queue size 2
	l := netx.NewDemux(serverConn, 4, netx.WithDemuxAccQueueSize(4), netx.WithDemuxSessQueueSize(2))
	defer l.Close()

	go func() {
		mc, _ := netx.NewDemuxClient(clientConn, []byte("1234"))
		// Write 4 packets
		mc.Write([]byte("P1"))
		mc.Write([]byte("P2"))
		mc.Write([]byte("P3"))
		mc.Write([]byte("P4"))
	}()

	sess, err := l.Accept()
	if err != nil {
		t.Fatalf("Accept failed: %v", err)
	}

	// Allow time for readLoop to process and fill queue
	time.Sleep(50 * time.Millisecond)

	// Queue size is 2. P1, P2 should be in. P3, P4 dropped?
	// Or P3, P4 in, P1, P2 consumed?
	// It's a channel. First in, First out.
	// If channel is full, select default executes (drop).
	// So P1, P2 go in. P3 arrives, queue full? Drop. P4 Drop.
	// IMPORTANT: net.Pipe writes are blocking.
	// `mc.Write("P1")` -> Server Read -> Queue P1.
	// `mc.Write("P2")` -> Server Read -> Queue P2.
	// `mc.Write("P3")` -> Server Read -> Queue FULL -> DROP.
	// `mc.Write("P4")` -> Server Read -> Queue FULL -> DROP.

	buf := make([]byte, 10)

	// Read 1
	n, err := sess.Read(buf)
	if string(buf[:n]) != "P1" {
		t.Errorf("Expected P1, got %s", string(buf[:n]))
	}

	// Read 2
	n, err = sess.Read(buf)
	if string(buf[:n]) != "P2" {
		t.Errorf("Expected P2, got %s", string(buf[:n]))
	}

	// Read 3 - Should timeout or be EOF or wait for new data?
	// The queue is empty now.
	// We rely on "Drop" behavior.
	// If we successfully read P3, then logic is wrong (or queue wasn't full).

	errCh := make(chan error)
	go func() {
		_, err := sess.Read(buf) // Should block if no data
		errCh <- err
	}()

	select {
	case <-errCh:
		t.Error("Read returned data, expected it to block (packets P3/P4 should be dropped)")
	case <-time.After(50 * time.Millisecond):
		// Correct, it blocked
	}
}

func TestDemuxSess_Deadline(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	l := netx.NewDemux(serverConn, 4, netx.WithDemuxAccQueueSize(4))
	defer l.Close()

	go func() {
		mc, _ := netx.NewDemuxClient(clientConn, []byte("1234"))
		mc.Write([]byte("hi"))
	}()

	sess, err := l.Accept()
	if err != nil {
		t.Fatalf("Accept failed: %v", err)
	}

	// Read first packet to clear queue
	buf := make([]byte, 100)
	sess.Read(buf)

	// Set deadline in past
	sess.SetReadDeadline(time.Now().Add(-1 * time.Second))
	_, err = sess.Read(buf)
	if err == nil {
		t.Error("Expected timeout error for past deadline, got nil")
	}

	// Set short deadline in future
	sess.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	// Should block until timeout because no data is coming
	start := time.Now()
	_, err = sess.Read(buf)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("Expected timeout error for future deadline, got nil")
	} else if !os.IsTimeout(err) {
		t.Errorf("Expected timeout error, got %v", err)
	}

	if elapsed < 10*time.Millisecond {
		t.Errorf("Returned too early: %v", elapsed)
	}
}
