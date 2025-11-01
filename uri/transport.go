package uri

import (
	"fmt"
	"strings"
)

type Transport string

const (
	TransportTCP  Transport = "tcp"
	TransportUDP  Transport = "udp"
	TransportICMP Transport = "icmp"
)

func (t Transport) String() string {
	return string(t)
}

func (t Transport) MarshalText() ([]byte, error) {
	return []byte(t.String()), nil
}

func (t *Transport) UnmarshalText(text []byte) error {
	*t = Transport(strings.ToLower(strings.TrimSpace(string(text))))
	switch *t {
	case TransportTCP, TransportUDP, TransportICMP:
		return nil
	default:
		return fmt.Errorf("uri: unknown transport %q", string(text))
	}
}
