package netx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

type ListenerScheme struct {
	Scheme
}

func (s ListenerScheme) Listen(ctx context.Context, addr string, opts ...ListenOption) (net.Listener, error) {
	l, err := Listen(ctx, s.Transport.String(), addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("error listening on %s://%s: %w", s.Transport.String(), addr, err)
	}
	wl, err := s.wrappers.Apply(l)
	if err != nil {
		return nil, fmt.Errorf("error upgrading to %s://%s: %w", s.String(), addr, err)
	}
	if l, ok := wl.(net.Listener); ok {
		return l, nil
	}
	return nil, fmt.Errorf("error upgrading to %s://%s: %w", s.String(), addr, errors.New("wrapper(s) did not produce net.Listener"))
}

func (s *ListenerScheme) UnmarshalText(text []byte) error {
	return s.Scheme.UnmarshalText(text, true)
}

type DialerScheme struct {
	Scheme
}

func (c DialerScheme) Dial(ctx context.Context, addr string, opts ...DialOption) (net.Conn, error) {
	dial := func() (net.Conn, error) {
		return Dial(ctx, c.Transport.String(), addr, opts...)
	}
	wdial, err := c.wrappers.Apply(dial)
	if err != nil {
		return nil, fmt.Errorf("error upgrading to %s://%s: %w", c.String(), addr, err)
	}
	if dial, ok := wdial.(Dialer); ok {
		return dial()
	}
	return nil, fmt.Errorf("error upgrading to %s://%s: %w", c.String(), addr, errors.New("wrapper(s) did not produce dial function"))
}

func (c *DialerScheme) UnmarshalText(text []byte) error {
	return c.Scheme.UnmarshalText(text, false)
}

type Scheme struct {
	Transport
	wrappers Wrappers
}

func (s Scheme) String() string {
	str := s.Transport.String()
	if len(s.wrappers) > 0 {
		str += "+" + s.wrappers.String()
	}
	return str
}

func (s Scheme) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}

func (s *Scheme) UnmarshalText(text []byte, listener bool) error {
	parts := strings.SplitN(string(text), "+", 2)
	if len(parts) == 0 {
		return fmt.Errorf("uri: empty scheme")
	}
	if err := s.Transport.UnmarshalText([]byte(parts[0]), listener); err != nil {
		return err
	}
	if len(parts) == 1 {
		return nil
	}
	return s.wrappers.UnmarshalText([]byte(parts[1]), listener)
}
