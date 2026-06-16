package ssh

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"

	"github.com/pedramktb/go-netx"
	sshproto "github.com/pedramktb/go-netx/proto/ssh"
	"golang.org/x/crypto/ssh"
)

func init() {
	netx.Register("ssh", func(params map[string]string, listener bool) (netx.Wrapper, error) {
		var pass string
		var sshkey ssh.Signer // Host key for server, private key for client
		var pubkey ssh.PublicKey
		for key, value := range params {
			switch key {
			case "pass":
				pass = value
			case "key":
				pemkey, err := hex.DecodeString(value)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid ssh key parameter: %w", err)
				}
				sshkey, err = ssh.ParsePrivateKey(pemkey)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid ssh private key: %w", err)
				}
			case "pub":
				azkey, err := hex.DecodeString(value)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid ssh public key parameter: %w", err)
				}
				pubkey, _, _, _, err = ssh.ParseAuthorizedKey(azkey)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid ssh public key: %w", err)
				}
			default:
				return netx.Wrapper{}, fmt.Errorf("uri: unknown ssh parameter %q", key)
			}
		}
		secretParams := sshSecretParams(sshkey)
		if listener {
			cfg := &ssh.ServerConfig{}
			if sshkey == nil {
				return netx.Wrapper{}, fmt.Errorf("uri: ssh server requires key parameter")
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
				return netx.Wrapper{}, fmt.Errorf("uri: ssh server requires pubkey or pass parameter")
			}
			return netx.Wrapper{
				Name:         "ssh",
				Params:       params,
				Listener:     listener,
				SecretParams: secretParams,
				ListenerToListener: func(l net.Listener) (net.Listener, error) {
					return netx.ConnWrapListener(l, func(c net.Conn) (net.Conn, error) {
						return sshproto.NewServerConn(c, cfg)
					})
				},
				ConnToConn: func(c net.Conn) (net.Conn, error) {
					return sshproto.NewServerConn(c, cfg)
				}}, nil
		} else {
			cfg := &ssh.ClientConfig{}
			if pubkey == nil {
				return netx.Wrapper{}, fmt.Errorf("uri: ssh client requires pubkey parameter")
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
				return netx.Wrapper{}, fmt.Errorf("uri: ssh client requires key or pass parameter")
			}
			return netx.Wrapper{
				Name:         "ssh",
				Params:       params,
				Listener:     listener,
				SecretParams: secretParams,
				DialerToDialer: func(f netx.Dialer) (netx.Dialer, error) {
					return netx.ConnWrapDialer(f, func(c net.Conn) (net.Conn, error) {
						return sshproto.NewClientConn(c, cfg)
					})
				},
				ConnToConn: func(c net.Conn) (net.Conn, error) {
					return sshproto.NewClientConn(c, cfg)
				}}, nil
		}
	})
}

// sshSecretParams declares the sensitive params on an ssh Wrapper:
//   - "key": private key — fingerprinted as the matching public key's standard
//     SSH fingerprint ("SHA256:<base64-of-sha256-of-wire-format>"), the same
//     value `ssh-keygen -lf` prints.
//   - "pass": password — bare REDACTED. A fingerprint of a low-entropy secret
//     is brute-forceable, so no payload is exposed.
func sshSecretParams(signer ssh.Signer) []netx.SecretParam {
	out := []netx.SecretParam{{Name: "pass"}}
	if signer != nil {
		sum := sha256.Sum256(signer.PublicKey().Marshal())
		out = append(out, netx.SecretParam{
			Name:        "key",
			Fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]),
		})
	}
	return out
}
