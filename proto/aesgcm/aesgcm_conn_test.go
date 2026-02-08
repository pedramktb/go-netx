package aesgcmproto_test

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/pedramktb/go-netx"
	aesgcmproto "github.com/pedramktb/go-netx/proto/aesgcm"
)

// helper to create an AES-GCM protected pair over a framed connection
func newAESPair(t *testing.T) (client net.Conn, server net.Conn) {
	t.Helper()
	cr, sr := net.Pipe()
	t.Cleanup(func() { _ = cr.Close(); _ = sr.Close() })

	fc := netx.NewFramedConn(cr)
	fs := netx.NewFramedConn(sr)

	key := bytes.Repeat([]byte{0x42}, 32)

	var (
		c    net.Conn
		s    net.Conn
		ec   error
		es   error
		done = make(chan struct{}, 2)
	)
	go func() { c, ec = aesgcmproto.NewAESGCMConn(fc, key); done <- struct{}{} }()
	go func() { s, es = aesgcmproto.NewAESGCMConn(fs, key); done <- struct{}{} }()
	<-done
	<-done
	if ec != nil {
		t.Fatalf("client aesgcm: %v", ec)
	}
	if es != nil {
		t.Fatalf("server aesgcm: %v", es)
	}
	return c, s
}

func TestAESGCM_Roundtrip(t *testing.T) {
	c, s := newAESPair(t)

	msg := []byte("hello secret world")

	got := make([]byte, len(msg))
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(s, got)
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	if _, err := c.Write(msg); err != nil {
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

	// multiple sequential messages should also work
	data2 := bytes.Repeat([]byte("x"), 1024)
	go func() { _, _ = c.Write(data2) }()
	buf := make([]byte, len(data2))
	if _, err := io.ReadFull(s, buf); err != nil {
		t.Fatalf("readfull2: %v", err)
	}
	if !bytes.Equal(buf, data2) {
		t.Fatalf("mismatch2")
	}
}

func TestAESGCM_EmptyPayload(t *testing.T) {
	c, s := newAESPair(t)
	// write an empty datagram concurrently to avoid net.Pipe blocking
	doneW := make(chan error, 1)
	go func() {
		_, err := c.Write(nil)
		doneW <- err
	}()
	// should deliver a zero-length read (keep-alive style)
	buf := make([]byte, 8)
	n, err := s.Read(buf)
	if err != nil {
		t.Fatalf("read empty: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected zero-length read, got %d", n)
	}
	select {
	case err := <-doneW:
		if err != nil {
			t.Fatalf("write empty err: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("write timeout")
	}
}

func TestAESGCM_ShortBufferDropsPacket(t *testing.T) {
	c, s := newAESPair(t)

	first := bytes.Repeat([]byte("a"), 128)
	second := []byte("ok")

	// send two packets back-to-back
	go func() { _, _ = c.Write(first); _, _ = c.Write(second) }()

	// too-small buffer for the first packet should return io.ErrShortBuffer
	small := make([]byte, 10)
	if n, err := s.Read(small); err != io.ErrShortBuffer || n != 0 {
		t.Fatalf("want io.ErrShortBuffer, got n=%d err=%v", n, err)
	}

	// the next read should yield the second packet
	buf := make([]byte, 16)
	n, err := s.Read(buf)
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if n != len(second) || !bytes.Equal(buf[:n], second) {
		t.Fatalf("unexpected second packet: %q", buf[:n])
	}
}

func TestAESGCM_MaxPacketWrite(t *testing.T) {
	// choose a small max packet size so writes exceed it
	// Overhead is 8 (seq) + 16 (GCM) + len(plaintext)
	// set max to 48; max plaintext allowed ~= 24
	cr, sr := net.Pipe()
	t.Cleanup(func() { _ = cr.Close(); _ = sr.Close() })
	fc := netx.NewFramedConn(cr)
	fs := netx.NewFramedConn(sr)
	key := bytes.Repeat([]byte{0x42}, 32)
	var (
		c    net.Conn
		ec   error
		es   error
		done = make(chan struct{}, 2)
	)
	go func() {
		c, ec = aesgcmproto.NewAESGCMConn(fc, key, aesgcmproto.WithAESGCMMaxPacket(48))
		done <- struct{}{}
	}()
	go func() {
		_, es = aesgcmproto.NewAESGCMConn(fs, key, aesgcmproto.WithAESGCMMaxPacket(48))
		done <- struct{}{}
	}()
	<-done
	<-done
	if ec != nil {
		t.Fatalf("client: %v", ec)
	}
	if es != nil {
		t.Fatalf("server: %v", es)
	}

	big := bytes.Repeat([]byte("b"), 64)
	if _, err := c.Write(big); err == nil {
		t.Fatalf("expected write error due to max packet size")
	}
}

func TestAESGCM_DecryptErrorWrongKey(t *testing.T) {
	cr, sr := net.Pipe()
	t.Cleanup(func() { _ = cr.Close(); _ = sr.Close() })
	fc := netx.NewFramedConn(cr)
	fs := netx.NewFramedConn(sr)

	keyA := bytes.Repeat([]byte{0x11}, 32)
	keyB := bytes.Repeat([]byte{0x22}, 32)

	var (
		c    net.Conn
		s    net.Conn
		ec   error
		es   error
		done = make(chan struct{}, 2)
	)
	go func() { c, ec = aesgcmproto.NewAESGCMConn(fc, keyA); done <- struct{}{} }()
	go func() { s, es = aesgcmproto.NewAESGCMConn(fs, keyB); done <- struct{}{} }()
	<-done
	<-done
	if ec != nil {
		t.Fatalf("client: %v", ec)
	}
	if es != nil {
		t.Fatalf("server: %v", es)
	}

	// write a packet and expect read to fail
	writeDone := make(chan error, 1)
	go func() {
		_, err := c.Write([]byte("test"))
		writeDone <- err
	}()

	buf := make([]byte, 16)
	if _, err := s.Read(buf); err == nil {
		t.Fatalf("expected decrypt error")
	}
	<-writeDone
}
