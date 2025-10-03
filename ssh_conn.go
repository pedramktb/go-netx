package netx

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

func (s *sshConn) LocalAddr() net.Addr                { return s.sshConn.LocalAddr() }
func (s *sshConn) RemoteAddr() net.Addr               { return s.sshConn.RemoteAddr() }
func (s *sshConn) SetDeadline(t time.Time) error      { return s.bc.SetDeadline(t) }
func (s *sshConn) SetReadDeadline(t time.Time) error  { return s.bc.SetReadDeadline(t) }
func (s *sshConn) SetWriteDeadline(t time.Time) error { return s.bc.SetWriteDeadline(t) }
func (s *sshConn) Close() error {
	return errors.Join(s.Channel.Close(), s.sshConn.Close())
}
func (s *sshConn) CloseWrite() error {
	err := s.Channel.CloseWrite()
	if bcCloseWrite, ok := s.bc.(interface{ CloseWrite() error }); ok {
		err = errors.Join(err, bcCloseWrite.CloseWrite())
	}
	return err
}

func NewSSHServerConn(bc net.Conn, cfg *ssh.ServerConfig) (net.Conn, error) {
	svConn, sshChans, sshReqs, err := ssh.NewServerConn(bc, cfg)
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
			return &sshConn{Channel: ch, sshConn: svConn, bc: bc}, nil
		default:
			_ = newCh.Reject(ssh.UnknownChannelType, "unsupported channel type")
			return nil, errors.New("no supported ssh channel opened by client")
		}

	}
	_ = svConn.Close()
	return nil, errors.New("no ssh channel opened by client")
}

func NewSSHClientConn(bc net.Conn, cfg *ssh.ClientConfig) (net.Conn, error) {
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
