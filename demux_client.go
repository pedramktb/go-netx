package netx

import (
	"errors"
	"io"
	"net"
)

type demuxClient struct {
	net.Conn
	tc      TaggedConn
	id      []byte
	bufSize int
}

type DemuxClientOption func(*demuxClient)

// WithDemuxClientBufSize sets the size of the write buffer for the demux client.
// This controls how much data is written to the underlying connection at once.
// Default is 4096.
func WithDemuxClientBufSize(size uint32) DemuxClientOption {
	return func(c *demuxClient) {
		c.bufSize = int(size)
	}
}

func NewDemuxClient(c net.Conn, id []byte, opts ...DemuxClientOption) (net.Conn, error) {
	m := &demuxClient{
		Conn:    c,
		id:      id,
		bufSize: 4096,
	}
	if tc, ok := c.(TaggedConn); ok {
		m.tc = tc
	}
	for _, opt := range opts {
		opt(m)
	}
	return m, nil
}

func (m *demuxClient) Write(b []byte) (n int, err error) {
	if len(b)+len(m.id) > m.bufSize {
		return 0, errors.New("demuxClient: packet may be truncated; increase demux buffer size or frame the packets")
	}
	n, err = m.Conn.Write(append(m.id, b...))
	if err != nil {
		return 0, err
	}
	if n < len(m.id) {
		return 0, io.ErrShortWrite
	}
	return n - len(m.id), nil
}

func (m *demuxClient) Read(b []byte) (n int, err error) {
	buf := make([]byte, m.bufSize)
	n, err = m.Conn.Read(buf)
	if err != nil {
		return 0, err
	}
	if n < len(m.id) {
		return 0, io.ErrUnexpectedEOF
	}
	if string(buf[:len(m.id)]) != string(m.id) {
		return 0, errors.New("demuxClient: received packet with mismatched ID")
	}
	copy(b, buf[len(m.id):n])
	return n - len(m.id), nil
}

func (m *demuxClient) ID() []byte { return m.id }
