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
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/pedramktb/go-netx"
)

const serverMaxRead = 512

type serverConnCore struct {
	encoding *base32.Encoding
	domain   string
	maxWrite uint16
	buf      sync.Pool
}

type serverConn struct {
	conn net.Conn
	serverConnCore
}

type ServerOption func(*serverConnCore)

// WithMaxWrite sets the maximum ciphertext packet size accepted on server writes.
// Default is 765 bytes, which given a 255 byte QNAME is the maximum based on a UDP transport with an MTU of 1500.
func WithMaxWrite(size uint16) ServerOption {
	return func(c *serverConnCore) {
		c.maxWrite = size
	}
}

// NewServerConn creates a new DNST server connection.
// See how to use a DNST Tagged Conn:
// https://github.com/pedramktb/go-netx/blob/main/docs/mux-tag-poll.md
func NewServerConn(conn net.Conn, domain string, opts ...ServerOption) netx.TaggedConn {
	ds := &serverConn{
		conn: conn,
		serverConnCore: serverConnCore{
			encoding: base32.StdEncoding.WithPadding(base32.NoPadding),
			domain:   strings.TrimSuffix(domain, ".") + ".",
			maxWrite: 765,
			buf: sync.Pool{
				New: func() any {
					b := make([]byte, serverMaxRead)
					return &b
				},
			},
		},
	}
	for _, o := range opts {
		o(&ds.serverConnCore)
	}
	return ds
}

// MaxWrite returns the maximum raw payload that a single WriteTagged can carry in a TXT response.
func (c *serverConn) MaxWrite() uint16 { return c.maxWrite }

// ReadTagged reads a packet and returns the associated DNS query context.
func (c *serverConn) ReadTagged(b []byte, tag *any) (n int, err error) {
	bp := c.buf.Get().(*[]byte)
	buf := *bp
	defer c.buf.Put(bp)

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
	// Remove label-separator dots inserted by the client to form valid DNS labels.
	encoded = strings.ReplaceAll(encoded, ".", "")

	data, err := c.encoding.DecodeString(encoded)
	if err != nil {
		return 0, err
	}

	return copy(b, data), nil
}

// WriteTagged writes a packet using the provided DNS query context to form a response.
func (c *serverConn) WriteTagged(b []byte, tag any) (n int, err error) {
	reqMsg, ok := tag.(*dns.Msg)
	if !ok || reqMsg == nil {
		return 0, errors.New("invalid context for dnst write")
	}

	// Create Response
	resp := new(dns.Msg)
	resp.SetReply(reqMsg)
	resp.Compress = false

	// Split encoded string into chunks of 255 bytes max, as required by DNS TXT record format.
	encoded := c.encoding.EncodeToString(b)
	txt := &dns.TXT{
		Hdr: dns.RR_Header{Name: reqMsg.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 0},
		Txt: splitString(encoded, 255),
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

func (c *serverConn) Close() error                       { return c.conn.Close() }
func (c *serverConn) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *serverConn) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }
func (c *serverConn) SetDeadline(t time.Time) error      { return c.conn.SetDeadline(t) }
func (c *serverConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *serverConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

// serverConnTagged is the tag returned by taggedServerConn.ReadTagged.
// It carries both the parsed DNS message (for forming the reply) and the
// tag from the underlying TaggedConn (for routing the write back to the
// correct underlying connection, e.g. a specific TCP conn inside a Mux).
type serverConnTagged struct {
	dnsMsg  *dns.Msg
	connTag any
}

// taggedServerConn is like serverConn but operates on an underlying TaggedConn
// instead of a plain net.Conn so that the underlying-connection routing tag is
// preserved end-to-end through the DNST layer.
type taggedServerConn struct {
	conn netx.TaggedConn
	serverConnCore
}

// NewTaggedServerConn creates a new DNST server connection that operates on an
// underlying TaggedConn (e.g. a Mux that aggregates multiple TCP connections).
// The tag returned by ReadTagged is a serverConnTagged value that carries both
// the DNS message (to form the TXT response) and the forwarded tag from the
// underlying TaggedConn (to route the write back to the correct connection).
func NewTaggedServerConn(conn netx.TaggedConn, domain string, opts ...ServerOption) netx.TaggedConn {
	ds := &taggedServerConn{
		conn: conn,
		serverConnCore: serverConnCore{
			encoding: base32.StdEncoding.WithPadding(base32.NoPadding),
			domain:   strings.TrimSuffix(domain, ".") + ".",
			maxWrite: 765,
			buf: sync.Pool{
				New: func() any {
					b := make([]byte, serverMaxRead)
					return &b
				},
			},
		},
	}
	for _, o := range opts {
		o(&ds.serverConnCore)
	}
	return ds
}

func (c *taggedServerConn) MaxWrite() uint16 { return c.maxWrite }

func (c *taggedServerConn) ReadTagged(b []byte, tag *any) (n int, err error) {
	bp := c.buf.Get().(*[]byte)
	buf := *bp
	defer c.buf.Put(bp)

	var subTag any
	n, err = c.conn.ReadTagged(buf, &subTag)
	if err != nil {
		return 0, err
	}
	m := new(dns.Msg)
	if err := m.Unpack(buf[:n]); err != nil {
		return 0, err
	}
	if tag != nil {
		*tag = serverConnTagged{dnsMsg: m, connTag: subTag}
	}

	if len(m.Question) == 0 {
		return 0, nil
	}
	qName := m.Question[0].Name
	if !strings.HasSuffix(strings.ToLower(qName), c.domain) {
		return 0, errors.New("invalid domain")
	}
	encoded := qName[:len(qName)-len(c.domain)-1]
	encoded = strings.ReplaceAll(encoded, ".", "")

	data, err := c.encoding.DecodeString(encoded)
	if err != nil {
		return 0, err
	}
	return copy(b, data), nil
}

func (c *taggedServerConn) WriteTagged(b []byte, tag any) (n int, err error) {
	ct, ok := tag.(serverConnTagged)
	if !ok || ct.dnsMsg == nil {
		return 0, errors.New("invalid context for dnst tagged write")
	}
	reqMsg := ct.dnsMsg

	resp := new(dns.Msg)
	resp.SetReply(reqMsg)
	resp.Compress = false

	encoded := c.encoding.EncodeToString(b)
	txt := &dns.TXT{
		Hdr: dns.RR_Header{Name: reqMsg.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 0},
		Txt: splitString(encoded, 255),
	}
	resp.Answer = append(resp.Answer, txt)

	out, err := resp.Pack()
	if err != nil {
		return 0, err
	}
	if _, err := c.conn.WriteTagged(out, ct.connTag); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *taggedServerConn) Close() error                       { return c.conn.Close() }
func (c *taggedServerConn) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *taggedServerConn) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }
func (c *taggedServerConn) SetDeadline(t time.Time) error      { return c.conn.SetDeadline(t) }
func (c *taggedServerConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *taggedServerConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

type clientConn struct {
	net.Conn
	encoding *base32.Encoding
	domain   string
	maxWrite uint16
	buf      sync.Pool
}

// NewClientConn creates a new DNST client connection.
// MaxWrite is automatically computed from the domain length, accounting for
// Base32 encoding overhead and DNS QNAME label splitting.
func NewClientConn(conn net.Conn, domain string) net.Conn {
	dt := &clientConn{
		Conn:     conn,
		encoding: base32.StdEncoding.WithPadding(base32.NoPadding),
		domain:   strings.TrimSuffix(domain, "."),
		maxWrite: maxQNAMEPayload(strings.TrimSuffix(domain, ".")),
		buf: sync.Pool{
			New: func() any {
				b := make([]byte, netx.MaxPacketSize)
				return &b
			},
		},
	}
	return dt
}

// MaxWrite returns the maximum raw payload that a single Write can carry in a DNS query QNAME.
func (c *clientConn) MaxWrite() uint16 { return c.maxWrite }

func (c *clientConn) Read(b []byte) (n int, err error) {
	bp := c.buf.Get().(*[]byte)
	buf := *bp
	defer c.buf.Put(bp)

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

func (c *clientConn) Write(b []byte) (n int, err error) {
	encoded := c.encoding.EncodeToString(b)
	// Split encoded data into labels of max 63 bytes to comply with DNS label length limit.
	qname := splitString63(encoded) + "." + c.domain + "."
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

// maxQNAMEPayload calculates the maximum raw bytes that can be encoded into a DNS QNAME
// after Base32 encoding and label splitting, for a given domain suffix.
func maxQNAMEPayload(domain string) uint16 {
	// QNAME: splitString63(encoded) + "." + domain + "."
	// Length: (E + ceil(E/63) - 1) + 1 + len(domain) + 1 = E + ceil(E/63) + len(domain) + 1
	// Must be <= 253:
	//   E + ceil(E/63) <= 252 - len(domain)
	available := uint16(252 - len(domain))
	if available <= 0 {
		return 0
	}
	// Approximate: E * 64/63 ≈ available → E ≈ available * 63/64
	maxE := available * 63 / 64
	for maxE > 0 && maxE+(maxE+62)/63 > available {
		maxE--
	}
	// Base32 no-padding: 5 raw bytes → 8 encoded chars
	return maxE * 5 / 8
}

// splitString splits s into chunks of at most maxLen bytes.
func splitString(s string, maxLen int) []string {
	var parts []string
	for len(s) > maxLen {
		parts = append(parts, s[:maxLen])
		s = s[maxLen:]
	}
	if len(s) > 0 {
		parts = append(parts, s)
	}
	return parts
}

// splitString63 splits s into DNS labels of at most 63 bytes, joined by dots.
func splitString63(s string) string {
	if len(s) <= 63 {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i += 63 {
		if i > 0 {
			b.WriteByte('.')
		}
		end := i + 63
		if end > len(s) {
			end = len(s)
		}
		b.WriteString(s[i:end])
	}
	return b.String()
}
