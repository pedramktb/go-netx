package netx

import (
	"fmt"
	"sync"
)

type Driver func(params map[string]string, listener bool) (Wrapper, error)

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
