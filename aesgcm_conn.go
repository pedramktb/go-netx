package netx

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"time"
)

type aesgcmConn struct {
	net.Conn
	aead cipher.AEAD
	wiv  [12]byte
	riv  [12]byte

	// sequence number for nonce derivation, incremented atomically
	seq atomic.Uint64

	maxPacketSize int
}

type AESGCMOption func(*aesgcmConn)

// WithMaxPacket sets the maximum ciphertext packet size accepted on Read.
// Default is 32KB. This should be >= 8 (seq) + plaintext + aead.Overhead().
func WithAESGCMMaxPacket(size uint32) AESGCMOption {
	return func(c *aesgcmConn) {
		c.maxPacketSize = int(size)
	}
}

// NewAESGCMConn constructs a new AES-GCM wrapper around a packet-based net.Conn.
// Key must be 16, 24, or 32 bytes (AES-128/192/256).
// It encrypts each packet using AES-GCM. It assumes the underlying conn
// preserves packet boundaries; it does not perform additional framing.
//
// Packet layout (single datagram):
//
//	[8-byte seq big-endian][GCM(ciphertext||tag)]
//
// Nonce derivation: 12-byte IV is required. For a packet with sequence S,
// nonce = IV with its last 8 bytes XORed with S (big-endian). This ensures
// per-packet unique nonces without transmitting the full nonce.
// Write IV is randomly generated on creation and sent to the peer in the
// passive handshake that is performed on creation to exchange random IVs.
func NewAESGCMConn(c net.Conn, key []byte, opts ...AESGCMOption) (net.Conn, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	agc := &aesgcmConn{
		Conn:          c,
		aead:          a,
		maxPacketSize: 32 * 1024}
	for _, opt := range opts {
		opt(agc)
	}
	if agc.maxPacketSize < 8+a.Overhead() {
		return nil, errors.New("aesgcmConn: maxPacketSize too small")
	}
	if _, err := io.ReadFull(rand.Reader, agc.wiv[:]); err != nil {
		return nil, err
	}

	// Passive handshake (duplex): concurrently read peer IV while writing ours
	handshakeDeadline := time.Now().Add(5 * time.Second)
	_ = c.SetDeadline(handshakeDeadline)
	defer func() { _ = c.SetDeadline(time.Time{}) }() // clear deadline after handshake

	// Start read of peer's 12-byte IV
	readErrCh := make(chan error, 1)
	go func() {
		// io.ReadFull returns err if not enough bytes
		_, err := io.ReadFull(c, agc.riv[:])
		readErrCh <- err
	}()

	// Write our 12-byte IV
	o := 0
	for o < len(agc.wiv) {
		n, err := c.Write(agc.wiv[o:])
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

// Read reads and decrypts a single datagram from the underlying conn.
// If p is too small for the decrypted payload, io.ErrShortBuffer is returned.
func (c *aesgcmConn) Read(p []byte) (int, error) {
	buf := make([]byte, c.maxPacketSize)
	n, err := c.Conn.Read(buf)
	if err != nil {
		return 0, err
	}
	if n == c.maxPacketSize {
		return 0, errors.New("aesgcmConn: packet may be truncated; increase maxPacketSize")
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
	if len(p)+8+c.aead.Overhead() > c.maxPacketSize {
		return 0, errors.New("aesgcmConn: packet may be too large; increase maxPacketSize")
	}
	buf := make([]byte, c.maxPacketSize)

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

	// Satisfy io.Writer contract: on success, return len(p) bytes written.
	return len(p), nil
}
