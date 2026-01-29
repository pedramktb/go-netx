package uri

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/pedramktb/go-netx"
	"github.com/pion/dtls/v3"
	dtlsnet "github.com/pion/dtls/v3/pkg/net"
	tlswithpks "github.com/raff/tls-ext"
	tlspks "github.com/raff/tls-psk"
	utls "github.com/refraction-networking/utls"
	"golang.org/x/crypto/ssh"
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

func (ls Layers) WrapConn(conn net.Conn) (net.Conn, error) {
	var err error
	for _, l := range ls {
		conn, err = l.WrapConn(conn)
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

func (ls Layers) MarshalJSON() ([]byte, error) {
	return json.Marshal(ls)
}

func (ls *Layers) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &ls)
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
	wrap   func(net.Conn) (net.Conn, error)
}

func (l Layer) WrapConn(conn net.Conn) (net.Conn, error) {
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

func (l *Layer) UnmarshalText(text []byte, server bool) error {
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

	switch l.Prot {
	case "dnst":
		fmt.Println("URI LAYER PARSING DNST")
		domain := l.Params["domain"]
		if domain == "" {
			return fmt.Errorf("uri: missing dnst domain parameter")
		}
		l.wrap = func(c net.Conn) (net.Conn, error) {
			if server {
				return netx.NewDNSTServerConn(c, domain), nil
			}
			return netx.NewDNSTClientConn(c, domain), nil
		}
	case "framed":
		opts := []netx.FramedConnOption{}
		for key, value := range l.Params {
			switch key {
			case "maxsize":
				maxSize, err := strconv.ParseUint(value, 10, 31)
				if err != nil {
					return fmt.Errorf("uri: invalid framed maxsize parameter %q: %w", value, err)
				}
				opts = append(opts, netx.WithMaxFrameSize(uint32(maxSize)))
			default:
				return fmt.Errorf("uri: unknown framed parameter %q", key)
			}
		}
		l.wrap = func(c net.Conn) (net.Conn, error) {
			return netx.NewFramedConn(c, opts...), nil
		}
	case "buffered":
		opts := []netx.BufConnOption{}
		for key, value := range l.Params {
			switch key {
			case "size":
				size, err := strconv.ParseUint(value, 10, 31)
				if err != nil {
					return fmt.Errorf("uri: invalid buffered size parameter %q: %w", value, err)
				}
				opts = append(opts, netx.WithBufSize(uint32(size)))
			default:
				return fmt.Errorf("uri: unknown buffered parameter %q", key)
			}
		}
		l.wrap = func(c net.Conn) (net.Conn, error) {
			return netx.NewBufConn(c, opts...), nil
		}
	case "aesgcm":
		aeskey := []byte{}
		opts := []netx.AESGCMOption{}
		for key, value := range l.Params {
			switch key {
			case "key":
				var err error
				aeskey, err = hex.DecodeString(value)
				if err != nil {
					return fmt.Errorf("uri: invalid aesgcm key parameter: %w", err)
				}
				if len(aeskey) != 16 && len(aeskey) != 24 && len(aeskey) != 32 {
					return fmt.Errorf("uri: invalid aesgcm key size %d", len(aeskey))
				}
			case "maxpacket":
				maxPacket, err := strconv.ParseUint(value, 10, 31)
				if err != nil {
					return fmt.Errorf("uri: invalid aesgcm maxpacket parameter %q: %w", value, err)
				}
				opts = append(opts, netx.WithAESGCMMaxPacket(uint32(maxPacket)))
			default:
				return fmt.Errorf("uri: unknown aesgcm parameter %q", key)
			}
		}
		if len(aeskey) == 0 {
			return fmt.Errorf("uri: missing aesgcm key parameter")
		}
		l.wrap = func(c net.Conn) (net.Conn, error) {
			return netx.NewAESGCMConn(c, aeskey, opts...)
		}
	case "ssh":
		var pass string
		var sshkey ssh.Signer // Host key for server, private key for client
		var pubkey ssh.PublicKey
		for key, value := range l.Params {
			switch key {
			case "pass":
				pass = value
			case "key":
				pemkey, err := hex.DecodeString(value)
				if err != nil {
					return fmt.Errorf("uri: invalid ssh key parameter: %w", err)
				}
				sshkey, err = ssh.ParsePrivateKey(pemkey)
				if err != nil {
					return fmt.Errorf("uri: invalid ssh private key: %w", err)
				}
			case "pubkey":
				azkey, err := hex.DecodeString(value)
				if err != nil {
					return fmt.Errorf("uri: invalid ssh pubkey parameter: %w", err)
				}
				pubkey, _, _, _, err = ssh.ParseAuthorizedKey(azkey)
				if err != nil {
					return fmt.Errorf("uri: invalid ssh public key: %w", err)
				}
			default:
				return fmt.Errorf("uri: unknown ssh parameter %q", key)
			}
		}
		if server {
			cfg := &ssh.ServerConfig{}
			if sshkey == nil {
				return fmt.Errorf("uri: ssh server requires key parameter")
			}
			cfg.AddHostKey(sshkey)
			if pubkey != nil {
				cfg.PublicKeyCallback = func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
					if bytes.Equal(key.Marshal(), pubkey.Marshal()) {
						return nil, nil
					}
					return nil, fmt.Errorf("uri: ssh public key mismatch")
				}
			}
			if pass != "" {
				cfg.PasswordCallback = func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
					if pass == string(password) {
						return nil, nil
					}
					return nil, fmt.Errorf("uri: ssh password mismatch")
				}
			}
			if cfg.PublicKeyCallback == nil && cfg.PasswordCallback == nil {
				return fmt.Errorf("uri: ssh server requires pubkey or pass parameter")
			}
			l.wrap = func(c net.Conn) (net.Conn, error) {
				return netx.NewSSHServerConn(c, cfg)
			}
		} else {
			cfg := &ssh.ClientConfig{}
			if pubkey == nil {
				return fmt.Errorf("uri: ssh client requires pubkey parameter")
			}
			cfg.HostKeyCallback = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
				if bytes.Equal(key.Marshal(), pubkey.Marshal()) {
					return nil
				}
				return fmt.Errorf("uri: ssh host key mismatch")
			}
			if sshkey != nil {
				cfg.Auth = append(cfg.Auth, ssh.PublicKeys(sshkey))
			}
			if pass != "" {
				cfg.Auth = append(cfg.Auth, ssh.Password(pass))
			}
			if len(cfg.Auth) == 0 {
				return fmt.Errorf("uri: ssh client requires key or pass parameter")
			}
			l.wrap = func(c net.Conn) (net.Conn, error) {
				return netx.NewSSHClientConn(c, cfg)
			}
		}
	case "tls":
		var certKey, cert []byte
		cfg := &tls.Config{
			MinVersion: tls.VersionTLS13,
			MaxVersion: tls.VersionTLS13,
		}
		for key, value := range l.Params {
			switch key {
			case "key":
				var err error
				certKey, err = hex.DecodeString(value)
				if err != nil {
					return fmt.Errorf("uri: invalid tls key parameter: %w", err)
				}
			case "cert":
				var err error
				cert, err = hex.DecodeString(value)
				if err != nil {
					return fmt.Errorf("uri: invalid tls cert parameter: %w", err)
				}
			case "servername":
				cfg.ServerName = value
			default:
				return fmt.Errorf("uri: unknown tls parameter %q", key)
			}
		}
		if server {
			if cert == nil || certKey == nil {
				return fmt.Errorf("uri: tls server requires cert and key parameters")
			}
			certificate, err := tls.X509KeyPair(cert, certKey)
			if err != nil {
				return fmt.Errorf("uri: invalid tls certificate: %w", err)
			}
			cfg.Certificates = []tls.Certificate{certificate}
			l.wrap = func(c net.Conn) (net.Conn, error) {
				return tls.Server(c, cfg), nil
			}
		} else {
			if certKey != nil {
				return fmt.Errorf("uri: tls client does not support key parameter")
			}
			if cert != nil {
				var err error
				cfg.InsecureSkipVerify = true
				cfg.VerifyPeerCertificate, err = spkiVerifier(cert)
				if err != nil {
					return fmt.Errorf("uri: invalid tls cert parameter: %w", err)
				}
			}
			if cfg.ServerName == "" && cert == nil {
				return fmt.Errorf("uri: tls client requires servername or cert parameter")
			}
			l.wrap = func(c net.Conn) (net.Conn, error) {
				return tls.Client(c, cfg), nil
			}
		}
	case "utls":
		if server {
			return errors.New("uri: utls is exclusive to clients, use tls for servers instead")
		}
		var cert []byte
		cfg := &utls.Config{
			MinVersion: tls.VersionTLS13,
			MaxVersion: tls.VersionTLS13,
		}
		id := utls.HelloChrome_Auto
		for key, value := range l.Params {
			switch key {
			case "cert":
				var err error
				cert, err = hex.DecodeString(value)
				if err != nil {
					return fmt.Errorf("uri: invalid utls cert parameter: %w", err)
				}
			case "servername":
				cfg.ServerName = value
			case "hello":
				switch strings.ToLower(value) {
				case "chrome":
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
					return fmt.Errorf("unknown utls hello profile %q", value)
				}
			default:
				return fmt.Errorf("uri: unknown utls parameter %q", key)
			}
		}
		if cert != nil {
			var err error
			cfg.InsecureSkipVerify = true
			cfg.VerifyPeerCertificate, err = spkiVerifier(cert)
			if err != nil {
				return fmt.Errorf("uri: invalid utls cert parameter: %w", err)
			}
		}
		if cfg.ServerName == "" && cert == nil {
			return fmt.Errorf("uri: utls client requires servername or cert parameter")
		}
		l.wrap = func(c net.Conn) (net.Conn, error) {
			uc := utls.UClient(c, cfg, id)
			return uc, uc.Handshake()
		}
	case "dtls":
		var certKey, cert []byte
		cfg := &dtls.Config{}
		for key, value := range l.Params {
			switch key {
			case "key":
				var err error
				certKey, err = hex.DecodeString(value)
				if err != nil {
					return fmt.Errorf("uri: invalid dtls key parameter: %w", err)
				}
			case "cert":
				var err error
				cert, err = hex.DecodeString(value)
				if err != nil {
					return fmt.Errorf("uri: invalid dtls cert parameter: %w", err)
				}
			case "servername":
				cfg.ServerName = value
			default:
				return fmt.Errorf("uri: unknown dtls parameter %q", key)
			}
		}
		if server {
			if cert == nil || certKey == nil {
				return fmt.Errorf("uri: dtls server requires cert and key parameters")
			}
			certificate, err := tls.X509KeyPair(cert, certKey)
			if err != nil {
				return fmt.Errorf("uri: invalid dtls certificate: %w", err)
			}
			cfg.Certificates = []tls.Certificate{certificate}
			l.wrap = func(c net.Conn) (net.Conn, error) {
				return dtls.Server(dtlsnet.PacketConnFromConn(c), c.RemoteAddr(), cfg)
			}
		} else {
			if certKey != nil {
				return fmt.Errorf("uri: dtls client does not support key parameter")
			}
			if cert != nil {
				var err error
				cfg.InsecureSkipVerify = true
				cfg.VerifyPeerCertificate, err = spkiVerifier(cert)
				if err != nil {
					return fmt.Errorf("uri: invalid dtls cert parameter: %w", err)
				}
			}
			if cfg.ServerName == "" && cert == nil {
				return fmt.Errorf("uri: dtls client requires servername or cert parameter")
			}
			l.wrap = func(c net.Conn) (net.Conn, error) {
				return dtls.Client(dtlsnet.PacketConnFromConn(c), c.RemoteAddr(), cfg)
			}
		}
	case "tlspsk":
		var identity string
		var psk []byte
		for key, value := range l.Params {
			switch key {
			case "key":
				var err error
				psk, err = hex.DecodeString(value)
				if err != nil {
					return fmt.Errorf("uri: invalid tlspsk key parameter: %w", err)
				}
			case "identity":
				identity = value
			default:
				return fmt.Errorf("uri: unknown tlspsk parameter %q", key)
			}
		}
		if len(psk) == 0 {
			return fmt.Errorf("uri: missing tlspsk key parameter")
		}
		if !server && identity == "" {
			return fmt.Errorf("uri: tlspsk client requires identity parameter")
		}
		cfg := &tlswithpks.Config{
			MinVersion: tls.VersionTLS12,
			MaxVersion: tls.VersionTLS12,
			Extra: tlspks.PSKConfig{
				GetIdentity: func() string { return identity },
				GetKey:      func(identity string) ([]byte, error) { return psk, nil },
			},
			CipherSuites:       []uint16{tlspks.TLS_PSK_WITH_AES_256_CBC_SHA},
			InsecureSkipVerify: true,
		}
		if server {
			// Provide dummy Certificates to make tlspsk happy on server side
			cfg.Certificates = dummyCert()
			l.wrap = func(c net.Conn) (net.Conn, error) {
				return tlswithpks.Server(c, cfg), nil
			}
		} else {
			l.wrap = func(c net.Conn) (net.Conn, error) {
				return tlswithpks.Client(c, cfg), nil
			}
		}
	case "dtlspsk":
		var identity string
		var psk []byte
		for key, value := range l.Params {
			switch key {
			case "key":
				var err error
				psk, err = hex.DecodeString(value)
				if err != nil {
					return fmt.Errorf("uri: invalid dtlspsk key parameter: %w", err)
				}
			case "identity":
				identity = value
			default:
				return fmt.Errorf("uri: unknown dtlspsk parameter %q", key)
			}
		}
		if len(psk) == 0 {
			return fmt.Errorf("uri: missing dtlspsk key parameter")
		}
		if !server && identity == "" {
			return fmt.Errorf("uri: dtlspsk client requires identity parameter")
		}
		cfg := &dtls.Config{
			PSK: func(hint []byte) ([]byte, error) {
				return psk, nil
			},
			PSKIdentityHint:    []byte(identity),
			CipherSuites:       []dtls.CipherSuiteID{dtls.TLS_PSK_WITH_AES_128_GCM_SHA256},
			InsecureSkipVerify: true,
		}
		if server {
			l.wrap = func(c net.Conn) (net.Conn, error) {
				return dtls.Server(dtlsnet.PacketConnFromConn(c), c.RemoteAddr(), cfg)
			}
		} else {
			l.wrap = func(c net.Conn) (net.Conn, error) {
				return dtls.Client(dtlsnet.PacketConnFromConn(c), c.RemoteAddr(), cfg)
			}
		}
	}
	return nil
}

func spkiVerifier(certPEM []byte) (func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("uri: invalid PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("uri: parse x509 certificate: %w", err)
	}
	spkiHash := sha256.New().Sum(cert.RawSubjectPublicKeyInfo)
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
	cert, _ := tlswithpks.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	return []tlswithpks.Certificate{cert}
}
