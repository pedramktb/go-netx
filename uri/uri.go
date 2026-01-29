package uri

import (
	"fmt"
	"strings"
)

type ServerURI struct {
	URI
}

func (u *ServerURI) UnmarshalText(text []byte) error {
	return u.URI.UnmarshalText(text, true)
}

type ClientURI struct {
	URI
}

func (u *ClientURI) UnmarshalText(text []byte) error {
	return u.URI.UnmarshalText(text, false)
}

type URI struct {
	Scheme `json:"scheme"`
	Addr   string `json:"addr"`
}

func (u URI) String() string {
	return u.Scheme.String() + "://" + u.Addr
}

func (u URI) MarshalText() ([]byte, error) {
	return []byte(u.String()), nil
}

func (u *URI) UnmarshalText(text []byte, server bool) error {
	str := string(text)
	parts := strings.SplitN(str, "://", 2)
	if len(parts) < 2 {
		return fmt.Errorf("uri: missing scheme delimiter in %q", str)
	}

	u.Addr = strings.TrimSpace(parts[1])
	if u.Addr == "" {
		return fmt.Errorf("uri: empty address in %q", str)
	}

	return u.Scheme.UnmarshalText([]byte(parts[0]), server)
}
