# <img src="./netx.svg" alt="netX" />

netX or network extended is a collection of small, focused extensions to Go's "net" standard library.

It provides composable building blocks that integrate well with standard `net.Conn` and `net.Listener` types without introducing heavy abstractions,
only introducing new interfaces and types where necessary to expose real functionality.

## Contents
- [Highlights](#highlights)
- [Installation](#installation)
- [Library guide](#library-guide)
	- [Buffered connections](#buffered-connections)
	- [Framed connections](#framed-connections)
	- [Mux and MuxClient](#mux-and-muxclient)
	- [Demux and DemuxClient](#demux-and-demuxclient)
	- [Poll connections](#poll-connections)
	- [Tagged connections](#tagged-connections)
	- [Runtime-routable server](#runtime-routable-server)
	- [Tunneling](#tunneling)
	- [Driver and wrapper system](#driver-and-wrapper-system)
	- [Programmatic URIs](#programmatic-uris)
	- [Logging](#logging)
	- [Design notes and guarantees](#design-notes-and-guarantees)
- [CLI](#cli)
	- [Quick start](#quick-start)
	- [Install and upgrade](#install-and-upgrade)
	- [Build from source](#build-from-source)
	- [Example commands](#example-commands)
	- [Chain syntax reference](#chain-syntax-reference)

## Highlights

- **Buffered connections:** `NewBufConn` adds buffered read/write with explicit `Flush`.
- **Framed connections:** `NewFramedConn` adds a simple 4-byte length-prefixed frame protocol.
- **Mux / MuxClient:** `NewMux` wraps a `net.Listener` as a `net.Conn`; `NewMuxClient` wraps a `Dialer` as a `net.Conn` — both transparently accept/redial on EOF.
- **Demux / DemuxClient:** session multiplexer over a single `net.Conn` using fixed-length ID prefixes. `NewDemux` returns a `net.Listener` of virtual sessions; `NewDemuxClient` returns a `Dialer`.
- **Poll connections:** `NewPollConn` turns a request-response `net.Conn` into a persistent bidirectional stream via periodic polling.
- **Tagged connections:** `TaggedConn` interface extends `net.Conn` with opaque tags that carry context (e.g., DNS query) from read path to write path. `TaggedPipe` provides an in-memory pair.
- **Connection router/server:** `Server[ID]` accepts on a listener and routes new conns to handlers you register at runtime.
- **Tunneling:** `Tun` and `TunMaster[ID]` wire two connections together for bidirectional relay (useful to bridge UDP over a framed TCP stream, add TLS, etc.).
- **Driver/wrapper system:** pluggable `Driver` registry and typed `Wrapper` pipeline for composing connection transformations. Supports type-safe chains across `net.Listener`, `Dialer`, `net.Conn`, and `TaggedConn`.
- **DNS tunneling:** `proto/dnst` encodes data into DNS TXT queries/responses; combine with `Mux`, `TaggedDemux`, `DemuxClient`, and `PollConn` for a full tunnel.
- **ICMP support:** `icmp` transport for listener and dialer, tunneling traffic over ICMP Echo Request/Reply.
- **Chainable tunnel CLI and URI builder:** compose transports and wrappers with `URI` in code or via the `netx tun` command.

## Installation

```bash
go get github.com/pedramktb/go-netx@latest
```

Import as:

```go
import netx "github.com/pedramktb/go-netx"
```

Protocol implementations and drivers live in separate modules:

```bash
go get github.com/pedramktb/go-netx/proto/aesgcm@latest   # AES-GCM conn
go get github.com/pedramktb/go-netx/proto/dnst@latest      # DNS tunnel conn
go get github.com/pedramktb/go-netx/proto/ssh@latest        # SSH conn
go get github.com/pedramktb/go-netx/drivers/tls@latest      # TLS driver (register via blank import)
# ... etc.
```

## Library guide

### Buffered connections

```go
c, _ := net.Dial("tcp", addr)
bc := netx.NewBufConn(c, netx.WithBufSize(8<<10))

_, _ = bc.Write([]byte("hello"))
_ = bc.Flush() // ensure data is written now
```

Notes:

- `NewBufConn` returns a `BufConn` that implements `net.Conn` plus `Flush() error`.
- Options: `WithBufSize(uint16)` sets both reader and writer size; `WithBufReaderSize(uint16)` and `WithBufWriterSize(uint16)` set them independently. Default: 4096.
- `Close()` will attempt to `Flush()` and close, returning a joined error if any.

### Framed connections

```go
rawClient, rawServer := net.Pipe()
defer rawClient.Close(); defer rawServer.Close()

client := netx.NewFramedConn(rawClient)                           // default max frame size 4096
server := netx.NewFramedConn(rawServer, netx.WithMaxFrameSize(64<<10))

msg := []byte("hello frame")
_, _ = client.Write(msg) // sends a 4-byte big-endian length header then payload

buf := make([]byte, len(msg))
_, _ = io.ReadFull(server, buf) // reads exactly one frame (may deliver across multiple Read calls)
```

Notes:

- Each `Write(p)` sends one frame. Empty frames are allowed and read as `n=0, err=nil`.
- If an incoming frame exceeds `maxFrameSize`, `Read` returns `ErrFrameTooLarge`.
- If the underlying conn also supports `Flush` (e.g., `BufConn`), `Write` flushes to coalesce header+payload.

### Mux and MuxClient

`NewMux` adapts a `net.Listener` into a single `net.Conn`. Reads accept connections from the listener; when the current connection reaches EOF, the next one is accepted transparently. Writes go to the most recently accepted connection.

`NewMuxClient` does the inverse: it wraps a `Dialer` function as a `net.Conn`, dialing lazily on first use and redialing on EOF.

```go
// Server side: collapse accepted connections into one conn
ln, _ := net.Listen("tcp", ":9000")
conn := netx.NewMux(ln) // conn implements net.Conn

// Client side: auto-reconnecting conn from a dialer
clientConn := netx.NewMuxClient(func() (net.Conn, error) {
	return net.Dial("tcp", "server:9000")
}, netx.WithMuxClientRemoteAddr(remoteAddr))
```

Notes:

- Deadlines set on the mux propagate to newly accepted/dialed connections.
- Closing the mux closes both the current connection and the underlying listener/dialer.

### Demux and DemuxClient

`NewDemux` is a session multiplexer: it reads from a single `net.Conn`, extracts a fixed-length session ID prefix from each packet, and routes payloads to virtual per-session connections exposed via a `net.Listener`.

`NewDemuxClient` creates a `Dialer` that produces connections which automatically prepend/strip a session ID on every write/read.

```go
// Server: multiplex sessions over a single conn
sessListener := netx.NewDemux(conn, 4, // 4-byte session ID
	netx.WithDemuxSessQueueSize(16),
	netx.WithDemuxAccQueueSize(8),
)
defer sessListener.Close()

for {
	sess, _ := sessListener.Accept() // each session is a net.Conn
	go handleSession(sess)
}
```

```go
// Client: wrap a conn with a session ID
dial := netx.NewDemuxClient(conn, []byte{0x01, 0x02, 0x03, 0x04})
sessConn, _ := dial() // net.Conn with ID prepended on writes, stripped on reads
```

Options:

| Option | Default | Purpose |
|---|---|---|
| `WithDemuxAccQueueSize(uint16)` | 0 (unbuffered) | Accept queue capacity |
| `WithDemuxSessQueueSize(uint16)` | 8 | Per-session read queue depth |
| `WithDemuxBufSize(uint16)` | 4096 | Underlying read buffer size |
| `WithDemuxClientBufSize(uint16)` | 4096 | Client read/write buffer size |

### Poll connections

`NewPollConn` converts a request-response style `net.Conn` into a persistent bidirectional stream. It sends user data (or empty polls on idle) and reads back responses in a continuous loop.

```go
pollConn := netx.NewPollConn(reqRespConn,
	netx.WithPollInterval(50*time.Millisecond),
	netx.WithPollBufSize(4096),
	netx.WithPollSendQueueSize(32),
	netx.WithPollRecvQueueSize(32),
)
defer pollConn.Close()

_, _ = pollConn.Write(data) // queued, sent on next cycle
_, _ = pollConn.Read(buf)   // blocks until a response arrives
```

This is essential for protocols where the client must poll to receive data (e.g., DNS tunneling where the server can only respond to queries).

### Tagged connections

`TaggedConn` extends `net.Conn` semantics with an opaque `any` tag that carries context from the read path to the write path. This is critical for protocols where responses must correspond to specific requests (e.g., DNS queries).

```go
type TaggedConn interface {
	ReadTagged([]byte, *any) (int, error)   // read payload + capture tag
	WriteTagged([]byte, any) (int, error)   // write payload + attach tag
	Close() error
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
	SetDeadline(t time.Time) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
}
```

`TaggedPipe()` creates an in-memory bidirectional `TaggedConn` pair (analogous to `net.Pipe()`), useful for testing.

`NewTaggedDemux` is the tag-aware variant of `NewDemux`: it routes `{payload, tag}` pairs to sessions, and sessions consume tags on write so the response is constructed with the original request context.

### Runtime-routable server

Register handlers keyed by an ID (any comparable type). Each handler decides if it matches an incoming connection and returns an `io.Closer` to track (often the conn itself or a wrapped version).

```go
var s netx.Server[string]

// Route A: TLS connections
s.SetRoute("tls", func(ctx context.Context, conn net.Conn, closed func()) (bool, io.Closer) {
	if _, ok := conn.(interface{ ConnectionState() tls.ConnectionState }); !ok {
		return false, nil
	}
	// handle TLS conn; call closed() when the connection is fully done
	go func() { /* ... */ ; closed() }()
	return true, conn
})

// Route B: plain connections (fallback)
s.SetRoute("plain", func(ctx context.Context, conn net.Conn, closed func()) (bool, io.Closer) {
	if _, ok := conn.(interface{ ConnectionState() tls.ConnectionState }); ok {
		return false, nil
	}
	go func() { /* ... */ ; closed() }()
	return true, conn
})

ln, _ := net.Listen("tcp", ":8080")
go s.Serve(context.Background(), ln)

// Hot-swap or remove routes at runtime
s.SetRoute("plain", newHandler)
s.RemoveRoute("tls")

// Graceful shutdown
ctx, cancel := context.WithTimeout(context.Background(), time.Second)
defer cancel()
_ = s.Shutdown(ctx) // waits for tracked connections or force-closes on deadline
```

Handler contract:

- Return `(matched=false, _ )` quickly if the connection is not yours; the server will try the next route.
- If you take ownership, return `(true, closer)`. Use `closed()` exactly once when you are logically done so the server stops tracking it.
- If you return `nil` for the closer, the server will track the original `conn`.
- `Close()` immediately stops accepting and closes tracked connections. `Shutdown(ctx)` stops accepting and waits for tracked connections until `ctx` is done, after which remaining connections are force-closed.

### Tunneling

`Tun` relays bytes bidirectionally between two endpoints. `TunMaster[ID]` builds on `Server[ID]` to create tunnels from accepted conns.

Bridge UDP over a framed TCP stream:

```go
// Server side: accept a TCP stream, frame it, and relay to a UDP socket
var tm netx.TunMaster[string]
tm.SetRoute("udp-over-tcp", func(ctx context.Context, conn net.Conn) (bool, context.Context, netx.Tun) {
	framed := netx.NewFramedConn(conn)
	udpConn, _ := net.DialUDP("udp", nil, serverUDPAddr)
	return true, ctx, netx.Tun{Conn: framed, Peer: udpConn, BufferSize: 64 << 10}
})

ln, _ := net.Listen("tcp", ":9000")
go tm.Serve(context.Background(), ln)
```

Notes:

- `Tun.Relay(ctx)` runs two half-duplex copies until either side closes; `Close()` shuts both sides.
- `BufferSize` controls the copy buffer (default 32KiB).
- `TunMaster.SetRoute` starts `Relay` in a goroutine and calls the server's `closed()` when finished; it also logs tunnel start/close using the configured `Logger`.

### Driver and wrapper system

netX uses a pluggable driver registry and a typed wrapper pipeline for composing connection transformations.

**Drivers** are registered globally and instantiate `Wrapper` values from parameters:

```go
// Register a custom driver (typically in an init function)
netx.Register("myproto", func(params map[string]string, listener bool) (netx.Wrapper, error) {
	return netx.Wrapper{
		Name:       "myproto",
		Params:     params,
		Listener:   listener,
		ConnToConn: func(c net.Conn) (net.Conn, error) { return myWrap(c), nil },
	}, nil
})
```

**Wrappers** form a typed pipeline that chains transformations. Each wrapper declares which pipe type it accepts and produces (`net.Listener`, `Dialer`, `net.Conn`, or `TaggedConn`):

```go
// Apply wrappers manually
var wrappers netx.Wrappers
_ = wrappers.UnmarshalText([]byte("tls{cert=...,key=...}+framed"), true) // true = listener side

wrapped, _ := wrappers.Apply(rawListener) // net.Listener → net.Listener
```

**Schemes** combine a transport with wrappers:

```go
var s netx.ListenerScheme
_ = s.UnmarshalText([]byte("tcp+tls{cert=...,key=...}+framed"))
ln, _ := s.Listen(ctx, ":9000")
```

Built-in drivers available via blank import of `drivers/*` packages: `aesgcm`, `dnst`, `dtls`, `dtlspsk`, `ssh`, `tls`, `tlspsk`, `utls`. Core drivers (`buffered`, `framed`, `mux`, `demux`) are registered automatically.

### Programmatic URIs

The chainable URI system composes a transport, wrappers, and address into a single string. URIs follow the format `<transport>+<wrapper1>{params}+<wrapper2>://<address>`.

```go
ctx := context.Background()

var listenURI netx.ListenerURI
_ = listenURI.UnmarshalText([]byte("tcp+tls{cert=...hex...,key=...hex...}://:9000"))
ln, _ := listenURI.Listen(ctx)

var dialURI netx.DialerURI
_ = dialURI.UnmarshalText([]byte("udp+aesgcm{key=...hex...}://127.0.0.1:5555"))
peerConn, _ := dialURI.Dial(ctx)

serverConn, _ := ln.Accept()

tun := netx.Tun{Conn: serverConn, Peer: peerConn}
go tun.Relay(ctx)
```

`ListenerURI.Listen` and `DialerURI.Dial` instantiate the transport, apply each wrapper in order, and enforce type-safe pipeline validation.

### Logging

You can plug any logger that implements the simple `Logger` interface:

```go
type Logger interface {
	DebugContext(ctx context.Context, msg string, args ...any)
	InfoContext(ctx context.Context, msg string, args ...any)
	WarnContext(ctx context.Context, msg string, args ...any)
	ErrorContext(ctx context.Context, msg string, args ...any)
}
```

If `Logger` is nil, the server/tunnel use `slog.Default()`.

### Design notes and guarantees

- All wrappers implement `net.Conn` (or `TaggedConn`) where applicable to remain drop-in.
- The wrapper pipeline validates type compatibility at parse time — mismatched chains fail early.
- Server routes use copy-on-write updates; `SetRoute`/`RemoveRoute` are safe to call concurrently.
- Unhandled connections are dropped immediately after all routes decline.
- `Shutdown(ctx)` will close listeners, then wait for tracked connections until `ctx` is done, after which remaining connections are force-closed.
- Mux and MuxClient transparently handle connection cycling (accept/redial on EOF).
- Demux sessions are fully independent `net.Conn` values with their own read queues; backpressure is per-session.

## CLI

The CLI is available at `cli/cmd/netx` with a `tun` subcommand to relay between chainable endpoints.

### Quick start

1. Install the CLI.

	 ```bash
	 go install github.com/pedramktb/go-netx/cli/cmd/netx@latest
	 ```

2. Compose listener and dialer URIs. Quote them so shells do not mangle the `+`, `{`, or `,` characters.

	 ```bash
	 netx tun \
		 --from "tcp+tls{cert=$(cat server.crt | xxd -p),key=$(cat server.key | xxd -p)}://:9000" \
		 --to   "udp+aesgcm{key=00112233445566778899aabbccddeeff}://127.0.0.1:5555"
	 ```

3. Watch the logs. Adjust verbosity with `--log debug`, or hit `Ctrl+C` for a graceful shutdown.

### Install and upgrade

```bash
go install github.com/pedramktb/go-netx/cli/cmd/netx@latest
```

### Build from source

```bash
task build
```

### Example commands

```bash
# Show help
netx tun -h

# Example: TCP TLS server to TCP TLS+buffered+framed+aesgcm client
netx tun \
	--from tcp+tls{cert=server.crt,key=server.key}://:9000 \
	--to tcp+tls{cert=client.crt}+buffered{size=8192}+framed{maxsize=4096}+aesgcm{key=00112233445566778899aabbccddeeff}://example.com:9443

# Example: UDP DTLS server to UDP aesgcm client
netx tun \
	--from udp+dtls{cert=server.crt,key=server.key}://:4444 \
	--to udp+aesgcm{key=00112233445566778899aabbccddeeff}://10.0.0.10:5555

# Example: DNS tunnel server
netx tun \
	--from udp+dnst{domain=t.example.com}+demux{id=0000,sessqueuesize=16}://:53 \
	--to tcp://internal-service:8080
```

Options:

- `--from <chain>://listenAddr` - Incoming side chain URI (required)
- `--to <chain>://connectAddr` - Peer side chain URI (required)
- `--log <level>` - Log level: debug|info|warn|error (default: info)
- `-h` - Show help

### Chain syntax reference

Chains use the form `<transport>+<wrapper1>+<wrapper2>+...://host:port` where `<transport>` is a base transport, optionally followed by `+`-separated wrappers with parameters in braces.

**Supported base transports:**

- `tcp` - TCP listener or dialer
- `udp` - UDP listener or dialer
- `icmp` - ICMP listener or dialer (tunnels over Echo Request/Reply)

**Supported wrappers:**

- `buffered` - Buffered read/write for better performance
	- Params: `size` (optional, default: 4096)

- `framed` - Length-prefixed frames for packet semantics over streams
	- Params: `maxsize` (optional, default: 4096)

- `mux` - Collapse a listener into a single `net.Conn` (server) or auto-reconnecting dialer into a `net.Conn` (client)
	- No params

- `demux` - Session multiplexer over a single conn
	- Params: `id` (hex, required for client), `bufsize` (optional), `accqueuesize` (optional), `sessqueuesize` (optional)

- `dnst` - DNS tunnel encoding (Base32 in TXT queries/responses)
	- Params: `domain` (required)

- `poll` - Convert request-response conn into persistent bidirectional stream
	- Params: `interval` (optional), `bufsize` (optional), `sendqueuesize` (optional), `recvqueuesize` (optional)

- `aesgcm` - AES-GCM encryption with passive IV exchange
	- Params: `key`, `maxpacket` (optional, default: 32768)

- `tls` - Transport Layer Security
	- Server params: `cert`, `key`
	- Client params: `cert` (optional, for SPKI pinning), `servername` (required if cert not provided)

- `utls` - TLS with client fingerprint camouflage via uTLS
	- Client-side only
	- Params: `cert` (optional, for SPKI pinning), `servername` (required if cert not provided), `hello` (optional: chrome, firefox, ios, android, safari, edge, randomized; default: chrome)

- `dtls` - Datagram Transport Layer Security
	- Server params: `cert`, `key`
	- Client params: `cert` (optional, for SPKI pinning), `servername` (required if cert not provided)

- `tlspsk` - TLS with pre-shared key (cipher: TLS_DHE_PSK_WITH_AES_256_CBC_SHA)
	- Params: `key`

- `dtlspsk` - DTLS with pre-shared key (cipher: TLS_PSK_WITH_AES_128_GCM_SHA256)
	- Params: `key`

- `ssh` - SSH tunneling via "direct-tcpip" channels
	- Server params: `key`, `pass` (optional), `pubkey` (optional, required if no pass)
	- Client params: `pubkey`, `pass` (optional), `key` (optional, required if no pass)

**Notes:**
- All passwords, keys and certificates must be provided as hex-encoded strings.
- When using `cert` for client-side `tls`/`utls`/`dtls`, default validation is disabled and a manual SPKI (SubjectPublicKeyInfo) hash comparison is performed against the provided certificate. This is certificate pinning and will fail if the server presents a different key.
- SSH server must accept "direct-tcpip" channels (most do by default).
- See [docs/mux-tag-poll.md](docs/mux-tag-poll.md) for the full architecture and data-flow diagrams of the mux/demux/poll/tagged system.
