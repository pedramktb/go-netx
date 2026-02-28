package netx

import (
	"errors"
	"io"
	"net"
	"sync"
)

type demuxClient struct {
	net.Conn
	id       []byte
	buf      sync.Pool
	writeMax uint16
}

func NewDemuxClient(c net.Conn, id []byte) Dialer {
	return func() (net.Conn, error) {
		m := &demuxClient{
			Conn: c,
			id:   id,
			buf: sync.Pool{
				New: func() any {
					b := make([]byte, MaxPacketSize)
					return &b
				},
			},
		}
		if mw, ok := c.(interface{ MaxWrite() uint16 }); ok && mw.MaxWrite() != 0 {
			if mw.MaxWrite() <= uint16(len(id)) {
				return nil, errors.New("demuxClient: underlying connection's MaxWrite is too small for ID")
			}
			m.writeMax = mw.MaxWrite() - uint16(len(id))
		}
		return m, nil
	}
}

func (m *demuxClient) Write(b []byte) (n int, err error) {
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
	bp := m.buf.Get().(*[]byte)
	buf := *bp
	defer m.buf.Put(bp)

	n, err = m.Conn.Read(buf)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		// Underlying transport returned an empty read (e.g. empty DNS TXT response).
		// Treat it as a no-data cycle, not an error.
		return 0, nil
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

// MaxWrite returns the maximum payload size for a single Write, accounting for the ID prefix.
// Returns 0 if the underlying connection does not advertise a limit.
func (m *demuxClient) MaxWrite() uint16 { return m.writeMax }
