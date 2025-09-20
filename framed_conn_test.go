package netx_test

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	netx "github.com/pedramktb/go-netx"
)

// helper to write a frame to a raw conn
func writeFrame(t *testing.T, c net.Conn, payload []byte) {
	t.Helper()
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := c.Write(hdr[:]); err != nil {
		t.Fatalf("write hdr: %v", err)
	}
	if len(payload) > 0 {
		if _, err := c.Write(payload); err != nil {
			t.Fatalf("write body: %v", err)
		}
	}
}

func TestFramedConnSimple(t *testing.T) {
	clientRaw, serverRaw := net.Pipe()
	t.Cleanup(func() { _ = clientRaw.Close(); _ = serverRaw.Close() })

	fcClient := netx.NewFramedConn(clientRaw)
	fcServer := netx.NewFramedConn(serverRaw)

	// send one frame
	msg := []byte("hello frame")
	got := make([]byte, len(msg))
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(fcServer, got)
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	if _, err := fcClient.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("readfull: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout")
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("mismatch")
	}
}

func TestFramedConnPartialRead(t *testing.T) {
	clientRaw, serverRaw := net.Pipe()
	t.Cleanup(func() { _ = clientRaw.Close(); _ = serverRaw.Close() })

	fcClient := netx.NewFramedConn(clientRaw)
	fcServer := netx.NewFramedConn(serverRaw)

	data := bytes.Repeat([]byte("x"), 1024)
	// Start reader first to avoid pipe deadlock
	first := make([]byte, 100)
	type res struct {
		n   int
		err error
	}
	done1 := make(chan res, 1)
	go func() {
		n1, err := fcServer.Read(first)
		done1 <- res{n: n1, err: err}
	}()
	time.Sleep(10 * time.Millisecond)
	if _, err := fcClient.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case r := <-done1:
		if r.err != nil || r.n != 100 {
			t.Fatalf("read1 n=%d err=%v", r.n, r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout first read")
	}

	// Now read the remainder
	rest := make([]byte, len(data)-len(first))
	n2, err := io.ReadFull(fcServer, rest)
	if err != nil {
		t.Fatalf("read2: %v", err)
	}
	if n2 != len(rest) {
		t.Fatalf("n2=%d", n2)
	}
}

func TestFramedConnDeliversEmptyFrames(t *testing.T) {
	clientRaw, serverRaw := net.Pipe()
	t.Cleanup(func() { _ = clientRaw.Close(); _ = serverRaw.Close() })

	fcServer := netx.NewFramedConn(serverRaw)

	// write two empty frames and then a payload on the client side using raw writer
	payload := []byte("data")
	doneWrite := make(chan struct{})
	go func() {
		writeFrame(t, clientRaw, nil)
		writeFrame(t, clientRaw, []byte{})
		writeFrame(t, clientRaw, payload)
		close(doneWrite)
	}()

	// First empty frame should yield 0, nil
	buf := make([]byte, 8)
	n, err := fcServer.Read(buf)
	if err != nil || n != 0 {
		t.Fatalf("empty1 n=%d err=%v", n, err)
	}
	// Second empty frame should also yield 0, nil
	n, err = fcServer.Read(buf)
	if err != nil || n != 0 {
		t.Fatalf("empty2 n=%d err=%v", n, err)
	}
	// Then the payload
	n, err = fcServer.Read(buf)
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if n != len(payload) || !bytes.Equal(buf[:n], payload) {
		t.Fatalf("unexpected payload: %q", buf[:n])
	}
	select {
	case <-doneWrite:
	case <-time.After(2 * time.Second):
		t.Fatalf("writer blocked")
	}
}

func TestFramedConnMaxSize(t *testing.T) {
	clientRaw, serverRaw := net.Pipe()
	t.Cleanup(func() { _ = clientRaw.Close(); _ = serverRaw.Close() })

	// Set small max frame size
	fcServer := netx.NewFramedConn(serverRaw, netx.WithMaxFrameSize(32))

	// Start a read and only send an oversized header so the reader errors
	buf := make([]byte, 10)
	errCh := make(chan error, 1)
	go func() {
		_, err := fcServer.Read(buf)
		errCh <- err
	}()
	time.Sleep(10 * time.Millisecond)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(64)) // larger than max 32
	if _, err := clientRaw.Write(hdr[:]); err != nil {
		t.Fatalf("write hdr: %v", err)
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("expected ErrFrameTooLarge")
		}
		if err != netx.ErrFrameTooLarge {
			t.Fatalf("got err=%v want ErrFrameTooLarge", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for error")
	}
}
