package utls

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/pedramktb/go-netx"
	utls "github.com/refraction-networking/utls"
)

func init() {
	netx.Register("utls", netx.FuncDriver(
		func(params map[string]string, listener bool) (netx.Wrapper, error) {
			if listener {
				return nil, errors.New("uri: utls is exclusive to clients, use tls for servers instead")
			}
			var cert []byte
			cfg := &utls.Config{
				MinVersion: tls.VersionTLS13,
				MaxVersion: tls.VersionTLS13,
			}
			id := utls.HelloChrome_Auto
			for key, value := range params {
				switch key {
				case "cert":
					var err error
					cert, err = hex.DecodeString(value)
					if err != nil {
						return nil, fmt.Errorf("uri: invalid utls cert parameter: %w", err)
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
						return nil, fmt.Errorf("unknown utls hello profile %q", value)
					}
				default:
					return nil, fmt.Errorf("uri: unknown utls parameter %q", key)
				}
			}
			if cert != nil {
				var err error
				cfg.InsecureSkipVerify = true
				cfg.VerifyPeerCertificate, err = spkiVerifier(cert)
				if err != nil {
					return nil, fmt.Errorf("uri: invalid utls cert parameter: %w", err)
				}
			}
			if cfg.ServerName == "" && cert == nil {
				return nil, fmt.Errorf("uri: utls client requires servername or cert parameter")
			}
			return func(c net.Conn) (net.Conn, error) {
				uc := utls.UClient(c, cfg, id)
				return uc, uc.Handshake()
			}, nil
		}))
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
