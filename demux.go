/*
Demux is a simple connection multiplexer that allows multiple virtual connections (sessions)
Each packet includes a session ID prefix and the payload.
The session ID is used to route packets to the correct virtual connection.
*/

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

type demux struct {
	bc            net.Conn
	idMask        int // length of ID prefix in bytes
	mu            sync.Mutex
	closing       atomic.Bool
	accQueue      chan net.Conn
	sessions      map[string]*demuxSess // session ID string to session
	sessQueueSize int
	bufSize       int
}

type DemuxOption func(*demux)

// WithAccQueueSize sets the size of the accept queue for new sessions.
// Default is 0.
func WithDemuxAccQueueSize(size uint32) DemuxOption {
	return func(m *demux) {
		m.accQueue = make(chan net.Conn, size)
	}
}

// WithSessQueueSize sets the size of the read and write queues of the sessions.
// Default is 8.
func WithDemuxSessQueueSize(size uint32) DemuxOption {
	return func(m *demux) {
		m.sessQueueSize = int(size)
	}
}

// WithSessionBufSize sets the size of the read and write buffers for each session.
// This controls how much data is read or written from the underlying connection at once.
// Default is 4096.
func WithDemuxBufSize(size uint32) DemuxOption {
	return func(m *demux) {
		m.bufSize = int(size)
	}
}

// NewDemux creates a new Demux.
// Demux implements a simple connection multiplexer that allows multiple virtual connections (sessions)
// to be multiplexed over a single underlying net.Conn.
// idMask: The length of the session ID prefix in bytes.
func NewDemux(c net.Conn, idMask int, opts ...DemuxOption) net.Listener {
	m := &demux{
		bc:            c,
		idMask:        idMask,
		accQueue:      make(chan net.Conn),
		bufSize:       4096,
		sessions:      make(map[string]*demuxSess),
		sessQueueSize: 8,
	}
	for _, opt := range opts {
		opt(m)
	}
	go m.readLoop(c)
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

func (m *demux) readLoop(c net.Conn) {
	defer c.Close()
	// Check if underlying connection supports Context.
	cc, hasContext := c.(CtxConn)

	buf := make([]byte, m.bufSize)
	for {
		var n int
		var err error
		var ctx any

		if hasContext {
			n, ctx, err = cc.ReadCtx(buf)
		} else {
			n, err = c.Read(buf)
		}
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
				rmtAddr:      c.RemoteAddr(),
				rQueue:       make(chan CtxData, m.sessQueueSize),
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
		case sess.rQueue <- CtxData{Data: payload, Ctx: ctx}:
		default:
			// If the session's read queue is full, drop the packet to avoid blocking the read loop.
		}
		m.mu.Unlock()
	}
}

func (m *demux) Addr() net.Addr {
	return m.bc.LocalAddr()
}

// demuxSess represents a persistent virtual connection
type demuxSess struct {
	demux           *demux
	id              []byte
	rmtAddr         net.Addr
	closing         atomic.Bool
	rQueue          chan CtxData
	unread          []byte
	mu              sync.Mutex
	readDeadline    time.Time
	writeDeadline   time.Time
	readDlNotify    chan struct{}
	pendingContexts []any
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
		case pkt, ok := <-s.rQueue:
			if timer != nil {
				timer.Stop()
			}
			if !ok {
				return 0, io.EOF
			}
			data := pkt.Data
			s.mu.Lock()
			if pkt.Ctx != nil {
				s.pendingContexts = append(s.pendingContexts, pkt.Ctx)
			}
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
	var ctx any
	if len(s.pendingContexts) > 0 {
		ctx = s.pendingContexts[0]
		s.pendingContexts = s.pendingContexts[1:]
	}
	s.mu.Unlock()

	if !deadline.IsZero() && time.Now().After(deadline) {
		return 0, os.ErrDeadlineExceeded
	}

	if len(b)+len(s.id) > s.demux.bufSize {
		return 0, errors.New("demuxSess: packet may be truncated; increase demux buffer size or frame the packets")
	}

	// Re-construct payload with ID
	payload := append(s.id, b...)

	if cc, ok := s.demux.bc.(CtxConn); ok {
		n, err = cc.WriteCtx(payload, ctx)
	} else {
		n, err = s.demux.bc.Write(payload)
	}

	if err != nil {
		return 0, err
	}
	// The underlying write returns n including ID length usually?
	// DemuxClient implementation does check n != len(b)+len(id).
	// But `net.Conn.Write` semantics: "returns the number of bytes written from b (0 <= n <= len(b))"
	// Senders responsibility.
	// If `s.demux.bc.Write` returns full packet len (ID + Data).
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

type demuxClient struct {
	net.Conn
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
	if n != len(b)+len(m.id) {
		return 0, io.ErrShortWrite
	}
	return n - len(m.id), nil
}

func (m *demuxClient) Read(b []byte) (n int, err error) {
	// We need to read the ID first.
	// We assume that the underlying connection preserves message boundaries (like UDP or DNST)
	// or that the first bytes we read are always the ID.
	// Since we can't peek, we have to read into a buffer large enough to hold at least the ID.
	// Uses the client's bufSize or a reasonable default if 0 (though constructor sets it).
	buf := make([]byte, m.bufSize)
	n, err = m.Conn.Read(buf)
	if err != nil {
		return 0, err
	}
	if n < len(m.id) {
		// Packet too short to contain ID
		return 0, nil // Or ignore?
	}
	// Check if ID matches? The client is bound to one ID.
	// For now just strip the ID.
	copy(b, buf[len(m.id):n])
	return n - len(m.id), nil
}

func (m *demuxClient) ID() []byte { return m.id }
