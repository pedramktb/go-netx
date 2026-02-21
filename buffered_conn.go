/*
BufferedConn is a network layer that buffers reads and writes, significantly reducing
the number of syscalls for small IO operations. It wraps a net.Conn with a bufio.Reader
and bufio.Writer.

This is particularly useful when used with FramedConn, which performs multiple writes
(header + payload) per frame.
*/

package netx

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
)

func init() {
	Register("buffered", func(params map[string]string, listener bool) (Wrapper, error) {
		opts := []BufConnOption{}
		for key, value := range params {
			switch key {
			case "size":
				size, err := strconv.ParseUint(value, 10, 31)
				if err != nil {
					return Wrapper{}, fmt.Errorf("uri: invalid buffered size parameter %q: %w", value, err)
				}
				opts = append(opts, WithBufSize(uint32(size)))
			default:
				return Wrapper{}, fmt.Errorf("uri: unknown buffered parameter %q", key)
			}
		}
		connToConn := func(c net.Conn) (net.Conn, error) {
			return NewBufConn(c, opts...), nil
		}
		return Wrapper{
			Name:   "buffered",
			Params: params,
			ListenerToListener: func(l net.Listener) (net.Listener, error) {
				return ConnWrapListener(l, connToConn)
			},
			DialerToDialer: func(f func() (net.Conn, error)) (func() (net.Conn, error), error) {
				return ConnWrapDialer(f, connToConn)
			},
			ConnToConn: connToConn,
		}, nil
	})
}

type BufConn interface {
	net.Conn
	Flush() error
}

type bufConn struct {
	net.Conn
	br *bufio.Reader
	bw *bufio.Writer
}

type BufConnOption func(*bufConn)

func WithBufSize(size uint32) BufConnOption {
	return func(bc *bufConn) {
		bc.br = bufio.NewReaderSize(bc.Conn, int(size))
		bc.bw = bufio.NewWriterSize(bc.Conn, int(size))
	}
}

func WithBufWriterSize(size uint32) BufConnOption {
	return func(bc *bufConn) {
		bc.bw = bufio.NewWriterSize(bc.Conn, int(size))
	}
}

func WithBufReaderSize(size uint32) BufConnOption {
	return func(bc *bufConn) {
		bc.br = bufio.NewReaderSize(bc.Conn, int(size))
	}
}

// NewBufConn wraps a net.Conn with buffered reader and writer.
// By default, the buffer size is 4KB. Use WithBufWriterSize and WithBufReaderSize to customize the sizes.
func NewBufConn(c net.Conn, opts ...BufConnOption) BufConn {
	bc := &bufConn{
		Conn: c,
		br:   bufio.NewReader(c),
		bw:   bufio.NewWriter(c),
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
	if c.Conn != nil {
		if cErr := c.Conn.Close(); cErr != nil {
			err = errors.Join(err, cErr)
		}
	}
	return err
}

func (c *bufConn) Flush() error { return c.bw.Flush() }
