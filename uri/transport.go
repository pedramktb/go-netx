package uri

import (
	"fmt"
	"strings"
)

type Transport string

const (
	TransportTCP Transport = "tcp"
	TransportUDP Transport = "udp"
)

func (t Transport) String() string {
	return string(t)
}

func (t Transport) MarshalText() ([]byte, error) {
	return []byte(t.String()), nil
}

func (t *Transport) UnmarshalText(text []byte) error {
	str := strings.ToLower(strings.TrimSpace(string(text)))
	switch Transport(str) {
	case TransportTCP, TransportUDP:
		*t = Transport(str)
		return nil
	default:
		return fmt.Errorf("uri: unknown transport %q", str)
	}
}
