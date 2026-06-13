package dtls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	netx "github.com/pedramktb/go-netx"
	"github.com/pion/dtls/v3"
)

// genECCertPEM produces an ECDSA P-256 self-signed leaf as PEM cert + SEC1 key —
// the exact shape the Flutter client emits (EC PRIVATE KEY block) and that the
// server's tls.X509KeyPair must accept.
func genECCertPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func TestDTLSParamParsing(t *testing.T) {
	cert, key := genECCertPEM(t)
	c := hex.EncodeToString(cert)
	k := hex.EncodeToString(key)

	tests := []struct {
		name    string
		uri     string
		server  bool
		wantErr bool
	}{
		{"server valid tuned", fmt.Sprintf("udp+dtls{cert=%s,key=%s,mtu=1200,flightinterval=300ms,nobackoff=true,skipcookie=true,resume=true}://127.0.0.1:0", c, k), true, false},
		{"client valid tuned", fmt.Sprintf("udp+dtls{cert=%s,mtu=1200,flightinterval=300ms,resume=true}://127.0.0.1:9000", c), false, false},
		{"client skipcookie rejected", fmt.Sprintf("udp+dtls{cert=%s,skipcookie=true}://127.0.0.1:9000", c), false, true},
		{"bad mtu low", fmt.Sprintf("udp+dtls{cert=%s,key=%s,mtu=10}://127.0.0.1:0", c, k), true, true},
		{"bad mtu high", fmt.Sprintf("udp+dtls{cert=%s,key=%s,mtu=9999}://127.0.0.1:0", c, k), true, true},
		{"bad flightinterval", fmt.Sprintf("udp+dtls{cert=%s,key=%s,flightinterval=nope}://127.0.0.1:0", c, k), true, true},
		{"unknown param", fmt.Sprintf("udp+dtls{cert=%s,key=%s,bogus=1}://127.0.0.1:0", c, k), true, true},
		{"legacy untuned still parses", fmt.Sprintf("udp+dtls{cert=%s,key=%s}://127.0.0.1:0", c, k), true, false},
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

// TestDTLSHandshakeTuned proves a real DTLS tunnel with the new tuning params
// (and an ECDSA cert) completes and echoes — i.e. the params are wired without
// breaking the handshake.
func TestDTLSHandshakeTuned(t *testing.T) {
	cert, key := genECCertPEM(t)
	c := hex.EncodeToString(cert)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// NOTE: cert-DTLS deliberately does NOT enable `resume` here. pion v3.1.2 has
	// a quirk where a full certificate handshake with SessionStore set on both
	// ends (separate stores, as in production's separate client/server processes)
	// fails to complete; resumption is therefore shipped on the PSK path only
	// (see dtlspsk_test.go), which is our fast scheme. mtu/flightinterval/
	// skipcookie are the cert-path tunables and work fine.
	var lu netx.ListenerURI
	if err := lu.UnmarshalText([]byte(fmt.Sprintf(
		"udp+dtls{cert=%s,key=%s,mtu=1200,flightinterval=200ms,skipcookie=true}://127.0.0.1:0",
		c, hex.EncodeToString(key)))); err != nil {
		t.Fatal(err)
	}
	ln, err := lu.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go echoLoop(ln)

	var du netx.DialerURI
	if err := du.UnmarshalText([]byte(fmt.Sprintf(
		"udp+dtls{cert=%s,mtu=1200,flightinterval=200ms}://%s", c, ln.Addr().String()))); err != nil {
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

func echoLoop(ln net.Listener) {
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
}

func TestMemSessionStore(t *testing.T) {
	s := &memSessionStore{m: make(map[string]dtls.Session), max: 2}

	s.Set([]byte("a"), dtls.Session{ID: []byte{1}, Secret: []byte{2}})
	got, _ := s.Get([]byte("a"))
	if len(got.ID) != 1 || got.ID[0] != 1 || len(got.Secret) != 1 || got.Secret[0] != 2 {
		t.Fatalf("get mismatch: %+v", got)
	}

	miss, _ := s.Get([]byte("missing"))
	if miss.ID != nil {
		t.Fatalf("expected zero session for missing key, got %+v", miss)
	}

	// Exceeding capacity evicts rather than growing unbounded.
	s.Set([]byte("b"), dtls.Session{ID: []byte{3}})
	s.Set([]byte("c"), dtls.Session{ID: []byte{4}})
	if len(s.m) > 2 {
		t.Fatalf("store exceeded max: %d", len(s.m))
	}

	s.Del([]byte("c"))
	if _, ok := s.m["c"]; ok {
		t.Fatalf("expected c deleted")
	}
}
