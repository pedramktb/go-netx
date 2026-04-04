/*
SSHConn is a network layer that tunnels traffic over a secure SSH connection.
It establishes an SSH handshake (client or server) over an underlying connection
and opens a "direct-tcpip" channel to stream data.
*/

package sshproto

import (
	"errors"
	"net"
	"time"

	ssh "golang.org/x/crypto/ssh"
)

type sshConn struct {
	ssh.Channel
	sshConn ssh.Conn
	bc      net.Conn
}

func NewServerConn(conn net.Conn, cfg *ssh.ServerConfig) (net.Conn, error) {
	svConn, sshChans, sshReqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return nil, err
	}
	go ssh.DiscardRequests(sshReqs)
	for newCh := range sshChans {
		switch newCh.ChannelType() {
		case "direct-tcpip":
			ch, reqs, err := newCh.Accept()
			if err != nil {
				_ = svConn.Close()
				return nil, err
			}
			go ssh.DiscardRequests(reqs)
			return &sshConn{Channel: ch, sshConn: svConn, bc: conn}, nil
		default:
			_ = newCh.Reject(ssh.UnknownChannelType, "unsupported channel type")
			return nil, errors.New("no supported ssh channel opened by client")
		}

	}
	_ = svConn.Close()
	return nil, errors.New("no ssh channel opened by client")
}

func NewClientConn(bc net.Conn, cfg *ssh.ClientConfig) (net.Conn, error) {
	clConn, _, sshReqs, err := ssh.NewClientConn(bc, "", cfg)
	if err != nil {
		return nil, err
	}
	go ssh.DiscardRequests(sshReqs)
	ch, reqs, err := clConn.OpenChannel("direct-tcpip", nil)
	if err != nil {
		_ = clConn.Close()
		return nil, err
	}
	go ssh.DiscardRequests(reqs)
	return &sshConn{Channel: ch, sshConn: clConn, bc: bc}, nil
}

func (c *sshConn) CloseWrite() error {
	err := c.Channel.CloseWrite()
	if bcCloseWrite, ok := c.bc.(interface{ CloseWrite() error }); ok {
		err = errors.Join(err, bcCloseWrite.CloseWrite())
	}
	return err
}

func (c *sshConn) Close() error {
	return errors.Join(c.Channel.Close(), c.sshConn.Close())
}

func (c *sshConn) LocalAddr() net.Addr                { return c.sshConn.LocalAddr() }
func (c *sshConn) RemoteAddr() net.Addr               { return c.sshConn.RemoteAddr() }
func (c *sshConn) SetDeadline(t time.Time) error      { return c.bc.SetDeadline(t) }
func (c *sshConn) SetReadDeadline(t time.Time) error  { return c.bc.SetReadDeadline(t) }
func (c *sshConn) SetWriteDeadline(t time.Time) error { return c.bc.SetWriteDeadline(t) }
