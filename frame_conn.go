/*
FrameConn is a network layer that adds a length-prefixed framing protocol inside a stream-oriented
connection (like TCP). This allows wrapping packet-based connections inside stream ones (e.g.
UDP over TCP+TLS), preserving message boundaries. Each frame consists of a 4-byte big-endian
length header followed by the payload.

Since FrameConn performs two writes per frame (one for the header and one for the payload),
it is highly recommended to wrap the underlying connection in a BufferedConn. This coalesces
the writes into a single system call, significantly improving performance.
*/

package netx

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
)

func init() {
	Register("frame", func(params map[string]string, listener bool) (Wrapper, error) {
		for key := range params {
			return Wrapper{}, fmt.Errorf("uri: unknown frame parameter %q", key)
		}
		connToConn := func(c net.Conn) (net.Conn, error) {
			return NewFrameConn(c), nil
		}
		return Wrapper{
			Name:   "frame",
			Params: params,
			ListenerToListener: func(l net.Listener) (net.Listener, error) {
				return ConnWrapListener(l, connToConn)
			},
			DialerToDialer: func(f Dialer) (Dialer, error) {
				return ConnWrapDialer(f, connToConn)
			},
			ConnToConn: connToConn,
		}, nil
	})
}

type frameConn struct {
	net.Conn
	pending  []byte
	buf      []byte
	rmu, wmu sync.Mutex
}

// NewFrameConn wraps a net.Conn with a simple length-prefixed framing protocol.
// Each frame is prefixed with a 4-byte big-endian unsigned integer indicating the length of the frame.
func NewFrameConn(c net.Conn) net.Conn {
	return &frameConn{
		Conn: c,
		buf:  make([]byte, MaxPacketSize),
	}
}

// Read returns at most one frame's bytes; large frames are delivered across multiple Reads.
func (c *frameConn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()

	if len(c.pending) > 0 {
		n := copy(p, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}

	var hdr [2]byte
	if _, err := io.ReadFull(c.Conn, hdr[:]); err != nil {
		return 0, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if len(p) >= n {
		_, err := io.ReadFull(c.Conn, p[:n])
		return n, err
	}

	if _, err := io.ReadFull(c.Conn, c.buf[:n]); err != nil {
		return 0, err
	}
	w := copy(p, c.buf[:n])
	c.pending = c.buf[w:n]
	return w, nil
}

// Write sends p as a single frame.
func (c *frameConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()

	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(p)))
	if _, err := c.Conn.Write(hdr[:]); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if _, err := c.Conn.Write(p); err != nil {
		return 0, err
	}
	// If the underlying layer is buffered and implements Flush, flush now to coalesce header+payload.
	if fw, ok := c.Conn.(BufConn); ok {
		if err := fw.Flush(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
