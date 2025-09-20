package netx_test

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	netx "github.com/pedramktb/go-netx"
)

func TestBufConnReadWrite(t *testing.T) {
	clientRaw, serverRaw := net.Pipe()
	t.Cleanup(func() { _ = clientRaw.Close(); _ = serverRaw.Close() })

	c := netx.NewBufConn(clientRaw)
	s := netx.NewBufConn(serverRaw)

	// write from client to server
	msg := []byte("hello buffered world")
	if _, err := c.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Data should be buffered until Flush.
	// We can't access internal buffers from an external package; ensure that
	// before Flush, a concurrent reader doesn't receive data, and after Flush it does.
	got := make([]byte, len(msg))
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(s, got)
		done <- err
	}()
	// Give the goroutine a moment; it should not complete until flush.
	select {
	case <-done:
		t.Fatalf("read finished before flush; expected buffering")
	case <-time.After(50 * time.Millisecond):
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("readfull: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for read")
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("mismatch: got %q want %q", got, msg)
	}
}

func TestBufConnCustomSizes(t *testing.T) {
	clientRaw, serverRaw := net.Pipe()
	t.Cleanup(func() { _ = clientRaw.Close(); _ = serverRaw.Close() })

	c := netx.NewBufConn(clientRaw, netx.WithBufReaderSize(128), netx.WithBufWriterSize(256))
	s := netx.NewBufConn(serverRaw, netx.WithBufReaderSize(128), netx.WithBufWriterSize(256))

	// roundtrip
	data := bytes.Repeat([]byte("a"), 1024)
	go func() { _, _ = c.Write(data); _ = c.Flush() }()
	got := make([]byte, len(data))
	if _, err := io.ReadFull(s, got); err != nil {
		t.Fatalf("readfull: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("mismatch")
	}
}
