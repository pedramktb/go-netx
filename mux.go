package netx

import (
	"io"
	"net"
	"sync"
	"time"
)

// ServerMux combines the functionality of a connection pool and a session multiplexer.
// It listens on a physical net.Listener, accepts connections, wraps them (optional),
// reads a session ID, and routes traffic to persistent virtual sessions.
type ServerMux struct {
	net.Listener
	idMask  int // length of ID prefix in bytes
	wrapper func(net.Conn) (net.Conn, error)

	mu       sync.Mutex
	sessions map[string]*muxSession
	acceptCh chan net.Conn
	closed   chan struct{}
}

type ServerMuxOption func(*ServerMux)

func WithServerMuxWrapper(wrapper func(net.Conn) (net.Conn, error)) ServerMuxOption {
	return func(m *ServerMux) {
		m.wrapper = wrapper
	}
}

// NewServerMux creates a new ServerMux.
// ln: The physical listener.
// idMask: The length of the session ID prefix in bytes.
func NewServerMux(ln net.Listener, idMask int, opts ...ServerMuxOption) *ServerMux {
	m := &ServerMux{
		Listener: ln,
		idMask:   idMask,
		sessions: make(map[string]*muxSession),
		acceptCh: make(chan net.Conn, 100),
		closed:   make(chan struct{}),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// TODO: Return the underlying error from Accept instead of net.ErrClosed when applicable.
func (m *ServerMux) Accept() (net.Conn, error) {
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
	select {
	case c, ok := <-m.acceptCh:
		if !ok {
			return nil, net.ErrClosed
		}
		return c, nil
	case <-m.closed:
		return nil, net.ErrClosed
	}
}

func (m *ServerMux) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.closed:
		return nil
	default:
	}
	close(m.closed)
	close(m.acceptCh)
	for _, s := range m.sessions {
		s.Close()
	}
	m.sessions = nil
	return m.Listener.Close()
}

func (m *ServerMux) handleConn(c net.Conn) {
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

func (m *ServerMux) routeConn(id []byte, c net.Conn) {
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
		case m.acceptCh <- sess:
		case <-m.closed:
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

func (m *ServerMux) removeSession(id []byte) {
	m.mu.Lock()
	if m.sessions != nil {
		delete(m.sessions, string(id))
	}
	m.mu.Unlock()
}

// muxSession represents a persistent virtual connection
type muxSession struct {
	id     []byte
	parent *ServerMux

	mu         sync.Mutex
	activeConn net.Conn

	readCh  chan []byte
	readBuf []byte

	closed chan struct{}
	once   sync.Once
}

func newMuxSession(id []byte, parent *ServerMux) *muxSession {
	return &muxSession{
		id:     id,
		parent: parent,
		readCh: make(chan []byte, 100),
		closed: make(chan struct{}),
	}
}

func (s *muxSession) attach(c net.Conn) {
	s.mu.Lock()
	s.activeConn = c
	s.mu.Unlock()
}

func (s *muxSession) readLoop(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case s.readCh <- chunk:
			case <-s.closed:
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *muxSession) Read(b []byte) (n int, err error) {
	if len(s.readBuf) > 0 {
		n = copy(b, s.readBuf)
		s.readBuf = s.readBuf[n:]
		return n, nil
	}

	select {
	case data := <-s.readCh:
		n = copy(b, data)
		if n < len(data) {
			s.readBuf = data[n:]
		}
		return n, nil
	case <-s.closed:
		return 0, io.EOF
	}
}

func (s *muxSession) Write(b []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeConn == nil {
		return 0, io.ErrClosedPipe
	}
	return s.activeConn.Write(b)
}

func (s *muxSession) Close() error {
	s.once.Do(func() {
		close(s.closed)
		s.parent.removeSession(s.id)
		s.mu.Lock()
		if s.activeConn != nil {
			s.activeConn.Close()
		}
		s.mu.Unlock()
	})
	return nil
}

func (s *muxSession) RemoteID() []byte { return s.id }

func (s *muxSession) LocalAddr() net.Addr { return s.parent.Addr() }
func (s *muxSession) RemoteAddr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeConn != nil {
		return s.activeConn.RemoteAddr()
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
