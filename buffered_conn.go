package netx

import (
	"bufio"
	"errors"
	"net"
	"time"
)

type BufConn interface {
	net.Conn
	Flush() error
}

type bufConn struct {
	bc net.Conn
	br *bufio.Reader
	bw *bufio.Writer
}

type BufConnOption func(*bufConn)

func WithBufSize(size int) BufConnOption {
	return func(bc *bufConn) {
		bc.br = bufio.NewReaderSize(bc.bc, size)
		bc.bw = bufio.NewWriterSize(bc.bc, size)
	}
}

func WithBufWriterSize(size int) BufConnOption {
	return func(bc *bufConn) {
		bc.bw = bufio.NewWriterSize(bc.bc, size)
	}
}

func WithBufReaderSize(size int) BufConnOption {
	return func(bc *bufConn) {
		bc.br = bufio.NewReaderSize(bc.bc, size)
	}
}

// NewBufConn wraps a net.Conn with buffered reader and writer.
// By default, the buffer size is 4KB. Use WithBufWriterSize and WithBufReaderSize to customize the sizes.
func NewBufConn(c net.Conn, opts ...BufConnOption) BufConn {
	bc := &bufConn{
		bc: c,
		br: bufio.NewReader(c),
		bw: bufio.NewWriter(c),
	}
	for _, opt := range opts {
		opt(bc)
	}
	return bc
}

func (c *bufConn) Read(p []byte) (int, error)  { return c.br.Read(p) }
func (c *bufConn) Write(p []byte) (int, error) { return c.bw.Write(p) }
func (c *bufConn) Close() error {
	// Attempt to flush; collect both flush and close errors.
	// Even if flush fails, still attempt to close the underlying conn.
	var err error
	if c.bw != nil {
		if fErr := c.bw.Flush(); fErr != nil {
			err = errors.Join(err, fErr)
		}
	}
	if c.bc != nil {
		if cErr := c.bc.Close(); cErr != nil {
			err = errors.Join(err, cErr)
		}
	}
	return err
}
func (c *bufConn) LocalAddr() net.Addr                { return c.bc.LocalAddr() }
func (c *bufConn) RemoteAddr() net.Addr               { return c.bc.RemoteAddr() }
func (c *bufConn) SetDeadline(t time.Time) error      { return c.bc.SetDeadline(t) }
func (c *bufConn) SetReadDeadline(t time.Time) error  { return c.bc.SetReadDeadline(t) }
func (c *bufConn) SetWriteDeadline(t time.Time) error { return c.bc.SetWriteDeadline(t) }

func (c *bufConn) Flush() error { return c.bw.Flush() }
