package tls

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"

	"github.com/pedramktb/go-netx"
)

func init() {
	netx.Register("tls", func(params map[string]string, listener bool) (netx.Wrapper, error) {
		var certKey, cert []byte
		cfg := &tls.Config{
			MinVersion: tls.VersionTLS13,
			MaxVersion: tls.VersionTLS13,
		}
		for key, value := range params {
			switch key {
			case "key":
				var err error
				certKey, err = hex.DecodeString(value)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid tls key parameter: %w", err)
				}
			case "cert":
				var err error
				cert, err = hex.DecodeString(value)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid tls cert parameter: %w", err)
				}
			case "servername":
				cfg.ServerName = value
			default:
				return netx.Wrapper{}, fmt.Errorf("uri: unknown tls parameter %q", key)
			}
		}
		if listener {
			if cert == nil || certKey == nil {
				return netx.Wrapper{}, fmt.Errorf("uri: tls server requires cert and key parameters")
			}
			certificate, err := tls.X509KeyPair(cert, certKey)
			if err != nil {
				return netx.Wrapper{}, fmt.Errorf("uri: invalid tls certificate: %w", err)
			}
			cfg.Certificates = []tls.Certificate{certificate}
			return netx.Wrapper{
				Name:     "tls",
				Params:   params,
				Listener: listener,
				ListenerToListener: func(l net.Listener) (net.Listener, error) {
					return tls.NewListener(l, cfg), nil
				},
				ConnToConn: func(c net.Conn) (net.Conn, error) {
					return tls.Server(c, cfg), nil
				}}, nil
		} else {
			if certKey != nil {
				return netx.Wrapper{}, fmt.Errorf("uri: tls client does not support key parameter")
			}
			if cert != nil {
				var err error
				cfg.InsecureSkipVerify = true
				cfg.VerifyPeerCertificate, err = spkiVerifier(cert)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid tls cert parameter: %w", err)
				}
			}
			if cfg.ServerName == "" && cert == nil {
				return netx.Wrapper{}, fmt.Errorf("uri: tls client requires servername or cert parameter")
			}
			return netx.Wrapper{
				Name:     "tls",
				Params:   params,
				Listener: listener,
				DialerToDialer: func(f netx.Dialer) (netx.Dialer, error) {
					return netx.ConnWrapDialer(f, func(c net.Conn) (net.Conn, error) {
						return tls.Client(c, cfg), nil
					})
				},
				ConnToConn: func(c net.Conn) (net.Conn, error) {
					return tls.Client(c, cfg), nil
				}}, nil
		}
	})
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
