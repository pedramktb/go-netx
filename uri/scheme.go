package uri

import (
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/pedramktb/go-netx"
)

var schemeBlockRegex = regexp.MustCompile(`^\[((?:[^\[\]]|\[[^\[\]]*\])*)\]`)

type ServerScheme struct {
	Scheme
}

func (s *Scheme) WrapListener(ln net.Listener) (net.Listener, error) {
	if len(s.TransportLayers) == 0 {
		return ln, nil
	}

	return netx.NewServerMux(ln, len(s.ID), netx.WithServerMuxWrapper(s.TransportLayers.WrapConn)), nil
}

func (s *ServerScheme) UnmarshalText(text []byte) error {
	return s.Scheme.UnmarshalText(text, true)
}

type ClientScheme struct {
	Scheme
}

func (s *Scheme) WrapDialer(dial func() (net.Conn, error)) (func() (net.Conn, error), error) {
	if s.TransportLayers == nil {
		return dial, nil
	}

	return netx.NewClientMux(dial, s.ID, netx.WithClientMuxWrapper(s.TransportLayers.WrapConn)), nil
}

func (c *ClientScheme) UnmarshalText(text []byte) error {
	return c.Scheme.UnmarshalText(text, false)
}

type Scheme struct {
	Transport       `json:"transport"`
	TransportLayers Layers `json:"transport_layers"`
	ConnLayers      Layers `json:"conn_layers"`
	ID              []byte `json:"id,omitempty"`
}

func (s Scheme) WrapConn(conn net.Conn) (net.Conn, error) {
	return s.ConnLayers.WrapConn(conn)
}

func (s Scheme) String() string {
	str := s.Transport.String()
	if s.TransportLayers != nil {
		for _, l := range s.TransportLayers {
			str += "+" + l.String()
		}
		str = "[" + str + "]"
		if len(s.ID) > 0 {
			str += "[id=" + hex.EncodeToString(s.ID[:]) + "]"
		}
	}

	for _, l := range s.ConnLayers {
		str += "+" + l.String()
	}
	return str
}

func (s Scheme) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}

func (s *Scheme) UnmarshalText(text []byte, server bool) error {
	str := string(text)

	transportParsed := false

	// Try parsing [Transport+Layers]
	if indices := schemeBlockRegex.FindStringSubmatchIndex(str); indices != nil {
		content := str[indices[2]:indices[3]]
		str = str[indices[1]:]

		// Inside [] it is Transport+Layers
		parts := strings.SplitN(content, "+", 2)
		if err := s.Transport.UnmarshalText([]byte(parts[0])); err != nil {
			return err
		}
		if len(parts) > 1 {
			if err := s.TransportLayers.UnmarshalText([]byte(parts[1]), server); err != nil {
				return err
			}
		}
		transportParsed = true

		// Params [id=...]
		if indices := schemeBlockRegex.FindStringSubmatchIndex(str); indices != nil {
			content := str[indices[2]:indices[3]]
			str = str[indices[1]:]

			// Parse ID params
			for pair := range strings.SplitSeq(content, ",") {
				kv := strings.SplitN(pair, "=", 2)
				if len(kv) != 2 {
					return fmt.Errorf("uri: invalid parameter %q", pair)
				}
				key := strings.ToLower(strings.TrimSpace(kv[0]))
				value := strings.TrimSpace(kv[1])
				if key == "id" {
					id, err := hex.DecodeString(value)
					if err != nil {
						return fmt.Errorf("uri: invalid id encoding: %w", err)
					}
					s.ID = id
				}
			}
		}
	}

	str = strings.TrimPrefix(str, "+")

	if !transportParsed {
		if str == "" {
			return fmt.Errorf("uri: empty scheme")
		}
		parts := strings.SplitN(str, "+", 2)
		if err := s.Transport.UnmarshalText([]byte(parts[0])); err != nil {
			return err
		}
		if len(parts) > 1 {
			str = parts[1]
		} else {
			str = ""
		}
	}

	if err := s.ConnLayers.UnmarshalText([]byte(str), server); err != nil {
		return err
	}

	return nil
}
