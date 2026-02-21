package netx

import (
	"fmt"
	"net"
	"strings"
)

type ServerWrappers struct {
	Wrappers
}

func (ls *ServerWrappers) UnmarshalText(text []byte) error {
	return ls.Wrappers.UnmarshalText(text, true)
}

type ClientWrappers struct {
	Wrappers
}

func (ls *ClientWrappers) UnmarshalText(text []byte) error {
	return ls.Wrappers.UnmarshalText(text, false)
}

type Wrappers []Wrapper

func (ws Wrappers) Apply(conn any) (any, error) {
	var err error
	for _, w := range ws {
		conn, err = w.Apply(conn)
		if err != nil {
			return nil, fmt.Errorf("wrap %q: %w", w.String(), err)
		}
	}
	return conn, nil
}

func (ws Wrappers) String() string {
	strs := make([]string, len(ws))
	for i, w := range ws {
		strs[i] = w.String()
	}
	return strings.Join(strs, "+")
}

func (ws Wrappers) MarshalText() ([]byte, error) {
	return []byte(ws.String()), nil
}

func (ws *Wrappers) UnmarshalText(text []byte, server bool) error {
	parts := strings.Split(string(text), "+")
	*ws = make([]Wrapper, len(parts))
	for i := range parts {
		if err := (*ws)[i].UnmarshalText([]byte(parts[i]), server); err != nil {
			return err
		}
	}

	return nil
}

type ListenerWrapper struct {
	Wrapper
}

func (ls *ListenerWrapper) UnmarshalText(text []byte) error {
	return ls.Wrapper.UnmarshalText(text, true)
}

type DialerWrapper struct {
	Wrapper
}

func (ls *DialerWrapper) UnmarshalText(text []byte) error {
	return ls.Wrapper.UnmarshalText(text, false)
}

// Wrapper represents a transformation in the layer pipeline.
// Maximum one function field per input type (Listener, Dialer, Conn, TaggedConn) should be set.
// The Apply method applies the wrapper to a value, transforming it according to the set function field.
type Wrapper struct {
	Name     string
	Params   map[string]string
	Listener bool

	ListenerToListener func(net.Listener) (net.Listener, error)
	ListenerToConn     func(net.Listener) (net.Conn, error)

	DialerToDialer func(func() (net.Conn, error)) (func() (net.Conn, error), error)
	DialerToConn   func(func() (net.Conn, error)) (net.Conn, error)

	ConnToConn     func(net.Conn) (net.Conn, error)
	ConnToTagged   func(net.Conn) (TaggedConn, error)
	ConnToListener func(net.Conn) (net.Listener, error)

	TaggedToTagged   func(TaggedConn) (TaggedConn, error)
	TaggedToListener func(TaggedConn) (net.Listener, error)
}

// Apply transforms the pipeline value through this wrapper.
// The input must match the expected type of the set function field.
// Returns the transformed value or an error if the type doesn't match.
func (w Wrapper) Apply(v any) (any, error) {
	switch v := v.(type) {
	case net.Listener:
		switch {
		case w.ListenerToListener != nil:
			return w.ListenerToListener(v)
		case w.ListenerToConn != nil:
			return w.ListenerToConn(v)
		}
	case func() (net.Conn, error):
		switch {
		case w.DialerToDialer != nil:
			return w.DialerToDialer(v)
		case w.DialerToConn != nil:
			return w.DialerToConn(v)
		}
	case net.Conn:
		if w.ConnToConn != nil {
			return w.ConnToConn(v)
		}
		if w.ConnToTagged != nil {
			return w.ConnToTagged(v)
		}
		if w.ConnToListener != nil {
			return w.ConnToListener(v)
		}
	case TaggedConn:
		if w.TaggedToTagged != nil {
			return w.TaggedToTagged(v)
		}
		if w.TaggedToListener != nil {
			return w.TaggedToListener(v)
		}
	}
	return nil, fmt.Errorf("wrapper %q: incompatible type %T", w.Name, v)
}

func (w Wrapper) String() string {
	pairs := make([]string, 0, len(w.Params))
	for k, v := range w.Params {
		pairs = append(pairs, k+"="+v)
	}
	if len(pairs) > 0 {
		return fmt.Sprintf("%s{%s}", w.Name, strings.Join(pairs, ","))
	}
	return w.Name
}

func (w Wrapper) MarshalText() ([]byte, error) {
	return []byte(w.String()), nil
}

func (w *Wrapper) UnmarshalText(text []byte, listener bool) error {
	str := string(text)

	w.Name = strings.ToLower(strings.TrimSpace(str))
	w.Params = map[string]string{}
	if idx := strings.Index(str, "{"); idx != -1 {
		if !strings.HasSuffix(str, "}") {
			return fmt.Errorf("uri: missing '}' in layer %q", str)
		}
		w.Name = strings.ToLower(strings.TrimSpace(str[:idx]))
		for pair := range strings.SplitSeq(str[idx+1:len(str)-1], ",") {
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) != 2 {
				return fmt.Errorf("uri: invalid parameter %q", pair)
			}
			key := strings.ToLower(strings.TrimSpace(kv[0]))
			value := strings.TrimSpace(kv[1])
			if key == "" {
				return fmt.Errorf("uri: empty parameter key")
			}
			w.Params[key] = value
		}
	}

	driver, err := GetDriver(w.Name)
	if err != nil {
		return fmt.Errorf("uri: %w", err)
	}
	*w, err = driver(w.Params, listener)
	if err != nil {
		return fmt.Errorf("uri: setup driver %s: %w", w.Name, err)
	}

	return nil
}

type connWrappedListener struct {
	net.Listener
	wrapConn func(net.Conn) (net.Conn, error)
}

func (l *connWrappedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	c, err = l.wrapConn(c)
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// ConnWrapListener adapts a ConnToConn wrapper to a ListenerToListener wrapper.
func ConnWrapListener(ln net.Listener, wrapConn func(net.Conn) (net.Conn, error)) (net.Listener, error) {
	return &connWrappedListener{ln, wrapConn}, nil
}

// ConnWrapDialer adapts a ConnToConn wrapper to a DialerToDialer wrapper.
func ConnWrapDialer(dial func() (net.Conn, error), wrapConn func(net.Conn) (net.Conn, error)) (func() (net.Conn, error), error) {
	return func() (net.Conn, error) {
		c, err := dial()
		if err != nil {
			return nil, err
		}
		c, err = wrapConn(c)
		if err != nil {
			c.Close()
			return nil, err
		}
		return c, nil
	}, nil
}
