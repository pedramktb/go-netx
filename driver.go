package netx

import (
	"fmt"
	"net"
	"sync"
)

type Wrapper func(net.Conn) (net.Conn, error)

type Driver interface {
	Setup(params map[string]string, listener bool) (Wrapper, error)
}

// FuncDriver is a helper to create a Driver from a function
type FuncDriver func(params map[string]string, listener bool) (Wrapper, error)

func (f FuncDriver) Setup(params map[string]string, listener bool) (Wrapper, error) {
	return f(params, listener)
}

var (
	driversMu sync.RWMutex
	drivers   = make(map[string]Driver)
)

func Register(name string, d Driver) {
	driversMu.Lock()
	defer driversMu.Unlock()
	if d == nil {
		panic("uri: Register driver is nil")
	}
	if _, dup := drivers[name]; dup {
		panic("uri: Register called twice for driver " + name)
	}
	drivers[name] = d
}

func GetDriver(name string) (Driver, error) {
	driversMu.RLock()
	defer driversMu.RUnlock()
	d, ok := drivers[name]
	if !ok {
		return nil, fmt.Errorf("uri: unknown driver %q", name)
	}
	return d, nil
}
