package netx

import (
	"net"
	"sync"
)

// CtxConn is a net.Conn extension that allows passing opaque context (metadata) along with packets.
// This is useful for protocols where the write path requires context from the read path (e.g. associating a response with a specific request in a tunnel),
// or when metadata needs to traverse through layers (e.g. Transport <-> Encryption <-> Demux).
type CtxConn interface {
	net.Conn
	// ReadCtx reads identifying context along with data.
	ReadCtx(b []byte) (n int, ctx any, err error)
	// WriteCtx writes data along with identifying context.
	WriteCtx(b []byte, ctx any) (n int, err error)
}

// CtxData is a simple struct to hold data and its associated context together.
// Useful for channels that require passing both in asynchronous scenarios.
type CtxData struct {
	Data []byte
	Ctx  any
}

// WrapCtxConn wraps a CtxConn with a net.Conn wrapper (e.g. for encryption layers before de-multiplexing) while preserving the context passing functionality.
// This wrapper is NOT compatible with stream-oriented protocols that require multiple reads/writes to form a complete message,
// or those that perform a handshake that requires multiple back-and-forth messages before the connection is fully established,
// as the context is associated with individual read/write calls. It is designed for packet-oriented protocols where each read/write corresponds to a complete message.
// You can, however, after using encyption layers that preserve packet boundaries, and de-multiplexing, use stream-oriented protocols on top of the resulting net.Conn.
func WrapCtxConn(c CtxConn, w Wrapper) (CtxConn, error) {
	rCtx := make(chan any, 1)
	wCtx := make(chan any, 1)
	wrapped, err := w(&innerCtxConn{
		CtxConn: c,
		rCtx:    rCtx,
		wCtx:    wCtx,
	})
	if err != nil {
		return nil, err
	}
	return &outerCtxConn{
		Conn: wrapped,
		rCtx: rCtx,
		wCtx: wCtx,
	}, nil
}

type ctxState struct {
	mu       sync.Mutex
	writeCtx any
	readCtx  any
}

type innerCtxConn struct {
	CtxConn
	rCtx chan<- any
	wCtx <-chan any
}

func (c *innerCtxConn) Read(b []byte) (n int, err error) {
	n, ctx, err := c.ReadCtx(b)
	c.rCtx <- ctx
	return n, err
}

func (c *innerCtxConn) Write(b []byte) (n int, err error) {
	ctx := <-c.wCtx
	return c.WriteCtx(b, ctx)
}

type outerCtxConn struct {
	net.Conn
	rCtx <-chan any
	wCtx chan any
}

func (c *outerCtxConn) ReadCtx(b []byte) (n int, ctx any, err error) {
	select {
	case <-c.rCtx:
	default:
	}
	n, err = c.Conn.Read(b)
	ctx = <-c.rCtx
	return n, ctx, err
}

func (c *outerCtxConn) WriteCtx(b []byte, ctx any) (n int, err error) {
	select {
	case <-c.wCtx:
	default:
	}
	c.wCtx <- ctx
	return c.Conn.Write(b)
}
