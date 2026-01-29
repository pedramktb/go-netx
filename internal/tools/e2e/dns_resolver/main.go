package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

var (
	listenAddr = flag.String("listen", ":5353", "Listen address")
	upstream   = flag.String("upstream", "127.0.0.1:53", "Authoritative Nameserver address")
	domain     = flag.String("domain", "t.com.", "Domain to forward")

	upstreamClient *dns.Client
	upstreamConn   *dns.Conn
	upstreamMu     sync.Mutex
)

func main() {
	flag.Parse()

	// Ensure domain ends with dot
	d := *domain
	if !strings.HasSuffix(d, ".") {
		d += "."
	}
	*domain = d

	// Initialize upstream connection
	upstreamClient = new(dns.Client)
	upstreamClient.Net = "udp"
	upstreamClient.Timeout = 2 * time.Second

	// We establish a persistent connection to the upstream to maintain stable 5-tuple (Source Port)
	// This is required because go-netx dnst transport relies on session stability.
	c, err := net.Dial("udp", *upstream)
	if err != nil {
		log.Fatalf("Failed to dial upstream: %v", err)
	}
	upstreamConn = &dns.Conn{Conn: c}

	server := &dns.Server{Addr: *listenAddr, Net: "udp"}
	server.Handler = dns.HandlerFunc(handleDNS)

	fmt.Printf("Starting Mock DNS Resolver on %s, forwarding zone %s to %s\n", *listenAddr, *domain, *upstream)

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Failed to start server: %s\n", err.Error())
	}
}

func handleDNS(w dns.ResponseWriter, r *dns.Msg) {
	if len(r.Question) == 0 {
		return
	}

	q := r.Question[0]
	name := strings.ToLower(q.Name)

	log.Printf("Received query: %s", name)

	// Check if authoritative
	if strings.HasSuffix(name, *domain) {
		// Forward to upstream using persistent connection
		upstreamMu.Lock()
		defer upstreamMu.Unlock()

		upstreamConn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		upstreamConn.SetReadDeadline(time.Now().Add(2 * time.Second))

		if err := upstreamConn.WriteMsg(r); err != nil {
			log.Printf("Upstream write error: %v", err)
			dns.HandleFailed(w, r)
			return
		}
		log.Printf("Forwarded query to upstream")

		resp, err := upstreamConn.ReadMsg()
		if err != nil {
			log.Printf("Upstream read error: %v", err)
			dns.HandleFailed(w, r)
			return
		}
		log.Printf("Received response from upstream")

		// Ensure response ID matches request ID?
		// Since we lock, it should match.
		// If upstream changed ID (unlikely), we might propagate it.
		// But usually we should restore the original ID for the requester?
		// dns.Client handles this, but here we do manual.
		// `resp.Id` comes from upstream. `r.Id` came from client.
		// If upstream is standard, it reflects ID.
		// If upstream ignores ID or changes it, client might reject.
		// But here `upstream` is `netx` authoritative server. It should reflect ID.
		log.Printf("Writing response to %s", w.RemoteAddr())
		w.WriteMsg(resp)
	} else {
		// Not authoritative
		log.Printf("Query for %s not in zone %s", name, *domain)
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeNameError)
		w.WriteMsg(m)
	}
}
