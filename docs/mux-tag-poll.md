# Multiplexing, Tagging & Polling — Architecture Guide

This document describes the **Demux**, **TaggedConn / TaggedDemux**, **DemuxClient**, **PollConn**, and **DNST** modules and how they compose together to build persistent tunneled connections over stateless protocols like DNS.

---

## Table of Contents

- [Overview](#overview)
- [TaggedConn](#taggedconn)
- [Demux](#demux)
- [TaggedDemux](#taggeddemux)
- [DemuxClient](#demuxclient)
- [PollConn](#pollconn)
- [DNST (DNS Tunnel)](#dnst-dns-tunnel)
- [Full Stack: DNS Tunnel Example](#full-stack-dns-tunnel-example)
- [Data Flow Diagrams](#data-flow-diagrams)

---

## Overview

netx provides a set of composable layers that can be stacked to build tunnels over arbitrary transports. The core challenge addressed by these modules is:

> How do you create a persistent, multiplexed, bidirectional connection over a protocol that is stateless, request-response oriented, and cannot natively distinguish clients?

The answer is a layered architecture:

| Layer | Purpose | Interface |
|-------|---------|-----------|
| **DNST** | Encodes data inside DNS queries/responses | `TaggedConn` (server) / `net.Conn` (client) |
| **TaggedDemux** | Multiplexes sessions over a `TaggedConn`, preserving tags | `net.Listener` |
| **DemuxClient** | Client-side session framing (prepends/strips session ID) | `net.Conn` |
| **PollConn** | Converts request-response into persistent bidirectional stream | `net.Conn` |

---

## TaggedConn

**File:** [tagged_conn.go](../tagged_conn.go)

`TaggedConn` extends `net.Conn` with tagged read/write operations. A **tag** is an opaque `any` value that carries contextual metadata alongside data — for example, the original DNS query message that must be used to construct the matching response.

```go
type TaggedConn interface {
    ReadTagged([]byte, *any) (int, error)   // reads data + populates tag
    WriteTagged([]byte, any) (int, error)   // writes data using the provided tag
    Close() error
    LocalAddr() net.Addr
    RemoteAddr() net.Addr
    SetDeadline(t time.Time) error
    SetReadDeadline(t time.Time) error
    SetWriteDeadline(t time.Time) error
}
```

**Why it exists:** Some protocols require context from the read path to be available on the write path. In DNST, the server must reply to the exact DNS query it received — the `*dns.Msg` is carried as the tag. Without `TaggedConn`, this context would be lost when passing through intermediate layers like a demultiplexer.

**TaggedPipe** ([tagged_pipe.go](../tagged_pipe.go)) provides an in-memory `TaggedConn` pair for testing, analogous to `net.Pipe()`.

---

## Demux

**File:** [demux.go](../demux.go)

`Demux` is a connection multiplexer that runs over a plain `net.Conn`. It splits a single connection into multiple virtual sessions identified by a fixed-length ID prefix on every packet.

```go
func NewDemux(c net.Conn, idMask int, opts ...DemuxOption) net.Listener
```

**Packet format:**

```
[ Session ID (idMask bytes) ][ Payload ]
```

**Behavior:**
- A background `readLoop` reads packets from the underlying connection, extracts the session ID, and routes the payload to the corresponding session.
- New session IDs trigger creation of a virtual `net.Conn` that is delivered via `Accept()`.
- Each session has its own read queue; packets are dropped (not blocked) if the queue is full.

**Options:**

| Option | Default | Description |
|--------|---------|-------------|
| `WithDemuxAccQueueSize` | 0 (unbuffered) | Accept queue capacity for new sessions |
| `WithDemuxSessQueueSize` | 8 | Per-session read queue depth |
| `WithDemuxBufSize` | 4096 | Read buffer size for the underlying connection |

---

## TaggedDemux

**File:** [tagged_demux.go](../tagged_demux.go)

`TaggedDemux` is the tag-aware variant of `Demux`. It operates over a `TaggedConn` instead of a plain `net.Conn`, preserving the tag through the read→process→write cycle.

```go
func NewTaggedDemux(c TaggedConn, idMask int, opts ...DemuxOption) net.Listener
```

**Key difference from Demux:** When a packet is read via `ReadTagged`, the tag (e.g., the DNS query) is stored alongside the data in the session's read queue. When the session writes a response, it consumes a tag from its internal tag queue and passes it to `WriteTagged` on the underlying `TaggedConn`. This ensures each response is correctly associated with its originating request.

**Internal flow:**
1. `readLoop` calls `ReadTagged(buf, &tag)` on the underlying `TaggedConn`
2. Extracts session ID and payload
3. Routes `{payload, tag}` to the session's `rQueue`
4. On `session.Read()`, the tag is moved to `tagQueue`
5. On `session.Write()`, a tag is consumed from `tagQueue` and used in `WriteTagged`

This design allows arbitrary layers between the transport and the demux (e.g., encryption) as long as they propagate tags.

---

## DemuxClient

**File:** [demux_client.go](../demux_client.go)

`DemuxClient` is the client-side counterpart to `Demux` / `TaggedDemux`. It wraps a `net.Conn` and transparently prepends the session ID on writes and strips it on reads.

```go
func NewDemuxClient(c net.Conn, id []byte, opts ...DemuxClientOption) (net.Conn, error)
```

**Behavior:**
- `Write(b)` → sends `[id | b]` to the underlying connection
- `Read(b)` → reads from the underlying connection, validates and strips the ID prefix, returns the payload

This gives the caller a plain `net.Conn` scoped to a single session, while the server-side demux routes packets to the correct virtual connection.

**Options:**

| Option | Default | Description |
|--------|---------|-------------|
| `WithDemuxClientBufSize` | 4096 | Read/write buffer size |

---

## PollConn

**File:** [poll_conn.go](../poll_conn.go)

`PollConn` converts a **request-response** `net.Conn` into a **persistent bidirectional stream**. This is the critical bridge between stateless protocols (where each write must be followed by exactly one read) and the streaming model that applications expect.

```go
func NewPollConn(conn net.Conn, opts ...PollConnOption) net.Conn
```

**Problem it solves:** In DNS tunneling, the client can only receive data by sending a request first. If the server has data for the client but the client hasn't sent anything, that data is stuck. PollConn solves this by automatically sending empty poll requests at a configurable interval when idle.

**Internal loop:**

```
for {
    select data from sendQueue or wait pollInterval:
    
    conn.Write(data)     // send user data or empty poll
    conn.Read(buf)       // receive server response
    
    if response has data:
        push to recvQueue
}
```

**User-facing behavior:**
- `Write(b)` queues data into the send channel (non-blocking until the queue is full)
- `Read(b)` pulls from the receive channel, with unread-remainder tracking for partial reads
- Full deadline support via `SetReadDeadline` / `SetWriteDeadline`
- `Close()` terminates the poll loop and closes the underlying connection

**Options:**

| Option | Default | Description |
|--------|---------|-------------|
| `WithPollInterval` | 100ms | How often to poll when idle |
| `WithPollBufSize` | 4096 | Response read buffer size |
| `WithPollSendQueueSize` | 32 | Send queue capacity (backpressure) |
| `WithPollRecvQueueSize` | 32 | Receive queue capacity (backpressure) |

**Important:** The underlying connection must be wrapped so that even zero-length writes produce a valid round-trip. `DemuxClient` achieves this naturally — an empty write still sends the session ID header, which the server's demux processes and responds to.

---

## DNST (DNS Tunnel)

**File:** [proto/dnst/dnst_conn.go](../proto/dnst/dnst_conn.go)

DNST transports arbitrary data inside DNS queries and responses, allowing traffic to be routed through any public DNS resolver.

### Encoding

- **Client → Server (upstream):** Data is Base32-encoded into the QNAME (hostname) of a DNS TXT query. The domain suffix is appended (e.g., `JBSWY3DP.example.com.`).
- **Server → Client (downstream):** Data is Base32-encoded into the TXT record of the DNS response.

### Server: `NewDNSTServerConn`

```go
func NewDNSTServerConn(conn net.Conn, domain string, opts ...DNSTServerOption) TaggedConn
```

Returns a `TaggedConn` where:
- `ReadTagged` parses a DNS query, decodes the QNAME payload, and stores the `*dns.Msg` as the tag
- `WriteTagged` constructs a DNS response using the tag (original query) and encodes the payload into a TXT answer

### Client: `NewDNSTClientConn`

```go
func NewDNSTClientConn(conn net.Conn, domain string, opts ...DNSTClientOption) net.Conn
```

Returns a plain `net.Conn` where:
- `Write` encodes data into a DNS TXT query and sends it
- `Read` parses the DNS response and decodes the TXT record payload

### Limitations

Each DNS query-response pair carries a single message. The connection is inherently stateless — the server cannot distinguish clients or maintain sessions without relying on the payload content. This is why DNST must be composed with the other layers described here.

---

## Full Stack: DNS Tunnel Example

A complete persistent tunnel over DNS composes the layers as follows:

### Server Side

```
Transport (UDP/TCP) → DNSTServerConn (TaggedConn) → TaggedDemux (net.Listener) → Accept() sessions
```

```go
// Accept a raw connection (e.g., from a UDP/TCP DNS listener)
serverTagged := dnst.NewDNSTServerConn(rawConn, "tunnel.example.com")

// Demux into sessions (4-byte session ID prefix)
listener := netx.NewTaggedDemux(serverTagged, 4, netx.WithDemuxAccQueueSize(16))

// Handle sessions
for {
    sess, _ := listener.Accept()
    go handleSession(sess) // sess is a plain net.Conn
}
```

### Client Side

```
Transport (UDP/TCP) → DNSTClientConn (net.Conn) → DemuxClient (net.Conn) → PollConn (net.Conn)
```

```go
// Connect to DNS resolver (or directly to tunnel server)
transport, _ := net.Dial("udp", "8.8.8.8:53")

// Wrap in DNST
dnstConn := dnst.NewDNSTClientConn(transport, "tunnel.example.com")

// Add session framing
demuxClient, _ := netx.NewDemuxClient(dnstConn, []byte("SES1"))

// Make it persistent with polling
persistent := netx.NewPollConn(demuxClient, netx.WithPollInterval(50*time.Millisecond))

// Use as a regular net.Conn
persistent.Write([]byte("hello"))
persistent.Read(buf)
```

---

## Data Flow Diagrams

### Client Write (upstream)

```
Application
    │ Write("hello")
    ▼
PollConn ──── queues data, sends on next cycle
    │ Write("hello")
    ▼
DemuxClient ──── prepends session ID
    │ Write("SES1" + "hello")
    ▼
DNSTClientConn ──── Base32-encodes into DNS query QNAME
    │ DNS Query: KNSXG5BRGEZSA.tunnel.example.com TXT?
    ▼
Transport (UDP/TCP) ──── sends to resolver / server
```

### Server Read + Write (downstream)

```
Transport (UDP/TCP)
    │ DNS Query arrives
    ▼
DNSTServerConn ──── parses query, decodes QNAME, tag = *dns.Msg
    │ ReadTagged → data="SES1hello", tag=query
    ▼
TaggedDemux ──── extracts "SES1", routes payload, stores tag
    │ session.Read → data="hello", tag queued
    ▼
Application (session handler)
    │ Write("world")
    ▼
TaggedDemux session ──── consumes tag, prepends "SES1"
    │ WriteTagged("SES1world", tag=original query)
    ▼
DNSTServerConn ──── constructs DNS response with TXT record
    │ DNS Response with TXT: Base32("SES1world")
    ▼
Transport (UDP/TCP) ──── sends response back
```

### Client Read (downstream)

```
Transport (UDP/TCP)
    │ DNS Response arrives
    ▼
DNSTClientConn ──── parses response, decodes TXT record
    │ Read → "SES1world"
    ▼
DemuxClient ──── validates & strips session ID
    │ Read → "world"
    ▼
PollConn ──── buffers in recvQueue
    │ Read → "world"
    ▼
Application
```

### Idle Poll Cycle

When the application has nothing to send, PollConn keeps the connection alive:

```
PollConn (poll interval elapsed, no queued data)
    │ Write(nil)           ← empty write
    ▼
DemuxClient
    │ Write("SES1")        ← just the session ID, no payload
    ▼
DNSTClientConn
    │ DNS Query: KNSXG5A.tunnel.example.com TXT?
    ▼
   ... round-trip ...
    ▼
PollConn
    │ Read → server data (if any) or empty response
    ▼
Application (Read returns when data is available)
```

This polling mechanism is what allows the server to push data to the client at any time — the client always has a pending request that the server can respond to with queued data.
