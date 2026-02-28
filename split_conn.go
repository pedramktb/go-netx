/*
SplitConn removes the MaxWrite limitation of an underlying net.Conn by splitting
large writes into multiple smaller writes, each no larger than MaxWrite bytes.
If the underlying connection does not expose a MaxWrite limitation the conn is
returned as-is.
*/

package netx

import (
	"errors"
	"fmt"
	"net"
)

func init() {
	Register("split", func(params map[string]string, listener bool) (Wrapper, error) {
		for key := range params {
			return Wrapper{}, fmt.Errorf("split: unknown parameter %q", key)
		}
		connToConn := func(c net.Conn) (net.Conn, error) {
			return NewSplitConn(c)
		}
		return Wrapper{
			Name:   "split",
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

type splitConn struct {
	net.Conn
	maxWrite int
}

// NewSplitConn wraps c so that Write calls larger than c's MaxWrite limit are
// transparently split into multiple smaller writes.
// If c does not implement MaxWrite, or MaxWrite returns 0, c is returned unchanged.
func NewSplitConn(c net.Conn) (net.Conn, error) {
	mw, ok := c.(interface{ MaxWrite() uint16 })
	if !ok || mw.MaxWrite() == 0 {
		return nil, errors.New("split: underlying connection does not implement MaxWrite or has no MaxWrite limit")
	}
	return &splitConn{
		Conn:     c,
		maxWrite: int(mw.MaxWrite()),
	}, nil
}

// Write splits b into chunks of at most maxWrite bytes and writes each chunk
// sequentially. It returns the total number of bytes from b successfully written.
func (sc *splitConn) Write(b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		chunk := b
		if len(chunk) > sc.maxWrite {
			chunk = b[:sc.maxWrite]
		}
		n, err := sc.Conn.Write(chunk)
		total += n
		if err != nil {
			return total, err
		}
		b = b[n:]
	}
	return total, nil
}
