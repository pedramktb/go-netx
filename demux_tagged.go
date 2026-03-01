package netx

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type taggedDemux struct {
	bc       TaggedConn
	mu       sync.Mutex
	closing  atomic.Bool
	sessions map[string]*taggedDemuxSess // session ID string to session
	demuxCore
}

// NewDemuxTagged creates a new Demux with a TaggedConn.
// Demux implements a simple connection multiplexer that allows multiple virtual connections (sessions)
// to be multiplexed over a single underlying TaggedConn.
// idMask: The length of the session ID prefix in bytes.
func NewTaggedDemux(c TaggedConn, idMask uint8, opts ...DemuxOption) (net.Listener, error) {
	m := &taggedDemux{
		bc:       c,
		sessions: make(map[string]*taggedDemuxSess),
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

func (m *taggedDemux) Accept() (net.Conn, error) {
	c, ok := <-m.accQueue
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}

func (m *taggedDemux) Close() error {
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

func (m *taggedDemux) readLoop() {
	defer m.bc.Close()

	buf := make([]byte, MaxPacketSize)
	var tag any
	for {
		n, err := m.bc.ReadTagged(buf, &tag)
		if err != nil {
			return
		}

		data := make([]byte, n)
		copy(data, buf[:n])

		if len(data) < m.idMask {
			return
		}
		id := data[:m.idMask]
		payload := data[m.idMask:]

		m.processPacket(id, payload, tag)
	}
}

func (m *taggedDemux) processPacket(id, payload []byte, tag any) {
	m.mu.Lock()
	if m.sessions == nil {
		m.mu.Unlock()
		return
	}
	sess, exists := m.sessions[string(id)]
	if !exists {
		sess = &taggedDemuxSess{
			demux: m,
			id:    id,
			rQueue: make(chan struct {
				data []byte
				tag  any
			}, m.sessReadQueueSize),
			tagQueue:     make(chan any, m.sessReadQueueSize*2),
			closed:       make(chan struct{}),
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
	case sess.rQueue <- struct {
		data []byte
		tag  any
	}{data: payload, tag: tag}:
	default:
		// If the session's read queue is full, drop the packet to avoid blocking the read loop.
	}
	m.mu.Unlock()
}
func (m *taggedDemux) Addr() net.Addr { return m.bc.LocalAddr() }

type taggedDemuxSess struct {
	demux   *taggedDemux
	id      []byte
	closing atomic.Bool
	rQueue  chan struct {
		data []byte
		tag  any
	}
	tagQueue      chan any
	closed        chan struct{}
	unread        []byte
	mu            sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
	readDlNotify  chan struct{}
}

func (s *taggedDemuxSess) MaxWrite() uint16 {
	return s.demux.maxWrite
}

func (s *taggedDemuxSess) Read(b []byte) (n int, err error) {
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
		case td, ok := <-s.rQueue:
			if timer != nil {
				timer.Stop()
			}
			if !ok {
				return 0, io.EOF
			}
			select {
			case s.tagQueue <- td.tag:
			case <-s.closed:
				return 0, net.ErrClosed
			}

			s.mu.Lock()
			n = copy(b, td.data)
			if n < len(td.data) {
				s.unread = td.data[n:]
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

func (s *taggedDemuxSess) Write(b []byte) (n int, err error) {
	s.mu.Lock()
	deadline := s.writeDeadline
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
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	var tag any
	select {
	case t, ok := <-s.tagQueue:
		if !ok {
			return 0, net.ErrClosed
		}
		tag = t
	case <-s.closed:
		return 0, net.ErrClosed
	case <-timeoutCh:
		return 0, os.ErrDeadlineExceeded
	}

	if len(b)+len(s.id) > MaxPacketSize {
		return 0, errors.New("demux: packet too large")
	}

	// Re-construct payload with ID
	payload := append(s.id, b...)

	n, err = s.demux.bc.WriteTagged(payload, tag)
	if err != nil {
		return 0, err
	}

	if n < len(s.id) {
		return 0, io.ErrShortWrite
	}
	return n - len(s.id), nil
}

func (s *taggedDemuxSess) Close() error {
	if !s.closing.CompareAndSwap(false, true) {
		return nil
	}
	s.demux.mu.Lock()
	if s.demux.sessions != nil {
		close(s.rQueue)
		delete(s.demux.sessions, string(s.id))
	}
	close(s.closed)
	s.demux.mu.Unlock()
	return nil
}

func (s *taggedDemuxSess) ID() []byte          { return s.id }
func (s *taggedDemuxSess) LocalAddr() net.Addr { return s.demux.Addr() }
func (s *taggedDemuxSess) RemoteAddr() net.Addr {
	return &demuxVirtualAddr{Addr: s.demux.bc.RemoteAddr(), id: s.id}
}
func (s *taggedDemuxSess) SetDeadline(t time.Time) error {
	if err := s.SetReadDeadline(t); err != nil {
		return err
	}
	return s.SetWriteDeadline(t)
}

func (s *taggedDemuxSess) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readDeadline = t
	if s.readDlNotify != nil {
		close(s.readDlNotify)
		s.readDlNotify = make(chan struct{})
	}
	return nil
}

func (s *taggedDemuxSess) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeDeadline = t
	return nil
}
