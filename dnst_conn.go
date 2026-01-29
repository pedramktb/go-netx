/*

DNST is a layer that transports data by encapsulating it within DNS queries and responses.
It operates by encoding data into the hostname of a DNS query (Client -> Server) and receiving data
embedded in the TXT record of the DNS response (Server -> Client). This allows routing data through
any public DNS resolver.

Note: The connection is only valid for a single request and response and cannot distinguish clients
without relying on the payload.

Based on "DNS Tunnel - through bastion hosts" by Oskar Pearson.
Ref: https://web.archive.org/web/20200208203702/http://gray-world.net/papers/dnstunnel.txt

*/

package netx

import (
	"encoding/base32"
	"errors"
	"io"
	"net"
	"strings"

	"github.com/miekg/dns"
)

type dnstClientConn struct {
	net.Conn
	encoding           *base32.Encoding
	domain             string
	maxReadPacketSize  int
	maxWritePacketSize int
}

type DNSTClientOption func(*dnstClientConn)

// WithMaxReadPacket sets the maximum ciphertext packet size accepted on Read.
// Default is 765 bytes, which given a 255 byte QNAME is the maximum based on a UDP transport with an MTU of 1500.
func WithDNSTMaxReadPacket(size uint32) DNSTClientOption {
	return func(c *dnstClientConn) {
		c.maxReadPacketSize = int(size)
	}
}

// NewDNSTClientConn creates a new DNST client connection.
// MaxWritePacketSize is set based on the domain length to fit within DNS limits. (This does not account for Base32 overhead.)
func NewDNSTClientConn(conn net.Conn, domain string, opts ...DNSTClientOption) net.Conn {
	dt := &dnstClientConn{
		Conn:               conn,
		encoding:           base32.StdEncoding.WithPadding(base32.NoPadding),
		domain:             strings.TrimSuffix(domain, "."),
		maxReadPacketSize:  765,
		maxWritePacketSize: 255 - len(domain) - 4, // -4 for periods added in encoding
	}
	for _, opt := range opts {
		opt(dt)
	}
	return dt
}

func (c *dnstClientConn) Read(b []byte) (n int, err error) {
	buf := make([]byte, c.maxReadPacketSize)
	n, err = c.Conn.Read(buf)
	if err != nil {
		return 0, err
	}
	msg := new(dns.Msg)
	err = msg.Unpack(buf[:n])
	if err != nil {
		return 0, err
	}
	if len(msg.Answer) == 0 {
		return 0, errors.New("dnst: no answer in DNS response")
	}
	if txt, ok := msg.Answer[0].(*dns.TXT); ok {
		if len(txt.Txt) == 0 {
			return 0, errors.New("dnst: no TXT record in DNS answer")
		}
		if len(txt.Txt[0]) > len(b) {
			return len(b), io.ErrShortBuffer
		}
		copy(b, txt.Txt[0])
		return len(txt.Txt[0]), nil
	}
	return 0, errors.New("dnst: no TXT record in DNS answer")
}

func (c *dnstClientConn) Write(b []byte) (n int, err error) {
	data := c.encoding.EncodeToString(b)

	// Split data into 63-character chunks (DNS label limit)
	var parts []string
	for len(data) > 63 {
		parts = append(parts, data[:63])
		data = data[63:]
	}
	parts = append(parts, data)
	encoded := strings.Join(parts, ".")

	if len(encoded) > c.maxWritePacketSize {
		return 0, errors.New("dnst: packet may be truncated; increase maxWritePacketSize or frame the packets")
	}
	msg := new(dns.Msg)
	msg.SetQuestion(strings.ToLower(encoded+"."+c.domain+"."), dns.TypeTXT)
	buf, err := msg.Pack()
	if err != nil {
		return 0, err
	}
	n, err = c.Conn.Write(buf)
	if err != nil {
		return 0, err
	}
	if n != len(buf) {
		return 0, io.ErrShortWrite
	}
	return len(b), nil
}

type dnstServerConn struct {
	net.Conn
	encoding           *base32.Encoding
	domain             string
	maxReadPacketSize  int
	maxWritePacketSize int
}
type DNSTServerOption func(*dnstServerConn)

// WithMaxWritePacket sets the maximum ciphertext packet size accepted on write.
// Default is 765 bytes, which given a 255 byte QNAME is the maximum based on a UDP transport with an MTU of 1500.
func WithDNSTMaxWritePacket(size uint32) DNSTServerOption {
	return func(c *dnstServerConn) {
		c.maxWritePacketSize = int(size)
	}
}

// NewDNSTServerConn creates a new DNST server connection.
func NewDNSTServerConn(conn net.Conn, domain string) net.Conn {
	return &dnstServerConn{
		Conn:               conn,
		encoding:           base32.StdEncoding.WithPadding(base32.NoPadding),
		domain:             strings.TrimSuffix(domain, ".") + ".",
		maxReadPacketSize:  512,
		maxWritePacketSize: 765,
	}
}

func (c *dnstServerConn) Read(b []byte) (n int, err error) {
	buf := make([]byte, c.maxReadPacketSize)
	n, err = c.Conn.Read(buf)
	if err != nil {
		return 0, err
	}
	msg := new(dns.Msg)
	err = msg.Unpack(buf[:n])
	if err != nil {
		return 0, err
	}
	if len(msg.Question) == 0 {
		return 0, errors.New("dnst: no question in DNS query")
	}
	q := msg.Question[0]
	// Remove domain suffix
	data := strings.TrimSuffix(strings.ToLower(q.Name), "."+strings.ToLower(c.domain))
	data = strings.TrimSuffix(data, ".")
	// Remove dots from label splitting
	data = strings.ReplaceAll(data, ".", "")
	// Normalize to upper case for base32 decoding
	data = strings.ToUpper(data)

	decoded, err := c.encoding.DecodeString(data)
	if err != nil {
		return 0, err
	}
	if len(decoded) > len(b) {
		return len(b), io.ErrShortBuffer
	}
	copy(b, decoded)
	return len(decoded), nil
}

func (c *dnstServerConn) Write(b []byte) (n int, err error) {
	if len(b) > c.maxWritePacketSize {
		return 0, errors.New("dnst: packet may be truncated; increase maxWritePacketSize or frame the packets")
	}
	txt := &dns.TXT{
		Hdr: dns.RR_Header{
			Name:   c.domain,
			Rrtype: dns.TypeTXT,
			Class:  dns.ClassINET,
			Ttl:    0,
		},
		Txt: []string{string(b)},
	}
	msg := new(dns.Msg)
	msg.Answer = []dns.RR{txt}
	buf, err := msg.Pack()
	if err != nil {
		return 0, err
	}
	n, err = c.Conn.Write(buf)
	if err != nil {
		return 0, err
	}
	if n != len(buf) {
		return 0, io.ErrShortWrite
	}
	return len(b), nil
}
