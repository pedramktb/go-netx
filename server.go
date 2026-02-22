package netx

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrServerClosed = errors.New("server is shutting down")
)

// Handler is a function that takes a base context, a net.Conn representing the incoming connection,
// and a closed function that should be called when the user is done with the connection.
// It returns a boolean indicating whether the connection matches the handler
// and a wrappedConn server can continue using for closing them.
//
// You should implement Handler in a way that it returns true for connections that should be handled by this handler.
// If the connection does not match, return false and nil wrappedConn. Do not call the closed function.
// Matching should be handled early, as the server will simply try handlers to find a match.
// The wrappedConn can be the same as the input conn, or a wrapped version of it (e.g. with TLS, obfuscation, etc).
type Handler func(ctx context.Context, conn net.Conn, closed func()) (matched bool, wrappedConn io.Closer)

// Server initially accepts no connections, since there are no initial handlers.
// It's the duty of the caller to add handlers via SetRoute.
// The generic ID type is used to identify different handlers, e.g. packet header, http path, remote address, username, etc.
type Server[ID comparable] struct {
	Logger Logger

	// We use a copy-on-write pattern to allow fast handler lookup.
	routes   atomic.Value
	routesMu sync.Mutex

	closing atomic.Bool

	mu sync.Mutex

	listeners     map[net.Listener]struct{}
	listenerGroup sync.WaitGroup

	conns map[*io.Closer]struct{}
}

func (s *Server[ID]) Serve(ctx context.Context, listener net.Listener) error {
	if s.Logger == nil {
		s.Logger = slog.Default()
	}

	if !s.addListener(listener) {
		return ErrServerClosed
	}
	defer s.removeListener(listener)

	for {
		conn, err := listener.Accept()
		if err != nil {
			if s.closing.Load() {
				return ErrServerClosed
			}
			s.Logger.WarnContext(ctx, "error accepting connection", "error", err.Error())
			continue
		}
		go s.route(ctx, conn)
	}
}

// SetRoute sets a handler for a specific ID.
// If a handler already exists for this ID, it will be replaced.
// It does not close any existing connections that were created by the previous handler, but new connections will use the new handler.
func (s *Server[ID]) SetRoute(id ID, handler Handler) {
	s.routesMu.Lock()
	defer s.routesMu.Unlock()
	old, _ := s.routes.Load().([]route[ID])
	// if there are no routes yet, initialize with the first one
	if old == nil {
		s.routes.Store([]route[ID]{{id: id, handler: handler}})
		return
	}
	// check if we need to replace an existing route; always copy-on-write
	for i, r := range old {
		if r.id == id {
			newRoutes := make([]route[ID], len(old))
			copy(newRoutes, old)
			newRoutes[i] = route[ID]{id: id, handler: handler}
			s.routes.Store(newRoutes)
			return
		}
	}
	// append a new route by creating a new slice to avoid modifying shared backing array
	newRoutes := make([]route[ID], len(old)+1)
	copy(newRoutes, old)
	newRoutes[len(old)] = route[ID]{id: id, handler: handler}
	s.routes.Store(newRoutes)
}

// RemoveRoute removes a handler by its ID.
// It does not close any existing connections that were created by this handler.
func (s *Server[ID]) RemoveRoute(id ID) {
	s.routesMu.Lock()
	defer s.routesMu.Unlock()
	routes, _ := s.routes.Load().([]route[ID])
	if len(routes) == 0 {
		return
	}
	// build a new slice excluding the id
	newRoutes := make([]route[ID], 0, len(routes))
	for _, r := range routes {
		if r.id != id {
			newRoutes = append(newRoutes, r)
		}
	}
	s.routes.Store(newRoutes)
}

type route[ID comparable] struct {
	id      ID
	handler Handler
}

func (s *Server[ID]) route(ctx context.Context, conn net.Conn) {
	routes, ok := s.routes.Load().([]route[ID])
	if !ok {
		_ = conn.Close()
		s.Logger.DebugContext(ctx, "no routes configured, dropping connection", "addr", conn.RemoteAddr().String())
		return
	}
	for _, r := range routes {
		connCloser := io.Closer(conn)
		var wConn *io.Closer = &connCloser
		var ok bool
		closeCooldown := make(chan struct{}, 1)
		ok, connCloser = r.handler(ctx, conn, func() {
			<-closeCooldown
			s.mu.Lock()
			delete(s.conns, wConn)
			s.mu.Unlock()
		})
		if !ok {
			continue
		}
		// Fallback to original conn if handler returned nil closer
		if connCloser == nil {
			connCloser = conn
		}
		s.mu.Lock()
		if s.conns == nil {
			s.conns = make(map[*io.Closer]struct{})
		}
		s.conns[wConn] = struct{}{}
		s.mu.Unlock()
		closeCooldown <- struct{}{}
		return
	}
	_ = conn.Close() // make sure to close the connection if not already closed by the handler
	s.Logger.DebugContext(ctx, "unhandled connection, dropping connection", "addr", conn.RemoteAddr().String())
}

func (s *Server[ID]) addListener(l net.Listener) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listeners == nil {
		s.listeners = make(map[net.Listener]struct{})
	}
	if s.closing.Load() {
		return false
	}
	s.listeners[l] = struct{}{}
	s.listenerGroup.Add(1)
	return true
}

func (s *Server[ID]) removeListener(l net.Listener) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.listeners, l)
	s.listenerGroup.Done()
}

func (s *Server[ID]) Close() error {
	if !s.closing.CompareAndSwap(false, true) {
		return nil
	}

	// First close listeners under lock
	s.mu.Lock()
	var err error
	for l := range s.listeners {
		if cErr := l.Close(); cErr != nil {
			err = errors.Join(err, cErr)
		}
	}
	s.mu.Unlock() // unlock before waiting to avoid deadlock

	// Wait for Serve to remove all listeners
	s.listenerGroup.Wait()

	// Now close all active connections under lock
	s.mu.Lock()
	for c := range s.conns {
		_ = (*c).Close()
		delete(s.conns, c)
	}
	s.mu.Unlock()

	return err
}

// Shutdown gracefully shuts down the server without interrupting active connections.
// It stops accepting new connections and waits until all tracked connections finish
// or the provided context is done. If the context is done before all connections
// finish, Shutdown will force-close remaining connections and return the context error
// joined with any listener close error.
func (s *Server[ID]) Shutdown(ctx context.Context) error {
	if !s.closing.CompareAndSwap(false, true) {
		return nil
	}

	// Close listeners to stop accepting new connections
	s.mu.Lock()
	var err error
	for l := range s.listeners {
		if cErr := l.Close(); cErr != nil {
			err = errors.Join(err, cErr)
		}
	}
	s.mu.Unlock()

	// Wait for Serve to remove all listeners
	s.listenerGroup.Wait()

	// Wait for active connections to finish, honoring context
	// Ticker to avoid busy waiting
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		remaining := len(s.conns)
		s.mu.Unlock()
		if remaining == 0 {
			return err
		}
		select {
		case <-ctx.Done():
			// Timeout/cancellation: force close remaining connections
			s.mu.Lock()
			for c := range s.conns {
				_ = (*c).Close()
				delete(s.conns, c)
			}
			s.mu.Unlock()
			return errors.Join(err, ctx.Err())
		case <-ticker.C:
			// re-check
		}
	}
}
