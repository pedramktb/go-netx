package netx_test

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"

	netx "github.com/pedramktb/go-netx"
)

func TestTaggedDemux_Basic(t *testing.T) {
	clientConn, serverConn := netx.TaggedPipe()
	defer clientConn.Close()
	defer serverConn.Close()

	expectedReadTag := "tag-1"

	// Create TaggedDemux
	idLen := 4
	l := netx.NewTaggedDemux(serverConn, idLen)
	defer l.Close()

	// Simulate client sending data
	sessID := []byte("1001")
	payload := []byte("Hello Tagged Demux")
	packet := append(sessID, payload...)

	go func() {
		// Send with a specific tag
		clientConn.WriteTagged(packet, expectedReadTag)
	}()

	// Accept session
	conn, err := l.Accept()
	if err != nil {
		t.Fatalf("Accept failed: %v", err)
	}
	defer conn.Close()

	// Read data
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if !bytes.Equal(buf[:n], payload) {
		t.Errorf("Expected payload %q, got %q", payload, buf[:n])
	}

	// Write response
	// The write on the session should use the tag captured during Read
	response := []byte("Response")
	go func() {
		_, err := conn.Write(response)
		if err != nil {
			t.Errorf("Write failed: %v", err)
		}
	}()

	// Verify response on client side
	respBuf := make([]byte, 1024)
	var respTag any
	n, err = clientConn.ReadTagged(respBuf, &respTag)
	if err != nil {
		t.Fatalf("Client Read failed: %v", err)
	}

	expectedPacket := append(sessID, response...)
	if !bytes.Equal(respBuf[:n], expectedPacket) {
		t.Errorf("Expected response packet %q, got %q", expectedPacket, respBuf[:n])
	}

	if respTag != expectedReadTag {
		t.Errorf("Expected response tag %v, got %v", expectedReadTag, respTag)
	}
}

func TestTaggedDemux_MultipleSessions(t *testing.T) {
	clientConn, serverConn := netx.TaggedPipe()
	defer clientConn.Close()
	defer serverConn.Close()

	l := netx.NewTaggedDemux(serverConn, 4) // idLen = 4
	defer l.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	// Session 1
	id1 := []byte("SID1")
	payload1 := []byte("Data1")
	go func() {
		defer wg.Done()
		time.Sleep(10 * time.Millisecond) // partial ordering
		clientConn.WriteTagged(append(id1, payload1...), nil)
	}()

	// Session 2
	id2 := []byte("SID2")
	payload2 := []byte("Data2")
	go func() {
		defer wg.Done()
		time.Sleep(20 * time.Millisecond)
		clientConn.WriteTagged(append(id2, payload2...), nil)
	}()

	// Accept sessions
	// We don't know which one comes first if we didn't sleep, but here we expect SID1 then SID2 roughly

	accepted := 0
	for accepted < 2 {
		conn, err := l.Accept()
		if err != nil {
			t.Fatalf("Accept failed: %v", err)
		}

		go func(c net.Conn) {
			defer c.Close()
			buf := make([]byte, 1024)
			n, err := c.Read(buf)
			if err != nil {
				t.Errorf("Read failed: %v", err)
				return
			}
			msg := buf[:n]

			var myID []byte
			if bytes.Equal(msg, payload1) {
				myID = id1
			} else if bytes.Equal(msg, payload2) {
				myID = id2
			} else {
				t.Errorf("Unknown payload: %q", msg)
				return
			}

			// Echo back
			c.Write(msg)
			_ = myID
		}(conn)
		accepted++
	}

	wg.Wait()

	// Read responses from clientConn
	// We expect 2 responses: SID1+Data1, SID2+Data2 (in any order)

	respBuf := make([]byte, 1024)
	totalRead := 0
	expectedTotal := len(id1) + len(payload1) + len(id2) + len(payload2)

	received := make([]byte, 0, expectedTotal)

	// Set a deadline for safety
	clientConn.SetReadDeadline(time.Now().Add(1 * time.Second))

	for totalRead < expectedTotal {
		var tag any
		n, err := clientConn.ReadTagged(respBuf, &tag)
		if err != nil {
			break
		}
		received = append(received, respBuf[:n]...)
		totalRead += n
	}

	if !bytes.Contains(received, append(id1, payload1...)) {
		t.Errorf("Missing response for SID1")
	}
	if !bytes.Contains(received, append(id2, payload2...)) {
		t.Errorf("Missing response for SID2")
	}
}

func TestTaggedDemux_Close(t *testing.T) {
	clientConn, serverConn := netx.TaggedPipe()
	defer clientConn.Close()
	// serverConn closed by Demux

	l := netx.NewTaggedDemux(serverConn, 4)

	// Create a session
	go func() {
		clientConn.WriteTagged([]byte("ID01Payload"), nil)
	}()

	conn, err := l.Accept()
	if err != nil {
		t.Fatalf("Accept failed: %v", err)
	}

	// Start a read which should block or read the payload
	// We read to ensure session is active
	buf := make([]byte, 1024)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("Session Read failed: %v", err)
	}

	// Close listener
	err = l.Close()
	if err != nil {
		t.Errorf("Close failed: %v", err)
	}

	// Verify session read returns error (EOF or Closed)
	conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, err = conn.Read(buf)
	if err == nil {
		t.Error("Expected error reading from closed session")
	}

	// Verify session write returns error
	_, err = conn.Write([]byte("foo"))
	if err == nil {
		t.Error("Expected error writing to closed session")
	}

	// Accept should fail
	_, err = l.Accept()
	if err == nil {
		t.Error("Expected error accepting on closed listener")
	}
}
