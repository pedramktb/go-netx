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
	"math"
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
		var bufSize uint32
		var accQueueSize, sessQueueSize uint32
		for key, value := range params {
			switch key {
			case "id":
				var err error
				id, err = hex.DecodeString(value)
				if err != nil {
					return Wrapper{}, fmt.Errorf("uri: invalid demux id hex parameter %q: %w", value, err)
				}
			case "bufsize":
				size, err := strconv.ParseUint(value, 10, 32)
				if err != nil {
					return Wrapper{}, fmt.Errorf("uri: invalid demux bufsize parameter %q: %w", value, err)
				}
				bufSize = uint32(size)
			case "accqueuesize":
				size, err := strconv.ParseUint(value, 10, 32)
				if err != nil {
					return Wrapper{}, fmt.Errorf("uri: invalid demux accqueuesize parameter %q: %w", value, err)
				}
				accQueueSize = uint32(size)
			case "sessqueuesize":
				size, err := strconv.ParseUint(value, 10, 32)
				if err != nil {
					return Wrapper{}, fmt.Errorf("uri: invalid demux sessqueuesize parameter %q: %w", value, err)
				}
				sessQueueSize = uint32(size)
			default:
				return Wrapper{}, fmt.Errorf("uri: unknown demux parameter %q", key)
			}
		}
		if listener {
			var opts []DemuxOption
			if bufSize > 0 {
				opts = append(opts, WithDemuxBufSize(bufSize))
			}
			if accQueueSize > 0 {
				opts = append(opts, WithDemuxAccQueueSize(accQueueSize))
			}
			if sessQueueSize > 0 {
				opts = append(opts, WithDemuxSessQueueSize(sessQueueSize))
			}
			return Wrapper{
				Name:     "demux",
				Params:   params,
				Listener: true,
				ConnToListener: func(c net.Conn) (net.Listener, error) {
					return NewDemux(c, len(id), opts...), nil
				},
				TaggedToListener: func(tc TaggedConn) (net.Listener, error) {
					return NewTaggedDemux(tc, len(id), opts...), nil
				},
			}, nil
		}
		var clientOpts []DemuxClientOption
		if bufSize > 0 {
			clientOpts = append(clientOpts, WithDemuxClientBufSize(bufSize))
		}
		return Wrapper{
			Name:     "demux",
			Params:   params,
			Listener: false,
			ConnToDialer: func(c net.Conn) (Dialer, error) {
				return NewDemuxClient(c, id, clientOpts...), nil
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
	idMask        int // length of ID prefix in bytes
	accQueue      chan net.Conn
	sessQueueSize int
	bufSize       int
}

type DemuxOption func(*demuxCore)

// WithAccQueueSize sets the size of the accept queue for new sessions.
// Default is 0.
func WithDemuxAccQueueSize(size uint32) DemuxOption {
	return func(m *demuxCore) {
		m.accQueue = make(chan net.Conn, size)
	}
}

// WithSessQueueSize sets the size of the read and write queues of the sessions.
// Default is 8.
func WithDemuxSessQueueSize(size uint32) DemuxOption {
	return func(m *demuxCore) {
		m.sessQueueSize = int(size)
		if m.sessQueueSize < 0 {
			m.sessQueueSize = math.MaxInt32
		}
	}
}

// WithSessionBufSize sets the size of the read and write buffers for each session.
// This controls how much data is read or written from the underlying connection at once.
// Default is 4096.
func WithDemuxBufSize(size uint32) DemuxOption {
	return func(m *demuxCore) {
		m.bufSize = int(size)
		if m.bufSize <= 0 {
			m.bufSize = math.MaxInt32
		}
	}
}

// NewDemux creates a new Demux.
// Demux implements a simple connection multiplexer that allows multiple virtual connections (sessions)
// to be multiplexed over a single underlying net.Conn.
// idMask: The length of the session ID prefix in bytes.
func NewDemux(c net.Conn, idMask int, opts ...DemuxOption) net.Listener {
	m := &demux{
		bc:       c,
		sessions: make(map[string]*demuxSess),
		demuxCore: demuxCore{
			idMask:        idMask,
			accQueue:      make(chan net.Conn),
			bufSize:       4096,
			sessQueueSize: 8,
		},
	}
	for _, opt := range opts {
		opt(&m.demuxCore)
	}
	go m.readLoop()
	return m
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

	buf := make([]byte, m.bufSize)
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

		m.processPacket(id, payload, nil)
	}
}

func (m *demux) processPacket(id, payload []byte, tag any) {
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
			rmtAddr:      m.bc.RemoteAddr(),
			rQueue:       make(chan []byte, m.sessQueueSize),
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
	rmtAddr       net.Addr
	closing       atomic.Bool
	rQueue        chan []byte
	unread        []byte
	mu            sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
	readDlNotify  chan struct{}
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

	if len(b)+len(s.id) > s.demux.bufSize {
		return 0, errors.New("demuxSess: packet may be truncated; increase demux buffer size or frame the packets")
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

func (s *demuxSess) ID() []byte           { return s.id }
func (s *demuxSess) LocalAddr() net.Addr  { return s.demux.Addr() }
func (s *demuxSess) RemoteAddr() net.Addr { return s.rmtAddr }
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
