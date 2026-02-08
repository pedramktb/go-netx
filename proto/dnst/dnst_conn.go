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

	"github.com/miekg/dns"
	"github.com/pedramktb/go-netx"
)

type dnstServerConn struct {
	net.Conn
	encoding           *base32.Encoding
	domain             string
	maxReadPacketSize  int
	maxWritePacketSize int
	dnsConn            *dns.Conn
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
func NewDNSTServerConn(conn net.Conn, domain string, opts ...DNSTServerOption) netx.CtxConn {
	ds := &dnstServerConn{
		Conn:               conn,
		encoding:           base32.StdEncoding.WithPadding(base32.NoPadding),
		domain:             strings.TrimSuffix(domain, ".") + ".",
		maxReadPacketSize:  512,
		maxWritePacketSize: 765,
		dnsConn:            &dns.Conn{Conn: conn},
	}
	for _, opt := range opts {
		opt(ds)
	}
	return ds
}

// ReadCtx reads a packet and returns the associated DNS query context.
func (c *dnstServerConn) ReadCtx(b []byte) (n int, ctx any, err error) {
	m, err := c.dnsConn.ReadMsg()
	if err != nil {
		return 0, nil, err
	}

	if len(m.Question) == 0 {
		return 0, nil, nil
	}
	qName := m.Question[0].Name
	if !strings.HasSuffix(strings.ToLower(qName), c.domain) {
		return 0, nil, errors.New("invalid domain")
	}
	encoded := qName[:len(qName)-len(c.domain)-1]

	data, err := c.encoding.DecodeString(encoded)
	if err != nil {
		return 0, nil, err
	}

	return copy(b, data), m, nil
}

func (c *dnstServerConn) Read(b []byte) (n int, err error) {
	n, _, err = c.ReadCtx(b)
	return n, err
}

// WriteCtx writes a packet using the provided DNS query context to form a response.
func (c *dnstServerConn) WriteCtx(b []byte, ctx any) (n int, err error) {
	reqMsg, ok := ctx.(*dns.Msg)
	if !ok || reqMsg == nil {
		return 0, errors.New("invalid context for dnst write")
	}

	// Create Response
	resp := new(dns.Msg)
	resp.SetReply(reqMsg)
	resp.Compress = false

	encoded := c.encoding.EncodeToString(b)

	txt := &dns.TXT{
		Hdr: dns.RR_Header{Name: reqMsg.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 0},
		Txt: []string{encoded},
	}
	resp.Answer = append(resp.Answer, txt)

	if err := c.dnsConn.WriteMsg(resp); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *dnstServerConn) Write(b []byte) (n int, err error) {
	return 0, errors.New("dnst server requires WriteCtx with a valid DNS request")
}

type dnstClientConn struct {
	net.Conn
	encoding           *base32.Encoding
	domain             string
	maxReadPacketSize  int
	maxWritePacketSize int
	dnsConn            *dns.Conn
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
		dnsConn:            &dns.Conn{Conn: conn},
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

	// Sending the query
	if err := c.dnsConn.WriteMsg(m); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *dnstClientConn) Read(b []byte) (n int, err error) {
	m, err := c.dnsConn.ReadMsg()
	if err != nil {
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
