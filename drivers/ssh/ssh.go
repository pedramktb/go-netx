package ssh

import (
	"bytes"
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
			case "pubkey":
				azkey, err := hex.DecodeString(value)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid ssh pubkey parameter: %w", err)
				}
				pubkey, _, _, _, err = ssh.ParseAuthorizedKey(azkey)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid ssh public key: %w", err)
				}
			default:
				return netx.Wrapper{}, fmt.Errorf("uri: unknown ssh parameter %q", key)
			}
		}
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
				Name:     "ssh",
				Params:   params,
				Listener: listener,
				ListenerToListener: func(l net.Listener) (net.Listener, error) {
					return netx.ConnWrapListener(l, func(c net.Conn) (net.Conn, error) {
						return sshproto.NewSSHServerConn(c, cfg)
					})
				},
				ConnToConn: func(c net.Conn) (net.Conn, error) {
					return sshproto.NewSSHServerConn(c, cfg)
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
				Name:     "ssh",
				Params:   params,
				Listener: listener,
				DialerToDialer: func(f func() (net.Conn, error)) (func() (net.Conn, error), error) {
					return netx.ConnWrapDialer(f, func(c net.Conn) (net.Conn, error) {
						return sshproto.NewSSHClientConn(c, cfg)
					})
				},
				ConnToConn: func(c net.Conn) (net.Conn, error) {
					return sshproto.NewSSHClientConn(c, cfg)
				}}, nil
		}
	})
}
