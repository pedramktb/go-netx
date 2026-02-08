package netx

import (
	"fmt"
	"net"
	"strings"
)

const (
	BaseTransportTCP  = "tcp"  // ip:6
	BaseTransportUDP  = "udp"  // ip:17
	BaseTransportICMP = "icmp" // ip:1
)

type ListenerTransport struct {
	Transport
}

func (t *ListenerTransport) UnmarshalText(text []byte) error {
	return t.Transport.UnmarshalText(text, true)
}

type DialerTransport struct {
	Transport
}

func (t *DialerTransport) UnmarshalText(text []byte) error {
	return t.Transport.UnmarshalText(text, false)
}

type Transport struct {
	Base string
	Layers
	Params map[string]string
}

func (t *Transport) WrapListener(ln net.Listener) (net.Listener, error) {
	if len(t.Layers) == 0 {
		return ln, nil
	}
	return nil, nil
}
func (t *Transport) WrapDialer(dial func() (net.Conn, error)) (func() (net.Conn, error), error) {
	if len(t.Layers) == 0 {
		return dial, nil
	}
	return nil, nil
}

func (t Transport) String() string {
	str := t.Base
	if len(t.Layers) > 0 || len(t.Params) > 0 {
		str += "{"
	}
	if len(t.Layers) > 0 {
		str += "layers=" + t.Layers.String()
	}
	pairs := make([]string, 0, len(t.Params))
	for k, v := range t.Params {
		pairs = append(pairs, k+"="+v)
	}
	if len(pairs) > 0 && len(t.Layers) > 0 {
		str += ","
	}
	str += strings.Join(pairs, ",")
	if len(t.Layers) > 0 || len(t.Params) > 0 {
		str += "}"
	}
	return str
}

func (t Transport) MarshalText() ([]byte, error) {
	return []byte(t.String()), nil
}

// Either "udp", "tcp", or "icmp" or with params like "tcp{param=value}". 1 Special param is layers
func (t *Transport) UnmarshalText(text []byte, listener bool) error {
	parts := strings.SplitN(string(text), "{", 2)
	if len(parts) == 0 {
		return fmt.Errorf("uri: empty transport")
	}
	t.Base = parts[0]
	if len(parts) == 1 {
		return nil
	}
	if !strings.HasSuffix(parts[1], "}") {
		return fmt.Errorf("uri: invalid transport params format")
	}
	parts[1] = strings.TrimSuffix(parts[1], "}")
	t.Params = make(map[string]string)
	for _, param := range strings.Split(parts[1], ",") {
		kv := strings.SplitN(param, "=", 2)
		if len(kv) != 2 {
			return fmt.Errorf("uri: invalid transport param format")
		}
		if kv[0] == "layers" {
			if err := t.Layers.UnmarshalText([]byte(kv[1]), listener); err != nil {
				return fmt.Errorf("error parsing transport layers: %w", err)
			}
		} else {
			t.Params[kv[0]] = kv[1]
		}
	}
	return nil
}
