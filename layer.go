package netx

import (
	"fmt"
	"net"
	"strings"
)

type ServerLayers struct {
	Layers
}

func (ls *ServerLayers) UnmarshalText(text []byte) error {
	return ls.Layers.UnmarshalText(text, true)
}

type ClientLayers struct {
	Layers
}

func (ls *ClientLayers) UnmarshalText(text []byte) error {
	return ls.Layers.UnmarshalText(text, false)
}

type Layers []Layer

func (ls Layers) Wrap(conn net.Conn) (net.Conn, error) {
	var err error
	for _, l := range ls {
		conn, err = l.Wrap(conn)
		if err != nil {
			return nil, fmt.Errorf("wrap %q: %w", l.String(), err)
		}
	}
	return conn, nil
}

func (ls Layers) String() string {
	strs := make([]string, len(ls))
	for i, l := range ls {
		strs[i] = l.String()
	}
	return strings.Join(strs, "+")
}

func (ls Layers) MarshalText() ([]byte, error) {
	return []byte(ls.String()), nil
}

func (ls *Layers) UnmarshalText(text []byte, server bool) error {
	parts := strings.Split(string(text), "+")
	*ls = make([]Layer, len(parts))
	for i := range parts {
		if err := (*ls)[i].UnmarshalText([]byte(parts[i]), server); err != nil {
			return err
		}
	}

	return nil
}

type ServerLayer struct {
	Layer
}

func (l *ServerLayer) UnmarshalText(text []byte) error {
	return l.Layer.UnmarshalText(text, true)
}

type ClientLayer struct {
	Layer
}

func (l *ClientLayer) UnmarshalText(text []byte) error {
	return l.Layer.UnmarshalText(text, false)
}

type Layer struct {
	Prot   string
	Params map[string]string
	wrap   Wrapper
}

func (l Layer) Wrap(conn net.Conn) (net.Conn, error) {
	if l.wrap == nil {
		return conn, nil
	}
	return l.wrap(conn)
}

func (l Layer) String() string {
	pairs := make([]string, 0, len(l.Params))
	for k, v := range l.Params {
		pairs = append(pairs, k+"="+v)
	}
	if len(pairs) > 0 {
		return fmt.Sprintf("%s[%s]", l.Prot, strings.Join(pairs, ","))
	}
	return l.Prot
}

func (l Layer) MarshalText() ([]byte, error) {
	return []byte(l.String()), nil
}

func (l *Layer) UnmarshalText(text []byte, listener bool) error {
	str := string(text)

	l.Prot = strings.ToLower(strings.TrimSpace(str))
	l.Params = map[string]string{}
	if idx := strings.Index(str, "["); idx != -1 {
		if !strings.HasSuffix(str, "]") {
			return fmt.Errorf("uri: missing ']' in layer %q", str)
		}
		l.Prot = strings.ToLower(strings.TrimSpace(str[:idx]))
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
			l.Params[key] = value
		}
	}

	driver, err := GetDriver(l.Prot)
	if err != nil {
		return fmt.Errorf("uri: %w", err)
	}
	l.wrap, err = driver.Setup(l.Params, listener)
	if err != nil {
		return fmt.Errorf("uri: setup driver %s: %w", l.Prot, err)
	}

	return nil
}
