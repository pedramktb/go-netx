package uri

import (
	"fmt"
	"strings"
)

type Scheme struct {
	Listener bool
	Transport
	Layers Layers
}

func (s Scheme) String() string {
	str := s.Transport.String()
	for _, l := range s.Layers.Layers {
		str += "+" + l.String()
	}
	return str
}

func (s Scheme) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}

func (s *Scheme) UnmarshalText(text []byte) error {
	parts := strings.SplitN(string(text), "+", 2)
	if len(parts) == 0 {
		return fmt.Errorf("uri: empty scheme")
	}

	if err := s.Transport.UnmarshalText([]byte(parts[0])); err != nil {
		return err
	}

	if len(parts) == 1 {
		return nil
	}

	s.Layers.Listener = s.Listener

	return s.Layers.UnmarshalText([]byte(parts[1]))
}
