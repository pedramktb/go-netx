package netx_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"

	netx "github.com/pedramktb/go-netx"
)

// chanListener is an in-memory net.Listener that yields connections from a channel.
type chanListener struct {
	ch   chan net.Conn
	done chan struct{}
	addr net.Addr
}

func newChanListener() *chanListener {
	return &chanListener{
		ch:   make(chan net.Conn, 1),
		done: make(chan struct{}),
		addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0},
	}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error   { close(l.done); return nil }
func (l *chanListener) Addr() net.Addr { return l.addr }

// newUDPPair creates two UDP conns connected to each other on localhost.
func newUDPPair(t *testing.T) (*net.UDPConn, *net.UDPConn) {
	t.Helper()
	// First, listen on two ephemeral ports to learn addresses
	la, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP a: %v", err)
	}
	lb, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP b: %v", err)
	}
	aAddr := la.LocalAddr().(*net.UDPAddr)
	bAddr := lb.LocalAddr().(*net.UDPAddr)
	// Close listeners so we can bind same local ports for connected sockets
	_ = la.Close()
	_ = lb.Close()

	// Create connected UDP endpoints using the discovered addresses
	a, err := net.DialUDP("udp", aAddr, bAddr)
	if err != nil {
		t.Fatalf("DialUDP a->b: %v", err)
	}
	b, err := net.DialUDP("udp", bAddr, a.LocalAddr().(*net.UDPAddr))
	if err != nil {
		_ = a.Close()
		t.Fatalf("DialUDP b->a: %v", err)
	}
	_ = a.SetDeadline(time.Now().Add(3 * time.Second))
	_ = b.SetDeadline(time.Now().Add(3 * time.Second))
	return a, b
}

func TestE2E_UDP_over_TCP_TunMasters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	logger := &memLogger{}

	// UDP peers: clientPeer <-> clientTunUDP, serverTunUDP <-> serverPeer
	clientPeer, clientTunUDP := newUDPPair(t)
	t.Cleanup(func() { _ = clientPeer.Close(); _ = clientTunUDP.Close() })
	serverTunUDP, serverPeer := newUDPPair(t)
	t.Cleanup(func() { _ = serverTunUDP.Close(); _ = serverPeer.Close() })

	// Tunnel between TunMasters (use in-memory pipe)
	tmClientSide, tmServerSide := net.Pipe()
	t.Cleanup(func() { _ = tmClientSide.Close(); _ = tmServerSide.Close() })

	// In-memory listeners to feed each TunMaster one connection endpoint
	lClient := newChanListener()
	lServer := newChanListener()

	// Client TunMaster: bridge UDP<->framed stream (towards server)
	var tmClient netx.TunMaster[string]
	tmClient.Logger = logger
	tmClient.SetRoute("route", func(connCtx context.Context, conn net.Conn) (bool, context.Context, netx.Tun) {
		// Wrap the stream side with framing to preserve UDP datagrams
		fc := netx.NewFramedConn(conn)
		return true, connCtx, netx.Tun{
			Logger:     logger,
			Conn:       fc,
			Peer:       clientTunUDP,
			BufferSize: 64 << 10, // 64KB to safely carry typical UDP datagrams in one read
		}
	})

	// Server TunMaster: bridge framed stream<->UDP (towards server UDP peer)
	var tmServer netx.TunMaster[string]
	tmServer.Logger = logger
	tmServer.SetRoute("route", func(connCtx context.Context, conn net.Conn) (bool, context.Context, netx.Tun) {
		fc := netx.NewFramedConn(conn)
		return true, connCtx, netx.Tun{
			Logger:     logger,
			Conn:       fc,
			Peer:       serverTunUDP,
			BufferSize: 64 << 10,
		}
	})

	// Start serving
	errChClient := make(chan error, 1)
	errChServer := make(chan error, 1)
	go func() { errChClient <- tmClient.Serve(ctx, lClient) }()
	go func() { errChServer <- tmServer.Serve(ctx, lServer) }()

	// Deliver the pipe endpoints as the accepted connections
	lClient.ch <- tmClientSide
	lServer.ch <- tmServerSide

	// Allow some time for tunnels to spin up
	time.Sleep(50 * time.Millisecond)

	// Datagrams from client -> server
	_ = clientPeer.SetWriteDeadline(time.Now().Add(2 * time.Second))
	msg1 := []byte("hello from client via UDP")
	if _, err := clientPeer.Write(msg1); err != nil {
		t.Fatalf("clientPeer write: %v", err)
	}
	_ = serverPeer.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	n, err := serverPeer.Read(buf)
	if err != nil {
		t.Fatalf("serverPeer read: %v", err)
	}
	if string(buf[:n]) != string(msg1) {
		t.Fatalf("serverPeer got %q want %q", string(buf[:n]), string(msg1))
	}

	// Datagrams from server -> client
	_ = serverPeer.SetWriteDeadline(time.Now().Add(2 * time.Second))
	msg2 := []byte("hello from server via UDP")
	if _, err := serverPeer.Write(msg2); err != nil {
		t.Fatalf("serverPeer write: %v", err)
	}
	_ = clientPeer.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = clientPeer.Read(buf)
	if err != nil {
		t.Fatalf("clientPeer read: %v", err)
	}
	if string(buf[:n]) != string(msg2) {
		t.Fatalf("clientPeer got %q want %q", string(buf[:n]), string(msg2))
	}

	// Shutdown both masters gracefully
	sdCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := tmClient.Shutdown(sdCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("tmClient Shutdown: %v", err)
	}
	if err := tmServer.Shutdown(sdCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("tmServer Shutdown: %v", err)
	}
}

// Test routing on a single server listener with two routes:
// - TLS route: expects a tls.Conn and forwards to server UDPTLS peer
// - Plain route: handles non-TLS and forwards to server UDPPlain peer
// Two client tunnels are created: one plain over framed stream, one framed over TLS.
func TestE2E_TunMasterRouting_PlainAndTLS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	logger := &memLogger{}

	// Server UDP peers (targets after decapsulation)
	serverTunUDPPlain, serverPeerPlain := newUDPPair(t)
	t.Cleanup(func() { _ = serverTunUDPPlain.Close(); _ = serverPeerPlain.Close() })
	serverTunUDPTLS, serverPeerTLS := newUDPPair(t)
	t.Cleanup(func() { _ = serverTunUDPTLS.Close(); _ = serverPeerTLS.Close() })

	// Server TunMaster with a single listener
	lServer := newChanListener()
	var tmServer netx.TunMaster[string]
	tmServer.Logger = logger

	// Route 1: TLS first
	tmServer.SetRoute("tls", func(connCtx context.Context, conn net.Conn) (bool, context.Context, netx.Tun) {
		// Match if the connection looks like a tls.Conn
		if _, ok := conn.(interface{ ConnectionState() tls.ConnectionState }); !ok {
			return false, connCtx, netx.Tun{}
		}
		fc := netx.NewFramedConn(conn)
		return true, connCtx, netx.Tun{Logger: logger, Conn: fc, Peer: serverTunUDPTLS, BufferSize: 64 << 10}
	})

	// Route 2: Plain fallback
	tmServer.SetRoute("plain", func(connCtx context.Context, conn net.Conn) (bool, context.Context, netx.Tun) {
		// If it wasn't TLS, handle as plain framed
		if _, ok := conn.(interface{ ConnectionState() tls.ConnectionState }); ok {
			return false, connCtx, netx.Tun{}
		}
		fc := netx.NewFramedConn(conn)
		return true, connCtx, netx.Tun{Logger: logger, Conn: fc, Peer: serverTunUDPPlain, BufferSize: 64 << 10}
	})

	// Start server
	errChServer := make(chan error, 1)
	go func() { errChServer <- tmServer.Serve(ctx, lServer) }()

	// Create client tunnels for plain and TLS, and feed server with their counterparts.
	// Plain connection pair
	clientPlain, serverPlain := net.Pipe()
	t.Cleanup(func() { _ = clientPlain.Close(); _ = serverPlain.Close() })

	// TLS connection pair (wrap pipe ends)
	cTLSRaw, sTLSRaw := net.Pipe()
	t.Cleanup(func() { _ = cTLSRaw.Close(); _ = sTLSRaw.Close() })
	srvCfg := &tls.Config{Certificates: []tls.Certificate{mustSelfSignedCert(t)}}
	cliCfg := &tls.Config{InsecureSkipVerify: true}
	tlsServer := tls.Server(sTLSRaw, srvCfg)
	tlsClient := tls.Client(cTLSRaw, cliCfg)
	t.Cleanup(func() { _ = tlsClient.Close(); _ = tlsServer.Close() })

	// Client-side UDP peers (sources before encapsulation and sinks after decapsulation)
	clientPeerPlain, clientTunUDPPlain := newUDPPair(t)
	t.Cleanup(func() { _ = clientPeerPlain.Close(); _ = clientTunUDPPlain.Close() })
	clientPeerTLS, clientTunUDPTLS := newUDPPair(t)
	t.Cleanup(func() { _ = clientPeerTLS.Close(); _ = clientTunUDPTLS.Close() })

	// Start client-side relays
	go (&netx.Tun{Logger: logger, Conn: netx.NewFramedConn(clientPlain), Peer: clientTunUDPPlain, BufferSize: 64 << 10}).Relay(ctx)
	go (&netx.Tun{Logger: logger, Conn: netx.NewFramedConn(tlsClient), Peer: clientTunUDPTLS, BufferSize: 64 << 10}).Relay(ctx)

	// Deliver server-side connections to a single listener to test routing
	lServer.ch <- serverPlain // should match plain route
	lServer.ch <- tlsServer   // should match TLS route

	// Give tunnels time to initialize and (for TLS) handshake on first IO
	time.Sleep(50 * time.Millisecond)

	// Verify plain path client -> server
	msgP := []byte("plain: hello")
	_ = clientPeerPlain.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := clientPeerPlain.Write(msgP); err != nil {
		t.Fatalf("clientPeerPlain write: %v", err)
	}
	_ = serverPeerPlain.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	n, err := serverPeerPlain.Read(buf)
	if err != nil {
		t.Fatalf("serverPeerPlain read: %v", err)
	}
	if string(buf[:n]) != string(msgP) {
		t.Fatalf("serverPeerPlain got %q want %q", string(buf[:n]), string(msgP))
	}

	// Verify TLS path client -> server
	msgT := []byte("tls: hello")
	_ = clientPeerTLS.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := clientPeerTLS.Write(msgT); err != nil {
		t.Fatalf("clientPeerTLS write: %v", err)
	}
	_ = serverPeerTLS.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = serverPeerTLS.Read(buf)
	if err != nil {
		t.Fatalf("serverPeerTLS read: %v", err)
	}
	if string(buf[:n]) != string(msgT) {
		t.Fatalf("serverPeerTLS got %q want %q", string(buf[:n]), string(msgT))
	}

	// Verify reverse direction for both routes
	_ = serverPeerPlain.SetWriteDeadline(time.Now().Add(2 * time.Second))
	rspP := []byte("plain: world")
	if _, err := serverPeerPlain.Write(rspP); err != nil {
		t.Fatalf("serverPeerPlain write: %v", err)
	}
	_ = clientPeerPlain.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = clientPeerPlain.Read(buf)
	if err != nil {
		t.Fatalf("clientPeerPlain read: %v", err)
	}
	if string(buf[:n]) != string(rspP) {
		t.Fatalf("clientPeerPlain got %q want %q", string(buf[:n]), string(rspP))
	}

	_ = serverPeerTLS.SetWriteDeadline(time.Now().Add(2 * time.Second))
	rspT := []byte("tls: world")
	if _, err := serverPeerTLS.Write(rspT); err != nil {
		t.Fatalf("serverPeerTLS write: %v", err)
	}
	_ = clientPeerTLS.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = clientPeerTLS.Read(buf)
	if err != nil {
		t.Fatalf("clientPeerTLS read: %v", err)
	}
	if string(buf[:n]) != string(rspT) {
		t.Fatalf("clientPeerTLS got %q want %q", string(buf[:n]), string(rspT))
	}

	// Cleanup: close client stream sides to end tunnels and allow graceful shutdown
	_ = clientPlain.Close()
	_ = tlsClient.Close()

	// Graceful shutdown of server
	sdCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := tmServer.Shutdown(sdCtx); err != nil {
		t.Fatalf("tmServer Shutdown: %v", err)
	}
	select {
	case got := <-errChServer:
		if got != nil && !errors.Is(got, netx.ErrServerClosed) {
			t.Fatalf("Serve returned %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("server did not exit after Shutdown")
	}
}

// mustSelfSignedCert returns a TLS certificate for tests.
func mustSelfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	// Generate an ECDSA self-signed certificate for localhost testing
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(10 * time.Minute),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("x509 key pair: %v", err)
	}
	return cert
}
