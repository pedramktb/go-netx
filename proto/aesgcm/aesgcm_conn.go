/*
AESGCMConn is a network layer that provides authenticated encryption using AES-GCM.
Key must be 16, 24, or 32 bytes (AES-128/192/256).
It assumes the underlying conn preserves packet boundaries; it does not perform additional framing.
Packet layout (single datagram):

	[8-byte seq big-endian][GCM(ciphertext||tag)]

Nonce derivation: 12-byte IV is required. For a packet with sequence S,
nonce = IV with its last 8 bytes XORed with S (big-endian). This ensures
per-packet unique nonces without transmitting the full nonce.
Write IV is randomly generated on creation and sent to the peer in the
passive handshake that is performed on creation to exchange random IVs.
*/

package aesgcmproto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pedramktb/go-netx"
)

type aesgcmConn struct {
	net.Conn
	aead cipher.AEAD
	wiv  [12]byte
	riv  [12]byte
	// sequence number for nonce derivation, incremented atomically
	seq      atomic.Uint64
	buf      sync.Pool
	maxWrite uint16
}

// NewAESGCMConn creates a new AESGCMConn wrapping the provided net.Conn with the given key.
func NewAESGCMConn(conn net.Conn, key []byte) (net.Conn, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	agc := &aesgcmConn{
		Conn: conn,
		aead: a,
		buf: sync.Pool{
			New: func() any {
				b := make([]byte, netx.MaxPacketSize)
				return &b
			},
		},
	}
	if mw, ok := conn.(interface{ MaxWrite() uint16 }); ok && mw.MaxWrite() != 0 {
		if mw.MaxWrite() <= uint16(8+a.Overhead()) {
			return nil, errors.New("demux: underlying connection's MaxWrite is too small")
		}
		agc.maxWrite = mw.MaxWrite() - uint16(8+a.Overhead())
	}
	if _, err := io.ReadFull(rand.Reader, agc.wiv[:]); err != nil {
		return nil, err
	}

	// Passive handshake (duplex): concurrently read peer IV while writing ours
	handshakeDeadline := time.Now().Add(5 * time.Second)
	_ = conn.SetDeadline(handshakeDeadline)
	defer func() { _ = conn.SetDeadline(time.Time{}) }() // clear deadline after handshake

	// Start read of peer's 12-byte IV
	readErrCh := make(chan error, 1)
	go func() {
		// io.ReadFull returns err if not enough bytes
		_, err := io.ReadFull(conn, agc.riv[:])
		readErrCh <- err
	}()

	// Write our 12-byte IV
	o := 0
	for o < len(agc.wiv) {
		n, err := conn.Write(agc.wiv[o:])
		if err != nil {
			return nil, err
		}
		o += n
	}
	if o != len(agc.wiv) {
		return nil, io.ErrShortWrite
	}

	// Wait for read to complete
	if err := <-readErrCh; err != nil {
		return nil, err
	}

	return agc, nil
}

func (c *aesgcmConn) MaxWrite() uint16 {
	return c.maxWrite
}

// Read reads and decrypts a single datagram from the underlying conn.
// If p is too small for the decrypted payload, io.ErrShortBuffer is returned.
func (c *aesgcmConn) Read(p []byte) (int, error) {
	bp := c.buf.Get().(*[]byte)
	buf := *bp
	defer c.buf.Put(bp)

	n, err := c.Conn.Read(buf)
	if err != nil {
		return 0, err
	}
	if n == netx.MaxPacketSize {
		return 0, errors.New("aesgcmConn: packet too large")
	}
	if n < 8+c.aead.Overhead() {
		return 0, errors.New("aesgcmConn: packet too small")
	}

	nonce := [12]byte{}
	copy(nonce[:], c.riv[:])
	for i := range 8 {
		nonce[4+i] ^= buf[i]
	}

	buf, err = c.aead.Open(buf[8:8], nonce[:], buf[8:n], buf[:8])
	if err != nil {
		return 0, err
	}

	if len(buf) > len(p) {
		return 0, io.ErrShortBuffer
	}

	copy(p, buf)
	return len(buf), nil
}

// Write encrypts p as a single datagram and writes it to the underlying conn.
// It prepends an 8-byte sequence number used for nonce derivation.
func (c *aesgcmConn) Write(p []byte) (int, error) {
	if len(p)+8+c.aead.Overhead() > netx.MaxPacketSize {
		return 0, errors.New("aesgcmConn: packet too large")
	}
	bp := c.buf.Get().(*[]byte)
	buf := *bp
	defer c.buf.Put(bp)

	seq := c.seq.Add(1) - 1
	binary.BigEndian.PutUint64(buf[:8], seq)

	nonce := [12]byte{}
	copy(nonce[:], c.wiv[:])
	for i := range 8 {
		nonce[4+i] ^= buf[i]
	}

	ct := c.aead.Seal(buf[8:8], nonce[:], p, buf[:8])
	buf = buf[:8+len(ct)]

	n, err := c.Conn.Write(buf)
	if err != nil {
		return 0, err
	}
	if n != len(buf) {
		return 0, io.ErrShortWrite
	}

	return len(p), nil
}
