package uri

import (
	"fmt"
	"strings"
)

type URI struct {
	// This flag must be set if the URI is being applied to a listener (server side)
	// The parser takes this into account when validating parameters
	Listener bool
	Scheme
	Addr string
}

func (u URI) String() string {
	return u.Scheme.String() + "://" + u.Addr
}

func (u URI) MarshalText() ([]byte, error) {
	return []byte(u.String()), nil
}

func (u *URI) UnmarshalText(text []byte) error {
	str := string(text)
	parts := strings.SplitN(str, "://", 2)
	if len(parts) < 2 {
		return fmt.Errorf("uri: missing scheme delimiter in %q", str)
	}

	u.Addr = strings.TrimSpace(parts[1])
	if u.Addr == "" {
		return fmt.Errorf("uri: empty address in %q", str)
	}

	u.Scheme.Listener = u.Listener

	return u.Scheme.UnmarshalText([]byte(parts[0]))
}
