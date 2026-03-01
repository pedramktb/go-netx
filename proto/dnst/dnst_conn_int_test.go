package netx

import (
	"bytes"
	"net"
	"testing"
	"time"

	netx "github.com/pedramktb/go-netx"
)

func TestDNST_TaggedDemux_SingleSession(t *testing.T) {
	p1, p2 := net.Pipe()

	domain := "example.com"
	idLen := uint8(4)

	serverTagged := NewServerConn(p1, domain)
	l, err := netx.NewTaggedDemux(serverTagged, idLen)
	if err != nil {
		t.Fatalf("Failed to create TaggedDemux: %v", err)
	}
	defer l.Close()

	clientDNST := NewClientConn(p2, domain)
	defer p2.Close()

	client, err := netx.NewDemuxClient(clientDNST, []byte("AAA1"))()
	if err != nil {
		t.Fatalf("NewDemuxClient failed: %v", err)
	}

	payload := []byte("ping")

	go func() {
		client.Write(payload)
	}()

	// Server accepts the demuxed session
	conn, err := l.Accept()
	if err != nil {
		t.Fatalf("Accept failed: %v", err)
	}
	defer conn.Close()

	// Read payload (session ID stripped by demux)
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if !bytes.Equal(buf[:n], payload) {
		t.Errorf("Expected payload %q, got %q", payload, buf[:n])
	}

	// Echo back through demux (re-prepends session ID, uses DNS tag from Read)
	response := []byte("pong")
	go func() {
		conn.Write(response)
	}()

	// Client reads response (DemuxClient strips session ID)
	respBuf := make([]byte, 1024)
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = client.Read(respBuf)
	if err != nil {
		t.Fatalf("Client read failed: %v", err)
	}

	if !bytes.Equal(respBuf[:n], response) {
		t.Errorf("Expected %q, got %q", response, respBuf[:n])
	}
}

func TestDNST_TaggedDemux_MultipleSessions(t *testing.T) {
	p1, p2 := net.Pipe()

	domain := "example.com"
	idLen := uint8(4)

	serverTagged := NewServerConn(p1, domain)
	l, err := netx.NewTaggedDemux(serverTagged, idLen, netx.WithDemuxAccQueue(4))
	if err != nil {
		t.Fatalf("Failed to create TaggedDemux: %v", err)
	}
	defer l.Close()

	clientDNST := NewClientConn(p2, domain)
	defer p2.Close()

	type session struct {
		id      []byte
		payload []byte
	}
	sessions := []session{
		{id: []byte("SID1"), payload: []byte("data1")},
		{id: []byte("SID2"), payload: []byte("data2")},
	}

	// Process each session as a complete request-response cycle
	// to avoid concurrent writes on the underlying pipe.
	for _, s := range sessions {
		client, err := netx.NewDemuxClient(clientDNST, s.id)()
		if err != nil {
			t.Fatalf("NewDemuxClient failed for %s: %v", s.id, err)
		}

		go func() {
			client.Write(s.payload)
		}()

		conn, err := l.Accept()
		if err != nil {
			t.Fatalf("Accept failed for %s: %v", s.id, err)
		}

		buf := make([]byte, 1024)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("Session %s read failed: %v", s.id, err)
		}
		if !bytes.Equal(buf[:n], s.payload) {
			t.Errorf("Session %s: expected %q, got %q", s.id, s.payload, buf[:n])
		}

		// Echo back
		go func() {
			conn.Write(buf[:n])
		}()

		client.SetReadDeadline(time.Now().Add(2 * time.Second))
		respBuf := make([]byte, 1024)
		n, err = client.Read(respBuf)
		if err != nil {
			t.Fatalf("Client read for %s failed: %v", s.id, err)
		}

		if !bytes.Equal(respBuf[:n], s.payload) {
			t.Errorf("Response for %s: expected %q, got %q", s.id, s.payload, respBuf[:n])
		}

		conn.Close()
	}
}

func TestDNST_TaggedDemux_MultipleMessages(t *testing.T) {
	p1, p2 := net.Pipe()

	domain := "example.com"
	idLen := uint8(4)

	serverTagged := NewServerConn(p1, domain)
	l, err := netx.NewTaggedDemux(serverTagged, idLen)
	if err != nil {
		t.Fatalf("Failed to create TaggedDemux: %v", err)
	}
	defer l.Close()

	clientDNST := NewClientConn(p2, domain)
	defer p2.Close()

	client, err := netx.NewDemuxClient(clientDNST, []byte("SESS"))()
	if err != nil {
		t.Fatalf("NewDemuxClient failed: %v", err)
	}

	messages := [][]byte{
		[]byte("msg1"),
		[]byte("msg2"),
		[]byte("msg3"),
	}

	// Server goroutine: accept one session and echo all messages
	errCh := make(chan error, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()

		for range messages {
			buf := make([]byte, 1024)
			n, err := conn.Read(buf)
			if err != nil {
				errCh <- err
				return
			}
			_, err = conn.Write(buf[:n])
			if err != nil {
				errCh <- err
				return
			}
		}
		close(errCh)
	}()

	// Client sends each message and reads the echoed response
	for i, msg := range messages {
		_, err := client.Write(msg)
		if err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}

		buf := make([]byte, 1024)
		client.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := client.Read(buf)
		if err != nil {
			t.Fatalf("Read %d failed: %v", i, err)
		}

		if !bytes.Equal(buf[:n], msg) {
			t.Errorf("Message %d: expected %q, got %q", i, msg, buf[:n])
		}
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Server error: %v", err)
		}
	case <-time.After(1 * time.Second):
	}
}

func TestDNST_TaggedDemux_Close(t *testing.T) {
	p1, p2 := net.Pipe()

	domain := "example.com"
	idLen := uint8(4)

	serverTagged := NewServerConn(p1, domain)
	l, err := netx.NewTaggedDemux(serverTagged, idLen)
	if err != nil {
		t.Fatalf("Failed to create TaggedDemux: %v", err)
	}

	// Create a session via DemuxClient
	clientDNST := NewClientConn(p2, domain)
	client, err := netx.NewDemuxClient(clientDNST, []byte("SID1"))()
	if err != nil {
		t.Fatalf("NewDemuxClient failed: %v", err)
	}

	go func() {
		client.Write([]byte("data"))
	}()

	conn, err := l.Accept()
	if err != nil {
		t.Fatalf("Accept failed: %v", err)
	}

	// Read to populate the tag queue
	buf := make([]byte, 1024)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("Session read failed: %v", err)
	}

	// Close the listener (closes underlying DNST conn and pipe)
	if err := l.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}

	// Session read should fail (rQueue closed)
	conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, err = conn.Read(buf)
	if err == nil {
		t.Error("Expected error reading from closed session")
	}

	// Session write should fail (underlying conn closed)
	_, err = conn.Write([]byte("foo"))
	if err == nil {
		t.Error("Expected error writing to closed session")
	}

	// Accept should fail (accQueue closed)
	_, err = l.Accept()
	if err == nil {
		t.Error("Expected error accepting on closed listener")
	}
}

func TestDNST_TaggedDemux_TCP(t *testing.T) {
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer tcpListener.Close()

	domain := "example.com"
	idLen := uint8(4)

	errCh := make(chan error, 1)

	// Server goroutine: accept TCP conn → DNST → TaggedDemux → echo
	go func() {
		tcpConn, err := tcpListener.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer tcpConn.Close()

		serverTagged := NewServerConn(tcpConn, domain)
		demux, _ := netx.NewTaggedDemux(serverTagged, idLen)
		defer demux.Close()

		sess, err := demux.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer sess.Close()

		buf := make([]byte, 1024)
		n, err := sess.Read(buf)
		if err != nil {
			errCh <- err
			return
		}

		_, err = sess.Write(buf[:n])
		if err != nil {
			errCh <- err
			return
		}
		close(errCh)
	}()

	// Client dials TCP and wraps in DNST + DemuxClient
	tcpConn, err := net.Dial("tcp", tcpListener.Addr().String())
	if err != nil {
		t.Fatalf("Failed to dial: %v", err)
	}
	defer tcpConn.Close()

	clientDNST := NewClientConn(tcpConn, domain)
	client, err := netx.NewDemuxClient(clientDNST, []byte("TST1"))()
	if err != nil {
		t.Fatalf("NewDemuxClient failed: %v", err)
	}

	payload := []byte("hello tcp")

	_, err = client.Write(payload)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	buf := make([]byte, 1024)
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if !bytes.Equal(buf[:n], payload) {
		t.Errorf("Expected %q, got %q", payload, buf[:n])
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Server error: %v", err)
		}
	case <-time.After(2 * time.Second):
	}
}

func TestDNST_TaggedDemux_PollConn_Echo(t *testing.T) {
	p1, p2 := net.Pipe()

	domain := "example.com"
	idLen := uint8(4)

	serverTagged := NewServerConn(p1, domain)
	l, err := netx.NewTaggedDemux(serverTagged, idLen, netx.WithDemuxAccQueue(1))
	if err != nil {
		t.Fatalf("Failed to create TaggedDemux: %v", err)
	}
	defer l.Close()

	// Server: accept session, echo all messages (including responding to polls)
	errCh := make(chan error, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()

		buf := make([]byte, 1024)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			_, err = conn.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}()

	// Client: DNST → DemuxClient → PollConn
	clientDNST := NewClientConn(p2, domain)
	demuxClient, err := netx.NewDemuxClient(clientDNST, []byte("SES1"))()
	if err != nil {
		t.Fatalf("NewDemuxClient failed: %v", err)
	}

	pc := netx.NewPollConn(demuxClient, netx.WithPollInterval(10*time.Millisecond))
	defer pc.Close()

	// Write and read multiple messages through the persistent PollConn
	messages := []string{"hello", "world", "test"}
	for i, msg := range messages {
		_, err := pc.Write([]byte(msg))
		if err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}

		buf := make([]byte, 1024)
		pc.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := pc.Read(buf)
		if err != nil {
			t.Fatalf("Read %d failed: %v", i, err)
		}
		if string(buf[:n]) != msg {
			t.Errorf("Message %d: expected %q, got %q", i, msg, buf[:n])
		}
	}
}

func TestDNST_TaggedDemux_PollConn_ServerInitiated(t *testing.T) {
	p1, p2 := net.Pipe()

	domain := "example.com"
	idLen := uint8(4)

	serverTagged := NewServerConn(p1, domain)
	l, err := netx.NewTaggedDemux(serverTagged, idLen, netx.WithDemuxAccQueue(1))
	if err != nil {
		t.Fatalf("Failed to create TaggedDemux: %v", err)
	}
	defer l.Close()

	welcome := []byte("welcome")

	// Server: accept session, send welcome on first request, then echo
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		buf := make([]byte, 1024)

		// First read (likely a poll with 0 bytes)
		_, err = conn.Read(buf)
		if err != nil {
			return
		}
		// Respond with server-initiated data
		conn.Write(welcome)

		// Then echo loop for subsequent messages
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			conn.Write(buf[:n])
		}
	}()

	// Client: DNST → DemuxClient → PollConn — don't write, just read
	clientDNST := NewClientConn(p2, domain)
	demuxClient, err := netx.NewDemuxClient(clientDNST, []byte("SES1"))()
	if err != nil {
		t.Fatalf("NewDemuxClient failed: %v", err)
	}

	pc := netx.NewPollConn(demuxClient, netx.WithPollInterval(10*time.Millisecond))
	defer pc.Close()

	// Read without writing — polling should deliver the server-initiated welcome message
	buf := make([]byte, 1024)
	pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := pc.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if !bytes.Equal(buf[:n], welcome) {
		t.Errorf("Expected %q, got %q", welcome, buf[:n])
	}

	// Now also verify a normal echo works after the welcome
	msg := []byte("ping")
	if _, err := pc.Write(msg); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = pc.Read(buf)
	if err != nil {
		t.Fatalf("Read echo failed: %v", err)
	}
	if !bytes.Equal(buf[:n], msg) {
		t.Errorf("Expected echo %q, got %q", msg, buf[:n])
	}
}

func TestDNST_Mux_TaggedDemux_Echo(t *testing.T) {
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer tcpListener.Close()

	domain := "example.com"
	idLen := uint8(4)

	// Server: Mux wraps the TCP listener into a TaggedConn
	// which is then passed to DNST → TaggedDemux → echo
	muxConn := netx.NewMux(tcpListener)
	serverTagged := NewTaggedServerConn(muxConn, domain)
	demux, err := netx.NewTaggedDemux(serverTagged, idLen, netx.WithDemuxAccQueue(1))
	if err != nil {
		t.Fatalf("Failed to create TaggedDemux: %v", err)
	}
	defer demux.Close()

	errCh := make(chan error, 1)
	go func() {
		sess, err := demux.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer sess.Close()

		buf := make([]byte, 1024)
		n, err := sess.Read(buf)
		if err != nil {
			errCh <- err
			return
		}
		_, err = sess.Write(buf[:n])
		if err != nil {
			errCh <- err
			return
		}
		close(errCh)
	}()

	// Client dials TCP → DNST → DemuxClient
	tcpConn, err := net.Dial("tcp", tcpListener.Addr().String())
	if err != nil {
		t.Fatalf("Failed to dial: %v", err)
	}
	defer tcpConn.Close()

	clientDNST := NewClientConn(tcpConn, domain)
	client, err := netx.NewDemuxClient(clientDNST, []byte("TST1"))()
	if err != nil {
		t.Fatalf("NewDemuxClient failed: %v", err)
	}

	payload := []byte("hello listenerconn")

	if _, err := client.Write(payload); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	buf := make([]byte, 1024)
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if !bytes.Equal(buf[:n], payload) {
		t.Errorf("Expected %q, got %q", payload, buf[:n])
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Server error: %v", err)
		}
	case <-time.After(2 * time.Second):
	}
}

func TestDNST_Mux_TaggedDemux_MultipleClients(t *testing.T) {
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer tcpListener.Close()

	domain := "example.com"
	idLen := uint8(4)
	addr := tcpListener.Addr().String()

	// Server: Mux → DNST → TaggedDemux
	// Each TCP connection from a different client is transparently absorbed by Mux.
	muxConn := netx.NewMux(tcpListener)
	serverTagged := NewTaggedServerConn(muxConn, domain)
	demux, err := netx.NewTaggedDemux(serverTagged, idLen, netx.WithDemuxAccQueue(4))
	if err != nil {
		t.Fatalf("Failed to create TaggedDemux: %v", err)
	}
	defer demux.Close()

	// Server echo loop: accept sessions and echo payloads
	go func() {
		for {
			sess, err := demux.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					if _, err := c.Write(buf[:n]); err != nil {
						return
					}
				}
			}(sess)
		}
	}()

	// Multiple clients dial separate TCP connections sequentially.
	// Each one sends a request-response through the full stack.
	clients := []struct {
		sessID  []byte
		payload []byte
	}{
		{[]byte("CL01"), []byte("first")},
		{[]byte("CL02"), []byte("second")},
		{[]byte("CL03"), []byte("third")},
	}

	for _, c := range clients {
		tcpConn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("Dial for %s failed: %v", c.sessID, err)
		}

		clientDNST := NewClientConn(tcpConn, domain)
		mc, err := netx.NewDemuxClient(clientDNST, c.sessID)()
		if err != nil {
			tcpConn.Close()
			t.Fatalf("NewDemuxClient for %s failed: %v", c.sessID, err)
		}

		if _, err := mc.Write(c.payload); err != nil {
			tcpConn.Close()
			t.Fatalf("Write for %s failed: %v", c.sessID, err)
		}

		buf := make([]byte, 1024)
		mc.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := mc.Read(buf)
		if err != nil {
			tcpConn.Close()
			t.Fatalf("Read for %s failed: %v", c.sessID, err)
		}
		if !bytes.Equal(buf[:n], c.payload) {
			t.Errorf("Client %s: expected %q, got %q", c.sessID, c.payload, buf[:n])
		}

		tcpConn.Close()
	}
}

func TestDNST_Mux_TaggedDemux_PollConn(t *testing.T) {
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer tcpListener.Close()

	domain := "example.com"
	idLen := uint8(4)

	// Server: Mux → DNST → TaggedDemux → echo
	muxConn := netx.NewMux(tcpListener)
	serverTagged := NewTaggedServerConn(muxConn, domain)
	demux, err := netx.NewTaggedDemux(serverTagged, idLen, netx.WithDemuxAccQueue(1))
	if err != nil {
		t.Fatalf("Failed to create TaggedDemux: %v", err)
	}
	defer demux.Close()

	go func() {
		conn, err := demux.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			if _, err := conn.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	// Client: TCP → DNST → DemuxClient → PollConn (full stack)
	tcpConn, err := net.Dial("tcp", tcpListener.Addr().String())
	if err != nil {
		t.Fatalf("Failed to dial: %v", err)
	}
	defer tcpConn.Close()

	clientDNST := NewClientConn(tcpConn, domain)
	mc, err := netx.NewDemuxClient(clientDNST, []byte("SES1"))()
	if err != nil {
		t.Fatalf("NewDemuxClient failed: %v", err)
	}

	pc := netx.NewPollConn(mc, netx.WithPollInterval(10*time.Millisecond))
	defer pc.Close()

	messages := []string{"alpha", "bravo", "charlie"}
	for i, msg := range messages {
		if _, err := pc.Write([]byte(msg)); err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
		buf := make([]byte, 1024)
		pc.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := pc.Read(buf)
		if err != nil {
			t.Fatalf("Read %d failed: %v", i, err)
		}
		if string(buf[:n]) != msg {
			t.Errorf("Message %d: expected %q, got %q", i, msg, buf[:n])
		}
	}
}
