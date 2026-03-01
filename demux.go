/*
Demux is a simple connection multiplexer that allows multiple virtual connections (sessions)
Each packet includes a session ID prefix and the payload.
The session ID is used to route packets to the correct virtual connection.
*/

package netx

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

func init() {
	Register("demux", func(params map[string]string, listener bool) (Wrapper, error) {
		var id []byte
		opts := []DemuxOption{}
		for key, value := range params {
			switch key {
			case "id":
				var err error
				id, err = hex.DecodeString(value)
				if err != nil {
					return Wrapper{}, fmt.Errorf("uri: invalid demux id hex parameter %q: %w", value, err)
				}
			case "accq":
				if !listener {
					return Wrapper{}, fmt.Errorf("uri: demux accept queue parameter is only valid for listeners")
				}
				size, err := strconv.ParseUint(value, 10, 16)
				if err != nil {
					return Wrapper{}, fmt.Errorf("uri: invalid demux accept queue parameter %q: %w", value, err)
				}
				opts = append(opts, WithDemuxAccQueue(uint16(size)))
			case "rq":
				if !listener {
					return Wrapper{}, fmt.Errorf("uri: demux session read queue parameter is only valid for listeners")
				}
				size, err := strconv.ParseUint(value, 10, 16)
				if err != nil {
					return Wrapper{}, fmt.Errorf("uri: invalid demux session queue parameter %q: %w", value, err)
				}
				opts = append(opts, WithDemuxReadQueue(uint16(size)))
			default:
				return Wrapper{}, fmt.Errorf("uri: unknown demux parameter %q", key)
			}
		}
		if listener {
			return Wrapper{
				Name:     "demux",
				Params:   params,
				Listener: true,
				ConnToListener: func(c net.Conn) (net.Listener, error) {
					return NewDemux(c, uint8(len(id)), opts...)
				},
				TaggedToListener: func(tc TaggedConn) (net.Listener, error) {
					return NewTaggedDemux(tc, uint8(len(id)), opts...)
				},
			}, nil
		}
		return Wrapper{
			Name:     "demux",
			Params:   params,
			Listener: false,
			ConnToDialer: func(c net.Conn) (Dialer, error) {
				return NewDemuxClient(c, id), nil
			},
		}, nil
	})
}

type demux struct {
	bc       net.Conn
	mu       sync.Mutex
	closing  atomic.Bool
	sessions map[string]*demuxSess // session ID string to session
	demuxCore
}

type demuxCore struct {
	idMask            int // length of ID prefix in bytes
	accQueue          chan net.Conn
	sessReadQueueSize int
	maxWrite          uint16
}

type DemuxOption func(*demuxCore)

// WithAccQueueSize sets the size of the accept queue for new sessions.
// Default is 1.
func WithDemuxAccQueue(size uint16) DemuxOption {
	return func(m *demuxCore) {
		m.accQueue = make(chan net.Conn, size)
	}
}

// WithReadQueueSize sets the size of the read queues of the sessions.
// Default is 128.
func WithDemuxReadQueue(size uint16) DemuxOption {
	return func(m *demuxCore) {
		m.sessReadQueueSize = int(size)
	}
}

// NewDemux creates a new Demux.
// Demux implements a simple connection multiplexer that allows multiple virtual connections (sessions)
// to be multiplexed over a single underlying net.Conn.
// idMask: The length of the session ID prefix in bytes.
func NewDemux(c net.Conn, idMask uint8, opts ...DemuxOption) (net.Listener, error) {
	m := &demux{
		bc:       c,
		sessions: make(map[string]*demuxSess),
		demuxCore: demuxCore{
			idMask:            int(idMask),
			accQueue:          make(chan net.Conn, 1),
			sessReadQueueSize: 128,
		},
	}
	if mw, ok := c.(interface{ MaxWrite() uint16 }); ok && mw.MaxWrite() != 0 {
		if mw.MaxWrite() <= uint16(idMask) {
			return nil, errors.New("demux: underlying connection's MaxWrite is too small for ID")
		}
		m.maxWrite = mw.MaxWrite() - uint16(idMask)
	}
	for _, o := range opts {
		o(&m.demuxCore)
	}
	go m.readLoop()
	return m, nil
}

func (m *demux) Accept() (net.Conn, error) {
	c, ok := <-m.accQueue
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}

func (m *demux) Close() error {
	if !m.closing.CompareAndSwap(false, true) {
		return nil
	}
	m.mu.Lock()
	close(m.accQueue)
	for _, s := range m.sessions {
		close(s.rQueue)
	}
	m.sessions = nil
	m.mu.Unlock()
	return m.bc.Close()
}

func (m *demux) readLoop() {
	defer m.bc.Close()

	buf := make([]byte, MaxPacketSize)
	for {
		n, err := m.bc.Read(buf)
		if err != nil {
			return
		}
		// Only copying the read data instead of recreating the buffer can reduce IO allocations by about 50%.
		data := make([]byte, n)
		copy(data, buf[:n])
		// Extract session ID from the beginning of the packet
		if len(data) < m.idMask {
			// Invalid packet, close connection
			return
		}
		id := data[:m.idMask]
		payload := data[m.idMask:]

		m.processPacket(id, payload)
	}
}

func (m *demux) processPacket(id, payload []byte) {
	m.mu.Lock()
	if m.sessions == nil {
		m.mu.Unlock()
		return
	}
	sess, exists := m.sessions[string(id)]
	if !exists {
		sess = &demuxSess{
			demux:        m,
			id:           id,
			rQueue:       make(chan []byte, m.sessReadQueueSize),
			readDlNotify: make(chan struct{}),
		}

		m.sessions[string(id)] = sess
		select {
		case m.accQueue <- sess:
		default:
			// If the accept queue is full, drop the new session to avoid blocking the read loop.
			delete(m.sessions, string(id))
		}
	}
	select {
	case sess.rQueue <- payload:
	default:
		// If the session's read queue is full, drop the packet to avoid blocking the read loop.
	}
	m.mu.Unlock()
}

func (m *demux) Addr() net.Addr { return m.bc.LocalAddr() }

// demuxSess represents a persistent virtual connection
type demuxSess struct {
	demux         *demux
	id            []byte
	closing       atomic.Bool
	rQueue        chan []byte
	unread        []byte
	mu            sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
	readDlNotify  chan struct{}
}

func (s *demuxSess) MaxWrite() uint16 {
	return s.demux.maxWrite
}

func (s *demuxSess) Read(b []byte) (n int, err error) {
	s.mu.Lock()
	if len(s.unread) > 0 {
		n = copy(b, s.unread)
		if n < len(s.unread) {
			s.unread = s.unread[n:]
		} else {
			s.unread = nil
		}
		s.mu.Unlock()
		return n, nil
	}
	s.mu.Unlock()

	for {
		s.mu.Lock()
		deadline := s.readDeadline
		notify := s.readDlNotify
		s.mu.Unlock()

		var timer *time.Timer
		var timeoutCh <-chan time.Time

		if !deadline.IsZero() {
			dur := time.Until(deadline)
			if dur <= 0 {
				return 0, os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(dur)
			timeoutCh = timer.C
		}

		select {
		case data, ok := <-s.rQueue:
			if timer != nil {
				timer.Stop()
			}
			if !ok {
				return 0, io.EOF
			}

			s.mu.Lock()
			n = copy(b, data)
			if n < len(data) {
				s.unread = data[n:]
			}
			s.mu.Unlock()
			return n, nil
		case <-timeoutCh:
			return 0, os.ErrDeadlineExceeded
		case <-notify:
			if timer != nil {
				timer.Stop()
			}
			// Deadline changed, loop to pick up new deadline
		}
	}
}

func (s *demuxSess) Write(b []byte) (n int, err error) {
	s.mu.Lock()
	deadline := s.writeDeadline
	s.mu.Unlock()

	if !deadline.IsZero() && time.Now().After(deadline) {
		return 0, os.ErrDeadlineExceeded
	}

	if len(b)+len(s.id) > MaxPacketSize {
		return 0, errors.New("demux: packet too large")
	}

	// Re-construct payload with ID
	payload := append(s.id, b...)

	n, err = s.demux.bc.Write(payload)
	if err != nil {
		return 0, err
	}

	if n < len(s.id) {
		return 0, io.ErrShortWrite
	}
	return n - len(s.id), nil
}

func (s *demuxSess) Close() error {
	if !s.closing.CompareAndSwap(false, true) {
		return nil
	}
	s.demux.mu.Lock()
	close(s.rQueue)
	if s.demux.sessions != nil {
		delete(s.demux.sessions, string(s.id))
	}
	s.demux.mu.Unlock()
	return nil
}

func (s *demuxSess) ID() []byte          { return s.id }
func (s *demuxSess) LocalAddr() net.Addr { return s.demux.Addr() }
func (s *demuxSess) RemoteAddr() net.Addr {
	return &demuxVirtualAddr{Addr: s.demux.bc.RemoteAddr(), id: s.id}
}
func (s *demuxSess) SetDeadline(t time.Time) error {
	if err := s.SetReadDeadline(t); err != nil {
		return err
	}
	return s.SetWriteDeadline(t)
}

func (s *demuxSess) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readDeadline = t
	if s.readDlNotify != nil {
		close(s.readDlNotify)
		s.readDlNotify = make(chan struct{})
	}
	return nil
}

func (s *demuxSess) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeDeadline = t
	return nil
}

type demuxVirtualAddr struct {
	net.Addr
	id []byte
}

func (a *demuxVirtualAddr) String() string {
	return a.String() + ":" + hex.EncodeToString(a.id)
}
