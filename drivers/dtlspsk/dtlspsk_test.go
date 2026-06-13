package dtlspsk

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	netx "github.com/pedramktb/go-netx"
	"github.com/pion/dtls/v3"
)

const testPSK = "00112233445566778899aabbccddeeff"

func TestDTLSPSKParamParsing(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		server  bool
		wantErr bool
	}{
		{"server valid tuned", fmt.Sprintf("udp+dtlspsk{key=%s,identity=srv,mtu=1200,flightinterval=300ms,nobackoff=true,skipcookie=true,resume=true}://127.0.0.1:0", testPSK), true, false},
		{"client valid tuned", fmt.Sprintf("udp+dtlspsk{key=%s,identity=cli,mtu=1200,flightinterval=300ms,resume=true}://127.0.0.1:9000", testPSK), false, false},
		{"client requires identity", fmt.Sprintf("udp+dtlspsk{key=%s}://127.0.0.1:9000", testPSK), false, true},
		{"client skipcookie rejected", fmt.Sprintf("udp+dtlspsk{key=%s,identity=cli,skipcookie=true}://127.0.0.1:9000", testPSK), false, true},
		{"missing key", "udp+dtlspsk{identity=cli}://127.0.0.1:0", true, true},
		{"bad mtu", fmt.Sprintf("udp+dtlspsk{key=%s,identity=srv,mtu=5}://127.0.0.1:0", testPSK), true, true},
		{"unknown param", fmt.Sprintf("udp+dtlspsk{key=%s,identity=srv,bogus=1}://127.0.0.1:0", testPSK), true, true},
		{"legacy untuned still parses", fmt.Sprintf("udp+dtlspsk{key=%s,identity=cli}://127.0.0.1:9000", testPSK), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.server {
				var u netx.ListenerURI
				err = u.UnmarshalText([]byte(tt.uri))
			} else {
				var u netx.DialerURI
				err = u.UnmarshalText([]byte(tt.uri))
			}
			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestDTLSPSKHandshakeTuned proves a real PSK DTLS tunnel with skipcookie +
// resume + tuned timers completes and echoes (no cert flight).
func TestDTLSPSKHandshakeTuned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var lu netx.ListenerURI
	if err := lu.UnmarshalText([]byte(fmt.Sprintf(
		"udp+dtlspsk{key=%s,identity=srv,mtu=1200,flightinterval=200ms,skipcookie=true,resume=true}://127.0.0.1:0",
		testPSK))); err != nil {
		t.Fatal(err)
	}
	ln, err := lu.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1500)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					if _, err := c.Write(buf[:n]); err != nil {
						return
					}
				}
			}(c)
		}
	}()

	var du netx.DialerURI
	if err := du.UnmarshalText([]byte(fmt.Sprintf(
		"udp+dtlspsk{key=%s,identity=cli,mtu=1200,flightinterval=200ms,resume=true}://%s",
		testPSK, ln.Addr().String()))); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		conn, err := du.Dial(ctx)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		if _, err := conn.Write([]byte("ping")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		buf := make([]byte, 64)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if string(buf[:n]) != "ping" {
			t.Fatalf("echo %d mismatch: %q", i, buf[:n])
		}
		_ = conn.Close()
	}
}

func TestMemSessionStore(t *testing.T) {
	s := &memSessionStore{m: make(map[string]dtls.Session), max: 2}

	s.Set([]byte("a"), dtls.Session{ID: []byte{1}, Secret: []byte{2}})
	got, _ := s.Get([]byte("a"))
	if len(got.ID) != 1 || got.ID[0] != 1 {
		t.Fatalf("get mismatch: %+v", got)
	}
	if miss, _ := s.Get([]byte("missing")); miss.ID != nil {
		t.Fatalf("expected zero session for missing key")
	}
	s.Set([]byte("b"), dtls.Session{ID: []byte{3}})
	s.Set([]byte("c"), dtls.Session{ID: []byte{4}})
	if len(s.m) > 2 {
		t.Fatalf("store exceeded max: %d", len(s.m))
	}
}
