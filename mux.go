package netx

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ServerMux combines the functionality of a connection pool and a session multiplexer.
// It listens on a physical net.Listener, accepts connections, wraps them (optional),
// reads a session ID, and routes traffic to persistent virtual sessions.
type serverMux struct {
	net.Listener
	idMask                int // length of ID prefix in bytes
	wrapper               func(net.Conn) (net.Conn, error)
	mu                    sync.Mutex
	closing               atomic.Bool
	acceptQueue           chan net.Conn
	sessions              map[string]*muxSession
	sessionReadQueueSize  int
	sessionReadBufferSize int
	sync                  bool
}

type ServerMuxOption func(*serverMux)

// WithServerMuxWrapper sets a connection wrapper for the ServerMux.
// The wrapper is applied to each incoming connection before reading the session ID.
func WithServerMuxWrapper(wrapper func(net.Conn) (net.Conn, error)) ServerMuxOption {
	return func(m *serverMux) {
		m.wrapper = wrapper
	}
}

// WithServerAcceptQueueSize sets the size of the accept queue for the ServerMux.
// Default is 128.
func WithServerAcceptQueueSize(size uint32) ServerMuxOption {
	return func(m *serverMux) {
		m.acceptQueue = make(chan net.Conn, size)
	}
}

// WithServerSessionReadQueueSize sets the size of the read queue for each session.
// This controls how much data can be stored in memory per session before blocking reads.
// Default is 128.
func WithServerSessionReadQueueSize(size uint32) ServerMuxOption {
	return func(m *serverMux) {
		m.sessionReadQueueSize = int(size)
	}
}

// WithServerSessionReadBufferSize sets the size of the read buffer for each session.
// This controls how much data is read from the underlying connection at once.
// Default is 4096.
func WithServerSessionReadBufferSize(size uint32) ServerMuxOption {
	return func(m *serverMux) {
		m.sessionReadBufferSize = int(size)
	}
}

// WithServerSyncMode enables synchronous mode for the ServerMux.
// In this mode, writes will happen synchronously to the connection that data is read from.
// Default is asynchronous.
func WithServerSyncMode() ServerMuxOption {
	return func(m *serverMux) {
		m.sync = true
	}
}

// NewServerMux creates a new ServerMux.
// ln: The physical listener.
// idMask: The length of the session ID prefix in bytes.
func NewServerMux(ln net.Listener, idMask int, opts ...ServerMuxOption) *serverMux {
	m := &serverMux{
		Listener:             ln,
		idMask:               idMask,
		acceptQueue:          make(chan net.Conn, 128),
		sessions:             make(map[string]*muxSession),
		sessionReadQueueSize: 128,
	}
	for _, opt := range opts {
		opt(m)
	}
	go func() {
		for {
			c, err := m.Listener.Accept()
			if err != nil {
				m.Close()
				return
			}
			go m.handleConn(c)
		}
	}()
	return m
}

func (m *serverMux) Accept() (net.Conn, error) {
	c, ok := <-m.acceptQueue
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}

func (m *serverMux) Close() error {
	if !m.closing.CompareAndSwap(false, true) {
		return nil
	}
	close(m.acceptQueue)
	for _, s := range m.sessions {
		s.Close()
	}
	m.sessions = nil
	return m.Listener.Close()
}

func (m *serverMux) handleConn(c net.Conn) {
	// Important to (un)wrap before reading ID as the whole purpose of the wrapper may be to
	// obscure the ID or detecting it's existence.
	if m.wrapper != nil {
		var err error
		c, err = m.wrapper(c)
		if err != nil {
			c.Close()
			return
		}
	}

	if m.idMask > 0 {
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, m.idMask)
		if _, err := io.ReadFull(c, buf); err != nil {
			c.Close()
			return
		}
		c.SetReadDeadline(time.Time{})
		m.routeConn(buf, c)
	} else {
		m.routeConn(nil, c)
	}
}

func (m *serverMux) routeConn(id []byte, c net.Conn) {
	m.mu.Lock()
	if m.sessions == nil { // Closed
		m.mu.Unlock()
		c.Close()
		return
	}

	sess, exists := m.sessions[string(id)]
	if !exists {
		sess = newMuxSession(id, m)
		m.sessions[string(id)] = sess
		select {
		case m.acceptQueue <- sess:
		default:
			// Accept queue full or closed, drop session
			sess.Close()
			m.mu.Unlock()
			c.Close()
			return
		}
	}

	// "Roaming" / Pooling logic: active conn is the latest one
	sess.attach(c)
	m.mu.Unlock()

	// Start forwarding
	sess.readLoop(c)
}

func (m *serverMux) removeSession(id []byte) {
	m.mu.Lock()
	if m.sessions != nil {
		delete(m.sessions, string(id))
	}
	m.mu.Unlock()
}

// muxSession represents a persistent virtual connection
type muxSession struct {
	parent    *serverMux
	id        []byte
	mu        sync.Mutex
	closing   atomic.Bool
	conn      net.Conn // currently active connection
	readQueue chan []byte
	readBuf   []byte
}

func newMuxSession(id []byte, parent *serverMux) *muxSession {
	return &muxSession{
		parent:    parent,
		id:        id,
		readQueue: make(chan []byte, parent.sessionReadQueueSize),
	}
}

func (s *muxSession) attach(c net.Conn) {
	s.mu.Lock()
	s.conn = c
	s.mu.Unlock()
}

func (s *muxSession) readLoop(c net.Conn) {
	defer c.Close()
	buf := make([]byte, s.parent.sessionReadBufferSize)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			select {
			case s.readQueue <- buf[:n]:
				if s.parent.sync {
					// TODO: force write to the same connection if in sync mode
				}
			default:
				// Read queue full or connection closed
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *muxSession) Read(b []byte) (n int, err error) {
	data, ok := <-s.readQueue
	if !ok {
		return 0, io.EOF
	}
	if len(b) < len(data) {
		return 0, io.ErrShortBuffer
	}
	n = copy(b, data)
	return n, nil
}

func (s *muxSession) Write(b []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return 0, io.ErrClosedPipe
	}
	return s.conn.Write(b)
}

func (s *muxSession) Close() error {
	if !s.closing.CompareAndSwap(false, true) {
		return nil
	}
	close(s.readQueue)
	s.parent.removeSession(s.id)
	s.mu.Lock()
	if s.conn != nil {
		s.conn.Close()
	}
	s.mu.Unlock()
	return nil
}

func (s *muxSession) RemoteID() []byte { return s.id }

func (s *muxSession) LocalAddr() net.Addr { return s.parent.Addr() }
func (s *muxSession) RemoteAddr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		return s.conn.RemoteAddr()
	}
	return &net.IPAddr{}
}
func (s *muxSession) SetDeadline(t time.Time) error      { return nil }
func (s *muxSession) SetReadDeadline(t time.Time) error  { return nil }
func (s *muxSession) SetWriteDeadline(t time.Time) error { return nil }

type ClientMuxOption func(*clientMux)

func WithClientMuxWrapper(wrapper func(net.Conn) (net.Conn, error)) ClientMuxOption {
	return func(c *clientMux) {
		c.wrapper = wrapper
	}
}

// NewClientMux creates a new ClientMux.
func NewClientMux(dialer func() (net.Conn, error), id []byte, options ...ClientMuxOption) func() (net.Conn, error) {
	c := &clientMux{
		dialer: dialer,
		id:     id,
	}
	for _, option := range options {
		option(c)
	}
	return func() (net.Conn, error) {
		return c, nil
	}
}

type clientMux struct {
	dialer  func() (net.Conn, error)
	wrapper func(net.Conn) (net.Conn, error)
	id      []byte

	readBuf    []byte
	mu         sync.Mutex
	activeConn net.Conn
}

func (m *clientMux) Write(b []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.activeConn == nil {
		if err := m.reconnect(); err != nil {
			return 0, err
		}
	}

	pkt := make([]byte, len(m.id)+len(b))
	copy(pkt, m.id)
	copy(pkt[len(m.id):], b)

	_, err := m.activeConn.Write(pkt)
	if err != nil {
		m.activeConn.Close()
		m.activeConn = nil
		if rErr := m.reconnect(); rErr == nil {
			_, err = m.activeConn.Write(pkt)
		}
		if err != nil {
			return 0, err
		}
	}

	buf := make([]byte, 65536)
	n, err := m.activeConn.Read(buf)
	if err != nil {
		m.activeConn.Close()
		m.activeConn = nil
		if err == io.EOF {
			return len(b), nil
		}
		return len(b), err
	}

	m.mu.Lock()
	m.readBuf = append(m.readBuf, buf[:n]...)
	m.mu.Unlock()

	return len(b), nil
}

func (m *clientMux) reconnect() error {
	conn, err := m.dialer()
	if err != nil {
		return err
	}
	if m.wrapper != nil {
		conn, err = m.wrapper(conn)
		if err != nil {
			conn.Close()
			return err
		}
	}
	m.activeConn = conn
	return nil
}

func (m *clientMux) Read(b []byte) (int, error) {
	m.mu.Lock()
	if len(m.readBuf) > 0 {
		n := copy(b, m.readBuf)
		m.readBuf = m.readBuf[n:]
		m.mu.Unlock()
		return n, nil
	}
	m.mu.Unlock()

	return m.poll(b)
}

func (m *clientMux) poll(b []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.activeConn == nil {
		if err := m.reconnect(); err != nil {
			return 0, err
		}
	}

	if _, err := m.activeConn.Write(m.id); err != nil {
		m.activeConn.Close()
		m.activeConn = nil
		return 0, err
	}

	buf := make([]byte, 65536)
	n, err := m.activeConn.Read(buf)
	if err != nil {
		m.activeConn.Close()
		m.activeConn = nil
		return 0, err
	}

	copied := copy(b, buf[:n])
	if copied < n {
		m.mu.Lock()
		m.readBuf = append(m.readBuf, buf[copied:n]...)
		m.mu.Unlock()
	}
	return copied, nil
}

func (m *clientMux) LocalID() []byte { return m.id }

func (m *clientMux) Close() error { return nil }
func (m *clientMux) LocalAddr() net.Addr {
	if m.activeConn != nil {
		return m.activeConn.LocalAddr()
	}
	return &net.IPAddr{}
}
func (m *clientMux) RemoteAddr() net.Addr {
	if m.activeConn != nil {
		return m.activeConn.RemoteAddr()
	}
	return &net.IPAddr{}
}
func (m *clientMux) SetDeadline(t time.Time) error      { return nil }
func (m *clientMux) SetReadDeadline(t time.Time) error  { return nil }
func (m *clientMux) SetWriteDeadline(t time.Time) error { return nil }
