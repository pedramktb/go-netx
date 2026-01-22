/*
ICMPConn is a network layer that tunnels traffic over ICMP Echo Request/Reply packets.
It can operate in client mode (sending Echo Requests) or server mode (replying with Echo Replies).
This allows bypassing firewalls that allow ICMP traffic but block other protocols.
It handles packet identification and sequencing to map ICMP packets to the stream.
*/

package netx

import (
	"crypto/sha256"
	"io"
	"net"
	"sync"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

type icmpConn struct {
	net.Conn
	ipV
	reply bool
	mutex *sync.RWMutex
	// For client connections to remember sent packets and discard the auto-replies from the OS on the server.
	sentHashes map[uint8][32]byte
	id         uint16 // Last seen identifier for server connections.
	seq        uint16 // Last seen sequence number for server connections.
}

func (c *icmpConn) rememberSent(seq uint16, data []byte) {
	digest := sha256.Sum256(data)
	c.mutex.Lock()
	c.sentHashes[uint8(seq%256)] = digest
	c.mutex.Unlock()
}

func (c *icmpConn) consumeSent(seq uint16, data []byte) bool {
	digest := sha256.Sum256(data)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.sentHashes[uint8(seq%256)] == digest
}

func NewICMPClientConn(conn net.Conn, version ipV) (net.Conn, error) {
	return &icmpConn{
		Conn:       conn,
		ipV:        version,
		reply:      false,
		mutex:      &sync.RWMutex{},
		id:         1,
		seq:        1,
		sentHashes: make(map[uint8][32]byte, 256),
	}, nil
}

func NewICMPServerConn(conn net.Conn, version ipV) (net.Conn, error) {
	return &icmpConn{
		Conn:  conn,
		ipV:   version,
		reply: true,
		mutex: &sync.RWMutex{},
	}, nil
}

func (c *icmpConn) Read(b []byte) (n int, err error) {
	for {
		n, err = c.Conn.Read(b)
		if err != nil {
			return 0, err
		}
		var msg *icmp.Message
		if c.ipV == IPv6 {
			if c.reply {
				msg, err = icmp.ParseMessage(58, b[:n])
			} else {
				if len(b) < 40 {
					return 0, io.ErrShortBuffer
				} else if n < 40 {
					return 0, io.ErrUnexpectedEOF
				}
				msg, err = icmp.ParseMessage(58, b[40:n])
			}
		} else {
			if c.reply {
				msg, err = icmp.ParseMessage(1, b[:n])
			} else {
				if len(b) < 20 {
					return 0, io.ErrShortBuffer
				} else if n < 20 {
					return 0, io.ErrUnexpectedEOF
				}
				msg, err = icmp.ParseMessage(1, b[20:n])
			}
		}
		if err != nil {
			return 0, err
		}
		switch pkt := msg.Body.(type) {
		case *icmp.Echo:
			if c.reply {
				c.mutex.Lock()
				c.id = uint16(pkt.ID)
				c.seq = uint16(pkt.Seq)
				c.mutex.Unlock()
			}
			n = copy(b, pkt.Data)
			if !c.reply && c.consumeSent(uint16(pkt.Seq), b[:n]) {
				continue
			}
			return n, nil
		default:
			return 0, io.ErrUnexpectedEOF
		}
	}
}

func (c *icmpConn) Write(b []byte) (n int, err error) {
	var msgType icmp.Type
	if c.reply {
		if c.ipV == IPv6 {
			msgType = ipv6.ICMPTypeEchoReply
		} else {
			msgType = ipv4.ICMPTypeEchoReply
		}
	} else {
		if c.ipV == IPv6 {
			msgType = ipv6.ICMPTypeEchoRequest
		} else {
			msgType = ipv4.ICMPTypeEcho
		}
	}

	var id, seq uint16
	if !c.reply {
		id = c.id
		c.mutex.Lock()
		c.seq++
		seq = c.seq
		c.mutex.Unlock()
		c.rememberSent(seq, b)
	} else {
		c.mutex.RLock()
		seq = c.seq
		id = c.id
		c.mutex.RUnlock()
	}
	msg := icmp.Message{
		Type: msgType,
		Code: 0,
		Body: &icmp.Echo{
			ID:   int(id),
			Seq:  int(seq),
			Data: b,
		},
	}
	msgBytes, err := msg.Marshal(nil)
	if err != nil {
		return 0, err
	}
	n, err = c.Conn.Write(msgBytes)
	if err != nil {
		return n, err
	}
	if n < len(msgBytes) {
		return n, io.ErrShortWrite
	}
	return len(b), nil
}
