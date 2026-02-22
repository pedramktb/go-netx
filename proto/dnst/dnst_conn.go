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
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
	"github.com/pedramktb/go-netx"
)

type dnstServerConn struct {
	conn               net.Conn
	encoding           *base32.Encoding
	domain             string
	maxReadPacketSize  int
	maxWritePacketSize int
}

type DNSTServerOption func(*dnstServerConn)

// WithDNSTMaxWritePacket sets the maximum ciphertext packet size accepted on write.
// Default is 765 bytes, which given a 255 byte QNAME is the maximum based on a UDP transport with an MTU of 1500.
func WithDNSTMaxWritePacket(size uint32) DNSTServerOption {
	return func(c *dnstServerConn) {
		c.maxWritePacketSize = int(size)
	}
}

// NewDNSTServerConn creates a new DNST server connection.
// See how to use a DNST Tagged Conn:
// https://github.com/pedramktb/go-netx/blob/main/docs/mux-tag-poll.md
func NewDNSTServerConn(conn net.Conn, domain string, opts ...DNSTServerOption) netx.TaggedConn {
	ds := &dnstServerConn{
		conn:               conn,
		encoding:           base32.StdEncoding.WithPadding(base32.NoPadding),
		domain:             strings.TrimSuffix(domain, ".") + ".",
		maxReadPacketSize:  512,
		maxWritePacketSize: 765,
	}
	for _, opt := range opts {
		opt(ds)
	}
	return ds
}

// ReadTagged reads a packet and returns the associated DNS query context.
func (c *dnstServerConn) ReadTagged(b []byte, tag *any) (n int, err error) {
	buf := make([]byte, c.maxReadPacketSize)
	n, err = c.conn.Read(buf)
	if err != nil {
		return 0, err
	}
	m := new(dns.Msg)
	if err := m.Unpack(buf[:n]); err != nil {
		return 0, err
	}
	*tag = m

	if len(m.Question) == 0 {
		return 0, nil
	}
	qName := m.Question[0].Name
	if !strings.HasSuffix(strings.ToLower(qName), c.domain) {
		return 0, errors.New("invalid domain")
	}
	encoded := qName[:len(qName)-len(c.domain)-1]

	data, err := c.encoding.DecodeString(encoded)
	if err != nil {
		return 0, err
	}

	return copy(b, data), nil
}

// WriteTagged writes a packet using the provided DNS query context to form a response.
func (c *dnstServerConn) WriteTagged(b []byte, tag any) (n int, err error) {
	reqMsg, ok := tag.(*dns.Msg)
	if !ok || reqMsg == nil {
		return 0, errors.New("invalid context for dnst write")
	}

	// Create Response
	resp := new(dns.Msg)
	resp.SetReply(reqMsg)
	resp.Compress = false

	txt := &dns.TXT{
		Hdr: dns.RR_Header{Name: reqMsg.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 0},
		Txt: []string{c.encoding.EncodeToString(b)},
	}
	resp.Answer = append(resp.Answer, txt)

	out, err := resp.Pack()
	if err != nil {
		return 0, err
	}
	if _, err := c.conn.Write(out); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *dnstServerConn) Close() error                       { return c.conn.Close() }
func (c *dnstServerConn) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *dnstServerConn) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }
func (c *dnstServerConn) SetDeadline(t time.Time) error      { return c.conn.SetDeadline(t) }
func (c *dnstServerConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *dnstServerConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

type dnstClientConn struct {
	net.Conn
	encoding           *base32.Encoding
	domain             string
	maxReadPacketSize  int
	maxWritePacketSize int
}

type DNSTClientOption func(*dnstClientConn)

// WithDNSTMaxReadPacket sets the maximum ciphertext packet size accepted on Read.
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

func (c *dnstClientConn) Write(b []byte) (n int, err error) {
	encoded := c.encoding.EncodeToString(b)
	qname := encoded + "." + c.domain + "."
	if len(qname) > 253 {
		return 0, errors.New("dns packet too long")
	}

	m := new(dns.Msg)
	m.SetQuestion(qname, dns.TypeTXT)
	m.Id = dns.Id()
	m.RecursionDesired = true

	out, err := m.Pack()
	if err != nil {
		return 0, err
	}
	if _, err := c.Conn.Write(out); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *dnstClientConn) Read(b []byte) (n int, err error) {
	buf := make([]byte, c.maxReadPacketSize)
	n, err = c.Conn.Read(buf)
	if err != nil {
		return 0, err
	}
	m := new(dns.Msg)
	if err := m.Unpack(buf[:n]); err != nil {
		return 0, err
	}
	if len(m.Answer) == 0 {
		return 0, nil
	}
	// Extract TXT
	txtRR, ok := m.Answer[0].(*dns.TXT)
	if !ok {
		return 0, errors.New("invalid dns response type")
	}
	if len(txtRR.Txt) == 0 {
		return 0, nil
	}
	dataStr := strings.Join(txtRR.Txt, "")

	decoded, err := c.encoding.DecodeString(dataStr)
	if err != nil {
		return 0, err
	}
	return copy(b, decoded), nil
}
