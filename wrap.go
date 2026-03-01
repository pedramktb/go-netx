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

// PipeType represents the type flowing through the wrapper pipeline.
type PipeType int

const (
	PipeTypeListener   PipeType = iota // net.Listener
	PipeTypeDialer                     // Dialer
	PipeTypeConn                       // net.Conn
	PipeTypeTaggedConn                 // TaggedConn
)

func (p PipeType) String() string {
	switch p {
	case PipeTypeListener:
		return "Listener"
	case PipeTypeDialer:
		return "Dialer"
	case PipeTypeConn:
		return "Conn"
	case PipeTypeTaggedConn:
		return "TaggedConn"
	default:
		return "Unknown"
	}
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

	// Validate wrapper chain compatibility
	currentType := PipeTypeDialer
	if server {
		currentType = PipeTypeListener
	}
	for i, w := range *ws {
		outputType, ok := w.OutputFor(currentType)
		if !ok {
			return fmt.Errorf("wrapper %q at position %d: incompatible input type %s, expected one of %v", w.String(), i, currentType.String(), w.InputTypes())
		}
		currentType = outputType
	}

	if server && currentType != PipeTypeListener {
		return fmt.Errorf("invalid wrapper chain: final output type %s is not a Listener for server scheme", currentType.String())
	}

	if !server && currentType != PipeTypeDialer {
		return fmt.Errorf("invalid wrapper chain: final output type %s is not a Dialer for client scheme", currentType.String())
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
	ListenerToTagged   func(net.Listener) (TaggedConn, error)

	DialerToDialer func(Dialer) (Dialer, error)
	DialerToConn   func(Dialer) (net.Conn, error)
	DialerToTagged func(Dialer) (TaggedConn, error)

	ConnToConn     func(net.Conn) (net.Conn, error)
	ConnToTagged   func(net.Conn) (TaggedConn, error)
	ConnToListener func(net.Conn) (net.Listener, error)
	ConnToDialer   func(net.Conn) (Dialer, error)

	TaggedToTagged   func(TaggedConn) (TaggedConn, error)
	TaggedToConn     func(TaggedConn) (net.Conn, error)
	TaggedToListener func(TaggedConn) (net.Listener, error)
	TaggedToDialer   func(TaggedConn) (Dialer, error)
}

func (w Wrapper) InputTypes() []PipeType {
	var types []PipeType
	if w.ListenerToListener != nil || w.ListenerToConn != nil || w.ListenerToTagged != nil {
		types = append(types, PipeTypeListener)
	}
	if w.DialerToDialer != nil || w.DialerToConn != nil || w.DialerToTagged != nil {
		types = append(types, PipeTypeDialer)
	}
	if w.ConnToConn != nil || w.ConnToTagged != nil || w.ConnToListener != nil || w.ConnToDialer != nil {
		types = append(types, PipeTypeConn)
	}
	if w.TaggedToTagged != nil || w.TaggedToConn != nil || w.TaggedToListener != nil || w.TaggedToDialer != nil {
		types = append(types, PipeTypeTaggedConn)
	}
	return types
}

// OutputFor returns the output PipeType when this wrapper receives the given input type.
// Returns (outputType, true) if the wrapper supports the input type, or (0, false) otherwise.
func (w Wrapper) OutputFor(input PipeType) (PipeType, bool) {
	switch input {
	case PipeTypeListener:
		switch {
		case w.ListenerToListener != nil:
			return PipeTypeListener, true
		case w.ListenerToConn != nil:
			return PipeTypeConn, true
		case w.ListenerToTagged != nil:
			return PipeTypeTaggedConn, true
		}
	case PipeTypeDialer:
		switch {
		case w.DialerToDialer != nil:
			return PipeTypeDialer, true
		case w.DialerToConn != nil:
			return PipeTypeConn, true
		case w.DialerToTagged != nil:
			return PipeTypeTaggedConn, true
		}
	case PipeTypeConn:
		switch {
		case w.ConnToConn != nil:
			return PipeTypeConn, true
		case w.ConnToTagged != nil:
			return PipeTypeTaggedConn, true
		case w.ConnToListener != nil:
			return PipeTypeListener, true
		case w.ConnToDialer != nil:
			return PipeTypeDialer, true
		}
	case PipeTypeTaggedConn:
		switch {
		case w.TaggedToTagged != nil:
			return PipeTypeTaggedConn, true
		case w.TaggedToConn != nil:
			return PipeTypeConn, true
		case w.TaggedToListener != nil:
			return PipeTypeListener, true
		case w.TaggedToDialer != nil:
			return PipeTypeDialer, true
		}
	}
	return 0, false
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
		case w.ListenerToTagged != nil:
			return w.ListenerToTagged(v)
		}
	case Dialer:
		switch {
		case w.DialerToDialer != nil:
			return w.DialerToDialer(v)
		case w.DialerToConn != nil:
			return w.DialerToConn(v)
		case w.DialerToTagged != nil:
			return w.DialerToTagged(v)
		}
	case net.Conn:
		switch {
		case w.ConnToConn != nil:
			return w.ConnToConn(v)
		case w.ConnToTagged != nil:
			return w.ConnToTagged(v)
		case w.ConnToListener != nil:
			return w.ConnToListener(v)
		case w.ConnToDialer != nil:
			return w.ConnToDialer(v)
		}
	case TaggedConn:
		switch {
		case w.TaggedToTagged != nil:
			return w.TaggedToTagged(v)
		case w.TaggedToConn != nil:
			return w.TaggedToConn(v)
		case w.TaggedToListener != nil:
			return w.TaggedToListener(v)
		case w.TaggedToDialer != nil:
			return w.TaggedToDialer(v)
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
	wc, err := l.wrapConn(c)
	if err != nil {
		c.Close()
		return nil, err
	}
	return wc, nil
}

// ConnWrapListener adapts a ConnToConn wrapper to a ListenerToListener wrapper.
func ConnWrapListener(ln net.Listener, wrapConn func(net.Conn) (net.Conn, error)) (net.Listener, error) {
	return &connWrappedListener{ln, wrapConn}, nil
}

// ConnWrapDialer adapts a ConnToConn wrapper to a DialerToDialer wrapper.
func ConnWrapDialer(dial Dialer, wrapConn func(net.Conn) (net.Conn, error)) (Dialer, error) {
	return func() (net.Conn, error) {
		c, err := dial()
		if err != nil {
			return nil, err
		}
		wc, err := wrapConn(c)
		if err != nil {
			c.Close()
			return nil, err
		}
		return wc, nil
	}, nil
}
