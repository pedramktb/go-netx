package dtlspsk

import (
	"sync"

	"github.com/pion/dtls/v3"
)

// memSessionStore is a bounded, process-global in-memory dtls.SessionStore used
// for abbreviated-handshake resumption. It is shared across every dtlspsk
// listener/dialer in the process so a resumable session survives the per-connect
// listener teardown on the server side. Entries are capped to bound memory; a
// peer whose session was evicted simply performs a full handshake.
type memSessionStore struct {
	mu  sync.Mutex
	m   map[string]dtls.Session
	max int
}

func (s *memSessionStore) Set(key []byte, v dtls.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.m[string(key)]; !exists && len(s.m) >= s.max {
		// Bound memory: evict an arbitrary entry (Go randomizes map iteration,
		// approximating random eviction — losing a ticket only forces a full
		// handshake, so this is a DoS-safe backstop, not an LRU).
		for k := range s.m {
			delete(s.m, k)
			break
		}
	}
	s.m[string(key)] = dtls.Session{
		ID:     append([]byte(nil), v.ID...),
		Secret: append([]byte(nil), v.Secret...),
	}
	return nil
}

func (s *memSessionStore) Get(key []byte) (dtls.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[string(key)]
	if !ok {
		// A zero Session (ID == nil) signals "no session" to pion's flight
		// handlers (treated as "no resumption", not an error).
		return dtls.Session{}, nil
	}
	return dtls.Session{
		ID:     append([]byte(nil), v.ID...),
		Secret: append([]byte(nil), v.Secret...),
	}, nil
}

func (s *memSessionStore) Del(key []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, string(key))
	return nil
}

var (
	sessionStoreOnce sync.Once
	sessionStore     *memSessionStore
)

// sharedSessionStore returns the lazily-created, process-global resumption store
// used by every dtlspsk wrapper that opts in with the `resume` param.
func sharedSessionStore() dtls.SessionStore {
	sessionStoreOnce.Do(func() {
		sessionStore = &memSessionStore{m: make(map[string]dtls.Session), max: 4096}
	})
	return sessionStore
}
