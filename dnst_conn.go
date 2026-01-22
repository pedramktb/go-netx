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
	"bytes"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/miekg/dns"
)

type dnstClientConn struct {
	net.Conn
	domain  string
	readBuf bytes.Buffer
}

// NewDNSTClientConn creates a new DNST client connection.
func NewDNSTClientConn(conn net.Conn, domain string) net.Conn {
	return &dnstClientConn{
		Conn:   conn,
		domain: strings.TrimSuffix(domain, ".") + ".",
	}
}

func (c *dnstClientConn) Read(b []byte) (n int, err error) {
	// If we have leftovers from a previous packet, return those.
	if c.readBuf.Len() > 0 {
		return c.readBuf.Read(b)
	}

	// Read from the underlying connection
	buf := make([]byte, 2048)
	n, err = c.Conn.Read(buf)
	if err != nil {
		return 0, err
	}
	pkt := buf[:n]

	msg := new(dns.Msg)
	if err := msg.Unpack(pkt); err != nil {
		// return nil (0 bytes read) to ignore garbage packets instead of throwing error
		return 0, nil
	}

	var payload []byte
	// CLIENT SIDE: Extract payload from TXT in Answer
	for _, ans := range msg.Answer {
		if txt, ok := ans.(*dns.TXT); ok {
			joined := strings.Join(txt.Txt, "")
			if p, err := hex.DecodeString(joined); err == nil {
				payload = append(payload, p...)
			} else {
				// Fallback to raw bytes?
				payload = append(payload, []byte(joined)...)
			}
		}
	}

	if len(payload) > 0 {
		c.readBuf.Write(payload)
	}

	return c.readBuf.Read(b)
}

func (c *dnstClientConn) Write(b []byte) (int, error) {
	// Send ONE DNS Query packet containing as much of b as fits (or default chunk size)
	chunkSize := 64
	n := min(chunkSize, len(b))
	chunk := b[:n]

	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(chunk)
	// Split into 63 char labels
	var labels []string
	encLen := len(encoded)
	for encLen > 0 {
		k := min(63, encLen)
		labels = append(labels, encoded[:k])
		encoded = encoded[k:]
		encLen = len(encoded)
	}

	qname := strings.Join(labels, ".") + "." + c.domain

	msg := new(dns.Msg)
	msg.SetQuestion(qname, dns.TypeTXT)
	msg.Id = dns.Id()
	msg.RecursionDesired = true

	packed, err := msg.Pack()
	if err != nil {
		return 0, err
	}

	_, err = c.Conn.Write(packed)
	if err != nil {
		return 0, err
	}

	return n, nil
}

// dnsServerConn handles the server side of the DNST.
type dnstServerConn struct {
	net.Conn
	domain string

	// Buffer for payload extracted from queries
	readBuf bytes.Buffer
	// The query we are currently processing/replying to
	lastQuery *dns.Msg
}

// NewDNSTServerConn creates a new DNST server connection.
func NewDNSTServerConn(conn net.Conn, domain string) net.Conn {
	return &dnstServerConn{
		Conn:   conn,
		domain: strings.TrimSuffix(domain, ".") + ".",
	}
}

func (c *dnstServerConn) Read(b []byte) (n int, err error) {
	// If we have leftovers from a previous packet, return those.
	if c.readBuf.Len() > 0 {
		return c.readBuf.Read(b)
	}

	// Read from the underlying connection
	buf := make([]byte, 2048)
	n, err = c.Conn.Read(buf)
	if err != nil {
		return 0, err
	}
	pkt := buf[:n]

	msg := new(dns.Msg)
	if err := msg.Unpack(pkt); err != nil {
		// If we can't unpack, return 0 for garbage/invalid packet
		return 0, nil
	}

	var payload []byte

	// SERVER SIDE: Extract payload from Query Name
	if len(msg.Question) > 0 {
		q := msg.Question[0]
		name := strings.ToLower(q.Name)
		if strings.HasSuffix(name, c.domain) {
			encoded := strings.TrimSuffix(name, c.domain)
			encoded = strings.TrimSuffix(encoded, ".")
			encoded = strings.ReplaceAll(encoded, ".", "")

			if p, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(encoded)); err == nil {
				payload = p
			} else if p, err := base32.StdEncoding.DecodeString(strings.ToUpper(encoded)); err == nil {
				payload = p
			} else {
				fmt.Printf("Decode error: %v for %s\n", err, encoded)
			}
		} else {
			fmt.Printf("Suffix mismatch. Name: %s Domain: %s\n", name, c.domain)
		}
		// Store the query so Write() can use it to reply
		c.lastQuery = msg
	}

	if len(payload) > 0 {
		c.readBuf.Write(payload)
	}

	return c.readBuf.Read(b)
}

func (c *dnstServerConn) Write(b []byte) (int, error) {
	if c.lastQuery == nil {
		return 0, io.ErrShortWrite // Or a specific "no active query" error
	}

	// Reply to the single active query we have
	chunkSize := 128
	n := min(chunkSize, len(b))
	chunk := b[:n]

	resp := new(dns.Msg)
	resp.SetReply(c.lastQuery)

	encoded := hex.EncodeToString(chunk)

	txt := &dns.TXT{
		Hdr: dns.RR_Header{
			Name:   c.lastQuery.Question[0].Name,
			Rrtype: dns.TypeTXT,
			Class:  dns.ClassINET,
			Ttl:    0,
		},
		Txt: []string{encoded},
	}
	resp.Answer = append(resp.Answer, txt)

	packed, err := resp.Pack()
	if err != nil {
		return 0, err
	}

	_, err = c.Conn.Write(packed)
	if err != nil {
		return 0, err
	}

	c.lastQuery = nil // Consumed
	return n, nil
}
