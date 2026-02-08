package netx

import (
	"fmt"
	"net"
	"strings"
)

type ListenerScheme struct {
	Scheme
}

func (s *ListenerScheme) UnmarshalText(text []byte) error {
	return s.Scheme.UnmarshalText(text, true)
}

type DialerScheme struct {
	Scheme
}

func (c *DialerScheme) UnmarshalText(text []byte) error {
	return c.Scheme.UnmarshalText(text, false)
}

type Scheme struct {
	Transport
	Layers
}

func (s Scheme) Wrap(conn net.Conn) (net.Conn, error) {
	return s.Layers.Wrap(conn)
}

func (s Scheme) String() string {
	str := s.Transport.String()
	if len(s.Layers) > 0 {
		str += "+" + s.Layers.String()
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
	return s.Layers.UnmarshalText([]byte(parts[1]), listener)
}
