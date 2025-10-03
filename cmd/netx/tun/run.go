package tun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	netx "github.com/pedramktb/go-netx"
	dtls "github.com/pion/dtls/v3"
	dtlsnet "github.com/pion/dtls/v3/pkg/net"
	pudp "github.com/pion/udp/v2"
	tlswithpks "github.com/raff/tls-ext"
	tlspks "github.com/raff/tls-psk"
	utls "github.com/refraction-networking/utls"
	"golang.org/x/crypto/ssh"
)

// chainStep represents a single segment in a connection chain (e.g. tls[key=...]).
type chainStep struct {
	name   string
	params map[string]string
}

// Run executes the tun subcommand.
// Usage: netx tun --from <chain>://host:port --to <chain>://host:port
func Run(ctx context.Context, cancel context.CancelFunc, args []string) {
	fs := flag.NewFlagSet("tun", flag.ExitOnError)
	from := fs.String("from", "", "chain URI for incoming side, e.g. tcp+tls[cert=...,key=...]://:9000 or udp+dtls[cert=...,key=...]://:4444")
	to := fs.String("to", "", "chain URI for peer side, e.g. tcp+tls[cert=...]://example.com:9443 or udp+aesgcm[key=...]://1.2.3.4:5555")
	logLevel := fs.String("log", "info", "log level: debug|info|warn|error")
	help := fs.Bool("h", false, "show help")
	_ = fs.Parse(args)

	if *help {
		fmt.Fprintln(os.Stderr, tunUsage())
		return
	}

	rest := fs.Args()
	if len(rest) != 0 || *from == "" || *to == "" {
		fmt.Fprintln(os.Stderr, tunUsage())
		os.Exit(2)
	}

	// Configure logging level
	lvl := slog.LevelInfo
	switch strings.ToLower(*logLevel) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))

	// Parse endpoints (chain + address)
	fromBase, fromSteps, fromAddr, err := parseChainURI(*from)
	if err != nil {
		slog.ErrorContext(ctx, "parse --from", "error", err)
		os.Exit(2)
	}
	toBase, toSteps, toAddr, err := parseChainURI(*to)
	if err != nil {
		slog.ErrorContext(ctx, "parse --to", "error", err)
		os.Exit(2)
	}

	// Build base listener only (wrappers are applied in the handler in order)
	ln, err := buildListener(ctx, fromAddr, fromBase)
	if err != nil {
		slog.ErrorContext(ctx, "error listening", "error", err, "addr", fromAddr)
		os.Exit(2)
	}
	defer ln.Close()

	// Build base dialer for outgoing side
	dialBase, err := buildDialer(toAddr, toBase)
	if err != nil {
		slog.ErrorContext(ctx, "error building dialer", "error", err, "addr", toAddr)
		os.Exit(2)
	}

	// Create TunMaster and route everything
	tm := netx.TunMaster[struct{}]{}

	tm.SetRoute(struct{}{}, func(ctx context.Context, conn net.Conn) (bool, context.Context, netx.Tun) {
		// Apply incoming wrappers in order (skip the base step)
		inSteps := fromSteps[1:]
		wc, err := applyWrappers(conn, inSteps, true)
		if err != nil {
			slog.Error("wrap incoming", "err", err)
			_ = conn.Close()
			return false, ctx, netx.Tun{}
		}

		// Dial and wrap peer side
		pcRaw, err := dialBase(ctx)
		if err != nil {
			slog.Error("dial peer", "err", err)
			_ = wc.Close()
			return false, ctx, netx.Tun{}
		}
		outSteps := toSteps[1:]
		pc, err := applyWrappers(pcRaw, outSteps, false)
		if err != nil {
			slog.Error("wrap outgoing", "err", err)
			_ = wc.Close()
			_ = pcRaw.Close()
			return false, ctx, netx.Tun{}
		}

		return true, ctx, netx.Tun{Conn: wc, Peer: pc}
	})

	go func() {
		if err := tm.Serve(ctx, ln); err != nil && !errors.Is(err, netx.ErrServerClosed) {
			slog.Error("serve error", "err", err)
			cancel()
		}
	}()

	slog.Info("netx tun started", "listen", ln.Addr().String(), "from", *from, "to", *to)

	<-ctx.Done()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	_ = tm.Shutdown(shutdownCtx)
}

func tunUsage() string {
	return `netx tun - relay between two endpoints with chainable transforms

Usage:
    netx tun --from <chain>://listenAddr --to <chain>://connectAddr

Where <chain> is a '+'-separated list starting with 'tcp' or 'udp', e.g.:
    tcp+tls[cert=server.crt,key=server.key]
    udp+dtls[cert=server.crt,key=server.key]
    tcp+tls[cert=server.crt,key=server.key]+framed[maxFrame=4096]+aesgcm[key=001122...]

Examples:
    netx tun \
        --from tcp+tls[cert=server.crt,key=server.key]://:9000 \
        --to   tcp+tls[cert=client.crt]+buffered[buf=8192]+framed[maxFrame=4096]+aesgcm[key=00112233445566778899aabbccddeeff]://example.com:9443

    netx tun \
        --from udp+dtls[cert=server.crt,key=server.key]://:4444 \
        --to   udp+aesgcm[key=...]://10.0.0.10:5555

Supported base transports:
	- tcp: TCP listener or dialer
	- udp: UDP listener or dialer

Supported wrappers:
	- tls: Transport Layer Security
		server params: key, cert
		client params: cert (optional, for SPKI pinning), serverName (required if cert not provided)
	- utls: TLS with client fingerprint camouflage via uTLS (github.com/refraction-networking/utls)
		client params: cert (optional, for SPKI pinning), serverName (required if cert not provided), hello (optional, e.g. chrome, firefox, ios, android, safari, edge, randomized)
	- dtls: Datagram Transport Layer Security
		server params: key, cert
		client params: cert (optional, for SPKI pinning), serverName (required if cert not provided)
	- tlspsk: TLS with pre-shared key. Cipher is TLS_DHE_PSK_WITH_AES_256_CBC_SHA. WARNING: This is not provided by the standard library, USE WITH CAUTION.
		params: key (hex-encoded)
	- dtlspsk: DTLS with pre-shared key. Cipher is TLS_PSK_WITH_AES_128_GCM_SHA256.
		params: key (hex-encoded)
	- aesgcm: AES-GCM encryption. A passive 12-byte handshake exchanges IVs.
		params: key (hex-encoded), maxPacket (optional, defaults to 32768)
	- buffered: buffered read/write for better performance when using framing.
		params: bufSize (optional, defaults to 4096)
	- framed: length-prefixed frames for transporting packet protocols or wrappers that need packet semantics over streams.
		params: maxFrame (optional, defaults to 32768)
	- ssh: SSH tunneling via "direct-tcpip" channels.
		server params: hostKey, user (optional, required with pass), pass (optional), authKey (optional, required if no pass)
		client options: hostKey, user, pass (optional), key (optional, required if no pass)

Notes:
    - If 'cert' is provided on the client for tls/dtls, default validation is disabled and a manual SPKI (SubjectPublicKeyInfo) hash comparison is performed
      against the provided certificate. This is certificate pinning and will fail if the server presents a different key.
`
}

// parseChainURI parses strings like "tcp+tls[...]+framed://host:port".
// Returns base (tcp|udp), full steps (including the base), and addr.
func parseChainURI(s string) (string, []chainStep, string, error) {
	parts := strings.SplitN(s, "://", 2)
	if len(parts) != 2 {
		return "", nil, "", fmt.Errorf("invalid chain URI (missing ://): %q", s)
	}
	chainSpec, addr := parts[0], parts[1]
	if strings.TrimSpace(addr) == "" {
		return "", nil, "", fmt.Errorf("missing host:port in %q", s)
	}
	steps, err := parseChain(chainSpec)
	if err != nil {
		return "", nil, "", err
	}
	if len(steps) == 0 || (steps[0].name != "tcp" && steps[0].name != "udp") {
		return "", nil, "", fmt.Errorf("chain must start with tcp or udp: %q", chainSpec)
	}
	return steps[0].name, steps, addr, nil
}

// parseChain parses strings like "tcp+tls[cert=x,key=y]+framed[maxFrame=4096]".
func parseChain(s string) ([]chainStep, error) {
	var steps []chainStep
	i := 0
	for i < len(s) {
		// read name until '[' or '+' or end
		j := i
		for j < len(s) && s[j] != '[' && s[j] != '+' {
			j++
		}
		if j == i {
			return nil, fmt.Errorf("unexpected token at %d", i)
		}
		name := strings.ToLower(s[i:j])
		params := map[string]string{}
		if j < len(s) && s[j] == '[' {
			// find closing ']'
			k := j + 1
			depth := 1
			for k < len(s) && depth > 0 {
				if s[k] == '[' {
					depth++
				} else if s[k] == ']' {
					depth--
					if depth == 0 {
						break
					}
				}
				k++
			}
			if depth != 0 {
				return nil, fmt.Errorf("unclosed '[' for %s", name)
			}
			content := s[j+1 : k]
			// parse k=v pairs separated by ','
			if strings.TrimSpace(content) != "" {
				for _, kv := range splitComma(content) {
					parts := strings.SplitN(kv, "=", 2)
					if len(parts) != 2 {
						return nil, fmt.Errorf("invalid param %q in %s", kv, name)
					}
					params[strings.ToLower(strings.TrimSpace(parts[0]))] = strings.TrimSpace(parts[1])
				}
			}
			j = k + 1
		}
		steps = append(steps, chainStep{name: name, params: params})
		if j < len(s) {
			if s[j] != '+' {
				return nil, fmt.Errorf("expected '+' after %s", name)
			}
			j++
		}
		i = j
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("empty chain")
	}
	return steps, nil
}

func splitComma(s string) []string {
	// simple split, no escaping supported
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// buildListener returns a listener possibly pre-wrapped with tls/dtls.
// It also returns remaining steps that should be applied per-connection on the incoming side.
func buildListener(ctx context.Context, addr string, base string) (net.Listener, error) {
	switch base {
	case "tcp":
		return (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	case "udp":
		uaddr, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			return nil, err
		}
		return (&pudp.ListenConfig{}).Listen("udp", uaddr)
	default:
		return nil, fmt.Errorf("unknown base %q (want tcp|udp)", base)
	}
}

// buildDialer creates a function that dials and applies wrappers according to the chain.
func buildDialer(addr string, base string) (func(ctx context.Context) (net.Conn, error), error) {
	switch base {
	case "tcp":
		return func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		}, nil
	case "udp":
		return func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "udp", addr)
		}, nil
	default:
		return nil, fmt.Errorf("unknown base %q (want tcp|udp)", base)
	}
}

// applyWrappers applies the given steps in order on the provided connection.
// The 'from' parameter indicates if this is the incoming side (true) or outgoing side (false).
func applyWrappers(conn net.Conn, steps []chainStep, from bool) (net.Conn, error) {
	var c net.Conn = conn
	for _, st := range steps {
		switch st.name {
		case "buffered":
			opts := []netx.BufConnOption{}
			if v, ok := st.params["buf"]; ok && strings.TrimSpace(v) != "" {
				size, err := strconv.Atoi(v)
				if err != nil {
					return nil, fmt.Errorf("invalid buf size %q: %w", v, err)
				}
				opts = append(opts, netx.WithBufSize(size))
			}
			c = netx.NewBufConn(c, opts...)
		case "framed":
			opts := []netx.FramedConnOption{}
			if v, ok := st.params["maxframe"]; ok && strings.TrimSpace(v) != "" {
				max, err := strconv.Atoi(v)
				if err != nil {
					return nil, fmt.Errorf("invalid maxFrame size %q: %w", v, err)
				}
				opts = append(opts, netx.WithMaxFrameSize(max))
			}
			c = netx.NewFramedConn(c, opts...)
		case "aesgcm":
			keyHex := st.params["key"]
			if keyHex == "" {
				return nil, fmt.Errorf("aesgcm requires key")
			}
			key, err := hex.DecodeString(keyHex)
			if err != nil {
				return nil, fmt.Errorf("invalid aesgcm key: %w", err)
			}
			opts := []netx.AESGCMOption{}
			if v, ok := st.params["maxpacket"]; ok && strings.TrimSpace(v) != "" {
				maxPkt, err := strconv.Atoi(v)
				if err != nil {
					return nil, fmt.Errorf("invalid maxpacket size %q: %w", v, err)
				}
				opts = append(opts, netx.WithMaxPacket(maxPkt))
			}
			c, err = netx.NewAESGCMConn(c, key, opts...)
			if err != nil {
				return nil, err
			}
		case "tls":
			cfg := &tls.Config{
				MinVersion: tls.VersionTLS13,
				MaxVersion: tls.VersionTLS13,
			}
			if from {
				// Server side requires cert+key
				certs, err := loadServerCertificates(st.params)
				if err != nil {
					return nil, fmt.Errorf("tls server config: %w", err)
				}
				cfg.Certificates = certs
				c = tls.Server(c, cfg)
			} else {
				// Client: if cert is provided, enable SPKI pinning with InsecureSkipVerify
				if cp, ok := st.params["cert"]; ok && strings.TrimSpace(cp) != "" {
					verify, err := makeSPKIPinVerifierFromCertPath(cp)
					if err != nil {
						return nil, fmt.Errorf("tls client pin setup: %w", err)
					}
					cfg.InsecureSkipVerify = true
					cfg.VerifyPeerCertificate = verify
				} else {
					// Otherwise, require serverName
					if sn, ok := st.params["servername"]; ok && strings.TrimSpace(sn) != "" {
						cfg.ServerName = strings.TrimSpace(sn)
					} else {
						return nil, fmt.Errorf("tls client requires serverName or cert")
					}
				}
				c = tls.Client(c, cfg)
			}
		case "utls":
			if from {
				return nil, fmt.Errorf("utls is client-side only")
			}
			cfg := &utls.Config{
				MinVersion: tls.VersionTLS13,
				MaxVersion: tls.VersionTLS13,
			}
			if cp, ok := st.params["cert"]; ok && strings.TrimSpace(cp) != "" {
				verify, err := makeSPKIPinVerifierFromCertPath(cp)
				if err != nil {
					return nil, fmt.Errorf("utls client pin setup: %w", err)
				}
				cfg.InsecureSkipVerify = true
				cfg.VerifyPeerCertificate = verify
			} else {
				if sn, ok := st.params["servername"]; ok && strings.TrimSpace(sn) != "" {
					cfg.ServerName = strings.TrimSpace(sn)
				} else {
					return nil, fmt.Errorf("utls client requires serverName or cert")
				}
			}
			// Map hello profile.
			hello := strings.ToLower(strings.TrimSpace(st.params["hello"]))
			var id utls.ClientHelloID
			switch hello {
			case "", "chrome":
				id = utls.HelloChrome_Auto
			case "firefox":
				id = utls.HelloFirefox_Auto
			case "ios":
				id = utls.HelloIOS_Auto
			case "android":
				id = utls.HelloAndroid_11_OkHttp
			case "safari":
				id = utls.HelloSafari_Auto
			case "edge":
				id = utls.HelloEdge_Auto
			case "randomized":
				id = utls.HelloRandomizedALPN
			case "randomizednoalpn":
				id = utls.HelloRandomized
			default:
				return nil, fmt.Errorf("unknown utls hello profile %q", hello)
			}
			uconn := utls.UClient(c, cfg, id)
			if err := uconn.Handshake(); err != nil {
				return nil, fmt.Errorf("utls handshake: %w", err)
			}
			c = uconn
		case "dtls":
			var err error
			cfg := &dtls.Config{}
			if from {
				certs, cerr := loadServerCertificates(st.params)
				if cerr != nil {
					return nil, fmt.Errorf("dtls server config: %w", cerr)
				}
				cfg.Certificates = certs
				c, err = dtls.Server(dtlsnet.PacketConnFromConn(c), c.RemoteAddr(), cfg)
			} else {
				if cp, ok := st.params["cert"]; ok && strings.TrimSpace(cp) != "" {
					verify, verr := makeSPKIPinVerifierFromCertPath(cp)
					if verr != nil {
						return nil, fmt.Errorf("dtls client pin setup: %w", verr)
					}
					cfg.InsecureSkipVerify = true
					cfg.VerifyPeerCertificate = verify
				} else {
					// Otherwise, require serverName
					if sn, ok := st.params["servername"]; ok && strings.TrimSpace(sn) != "" {
						cfg.ServerName = strings.TrimSpace(sn)
					} else {
						return nil, fmt.Errorf("dtls client requires serverName or cert")
					}
				}
				c, err = dtls.Client(dtlsnet.PacketConnFromConn(c), c.RemoteAddr(), cfg)
			}
			if err != nil {
				return nil, err
			}
		case "tlspsk":
			keyHex := strings.TrimSpace(st.params["key"])
			if keyHex == "" {
				return nil, fmt.Errorf("tlspsk requires key")
			}
			psk, err := hex.DecodeString(keyHex)
			if err != nil {
				return nil, fmt.Errorf("invalid tlspsk key: %w", err)
			}
			identity := strings.TrimSpace(st.params["identity"])
			if identity == "" {
				return nil, fmt.Errorf("tlspsk requires identity")
			}
			cfg := &tlswithpks.Config{
				MinVersion: tls.VersionTLS12,
				MaxVersion: tls.VersionTLS12,
				Extra: tlspks.PSKConfig{
					GetKey:      func(identity string) ([]byte, error) { return psk, nil },
					GetIdentity: func() string { return identity },
				},
				CipherSuites:       []uint16{tlspks.TLS_PSK_WITH_AES_256_CBC_SHA},
				InsecureSkipVerify: true,
			}
			if from {
				// Provide dummy Certificates to make tlspsk happy on server side
				cfg.Certificates = dummyCert()
				c = tlswithpks.Server(c, cfg)
			} else {
				c = tlswithpks.Client(c, cfg)
			}
		case "dtlspsk":
			keyHex := strings.TrimSpace(st.params["key"])
			if keyHex == "" {
				return nil, fmt.Errorf("dtlspsk requires key")
			}
			psk, err := hex.DecodeString(keyHex)
			if err != nil {
				return nil, fmt.Errorf("invalid dtlspsk key: %w", err)
			}
			identity := strings.TrimSpace(st.params["identity"])
			if identity == "" {
				return nil, fmt.Errorf("dtlspsk requires identity")
			}
			cfg := &dtls.Config{
				PSK:                func(hint []byte) ([]byte, error) { return psk, nil },
				PSKIdentityHint:    []byte(identity),
				CipherSuites:       []dtls.CipherSuiteID{dtls.TLS_PSK_WITH_AES_128_GCM_SHA256},
				InsecureSkipVerify: true,
			}
			if from {
				c, err = dtls.Server(dtlsnet.PacketConnFromConn(c), c.RemoteAddr(), cfg)
				if err != nil {
					return nil, err
				}
			} else {
				c, err = dtls.Client(dtlsnet.PacketConnFromConn(c), c.RemoteAddr(), cfg)
				if err != nil {
					return nil, err
				}
			}
		case "ssh":
			if from {
				cfg := &ssh.ServerConfig{}
				if hp := strings.TrimSpace(st.params["hostkey"]); hp != "" {
					keyData, err := os.ReadFile(hp)
					if err != nil {
						return nil, fmt.Errorf("read hostkey: %w", err)
					}
					private, err := ssh.ParsePrivateKey(keyData)
					if err != nil {
						return nil, fmt.Errorf("parse hostkey: %w", err)
					}
					cfg.AddHostKey(private)
				}
				if p := strings.TrimSpace(st.params["pass"]); p != "" {
					cfg.PasswordCallback = func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
						if c.User() == strings.TrimSpace(st.params["user"]) && string(pass) == p {
							return nil, nil
						}
						return nil, fmt.Errorf("invalid user or password")
					}
				}
				if ak := strings.TrimSpace(st.params["authkey"]); ak != "" {
					// Load authorized key file
					data, err := os.ReadFile(ak)
					if err != nil {
						return nil, fmt.Errorf("read authkey: %w", err)
					}
					pubKey, _, _, _, err := ssh.ParseAuthorizedKey(data)
					if err != nil {
						return nil, fmt.Errorf("parse authkey: %w", err)
					}
					cfg.PublicKeyCallback = func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
						if bytes.Equal(key.Marshal(), pubKey.Marshal()) {
							return nil, nil
						}
						return nil, fmt.Errorf("unauthorized public key")
					}
				}
				if cfg.PasswordCallback == nil && cfg.PublicKeyCallback == nil {
					return nil, fmt.Errorf("ssh server requires pass or authKey")
				}
				var err error
				c, err = netx.NewSSHServerConn(c, cfg)
				if err != nil {
					return nil, err
				}
			} else {
				cfg := &ssh.ClientConfig{
					User: strings.TrimSpace(st.params["user"]),
				}
				if p := strings.TrimSpace(st.params["pass"]); p != "" {
					cfg.Auth = append(cfg.Auth, ssh.Password(p))
				}
				if k := strings.TrimSpace(st.params["key"]); k != "" {
					keyData, err := os.ReadFile(k)
					if err != nil {
						return nil, fmt.Errorf("read private key: %w", err)
					}
					signer, err := ssh.ParsePrivateKey(keyData)
					if err != nil {
						return nil, fmt.Errorf("parse private key: %w", err)
					}
					cfg.Auth = append(cfg.Auth, ssh.PublicKeys(signer))
				}
				if hk := strings.TrimSpace(st.params["hostkey"]); hk != "" {
					keyData, err := os.ReadFile(hk)
					if err != nil {
						return nil, fmt.Errorf("read hostkey: %w", err)
					}
					hostKey, _, _, _, err := ssh.ParseAuthorizedKey(keyData)
					if err != nil {
						return nil, fmt.Errorf("parse hostkey: %w", err)
					}
					cfg.HostKeyCallback = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
						if bytes.Equal(key.Marshal(), hostKey.Marshal()) {
							return nil
						}
						return fmt.Errorf("host key mismatch")
					}
				}
				if cfg.User == "" || len(cfg.Auth) == 0 {
					return nil, fmt.Errorf("ssh client requires user and (pass or key)")
				}
				if cfg.HostKeyCallback == nil {
					if strings.ToLower(strings.TrimSpace(st.params["insecure"])) == "true" {
						cfg.HostKeyCallback = ssh.InsecureIgnoreHostKey()
					} else {
						return nil, fmt.Errorf("ssh client requires hostKey or insecure=true")
					}
				}
				var err error
				c, err = netx.NewSSHClientConn(c, cfg)
				if err != nil {
					return nil, err
				}
			}
		default:
			return nil, fmt.Errorf("unknown wrapper %q on incoming side", st.name)
		}
	}
	return c, nil
}

// loadServerCertificates loads the key pair specified by params["cert"], params["key"].
// Returns an error if missing or invalid.
func loadServerCertificates(params map[string]string) ([]tls.Certificate, error) {
	certPath := strings.TrimSpace(params["cert"])
	keyPath := strings.TrimSpace(params["key"])
	if certPath == "" || keyPath == "" {
		return nil, fmt.Errorf("both cert and key are required")
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load key pair: %w", err)
	}
	return []tls.Certificate{pair}, nil
}

// makeSPKIPinVerifierFromCertPath creates a VerifyPeerCertificate callback that pins the
// peer's SPKI hash (SHA-256 over RawSubjectPublicKeyInfo) to the certificate at certPath.
func makeSPKIPinVerifierFromCertPath(certPath string) (func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error, error) {
	spkiHash, err := spkiHashFromCertFile(certPath)
	if err != nil {
		return nil, err
	}
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		for _, rawCert := range rawCerts {
			c, err := x509.ParseCertificate(rawCert)
			if err != nil {
				return fmt.Errorf("parse peer cert: %w", err)
			}
			if bytes.Equal(sha256.New().Sum(c.RawSubjectPublicKeyInfo), spkiHash) {
				return nil
			}
		}
		return fmt.Errorf("no matching SPKI found")
	}, nil
}

// spkiHashFromCertFile reads a PEM certificate file and returns SHA-256(SPKI) bytes.
func spkiHashFromCertFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cert: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || (block.Type != "CERTIFICATE" && !strings.HasSuffix(block.Type, "CERTIFICATE")) {
		return nil, fmt.Errorf("no PEM certificate found in %s", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse cert: %w", err)
	}
	sum := sha256.New().Sum(cert.RawSubjectPublicKeyInfo)
	return sum, nil
}

// dummyCert returns a self-signed certificate for use in tls-psk server mode. (ed25519)
func dummyCert() []tlswithpks.Certificate {
	// Generated with:
	// openssl req -x509 -newkey ed25519 -keyout key.pem -out cert.pem -days 100000 -nodes -subj "/CN=dummy"
	certPEM := `-----BEGIN CERTIFICATE-----
MIIBNjCB6aADAgECAhRX020iAjrT4wTjwRdAJ+PPjpe33DAFBgMrZXAwEDEOMAwG
A1UEAwwFZHVtbXkwIBcNMjUwOTIxMTUxNzMwWhgPMjI5OTA3MDcxNTE3MzBaMBAx
DjAMBgNVBAMMBWR1bW15MCowBQYDK2VwAyEA/8RGhnpLT8uPAm8Ah0vEYWCskGrk
R3lqdOjspIidVmKjUzBRMB0GA1UdDgQWBBRMUX8P7I1KV1UxMjcJlIT42a72ozAf
BgNVHSMEGDAWgBRMUX8P7I1KV1UxMjcJlIT42a72ozAPBgNVHRMBAf8EBTADAQH/
MAUGAytlcANBAEFf17f1XhfLek4D203mGz8BihBfXfeL6kADMMV+G2qpkqZPcnTI
NXPuT9B/6+hM7nD/vh7JKXTfSAEFo22rzwA=
-----END CERTIFICATE-----
`
	keyPEM := `-----BEGIN PRIVATE KEY-----
MC4CAQAwBQYDK2VwBCIEIEsb9X3HHGBFSe5jKvqNmua6ZFplNaiBROtJ7ZZAJlRz
-----END PRIVATE KEY-----
`
	cert, err := tlswithpks.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		panic("dummyCert: " + err.Error())
	}
	return []tlswithpks.Certificate{cert}
}
