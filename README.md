# <img src="./netx.svg" alt="netx" />

Small, focused extensions to Go's "net" standard library.

This library provides a few composable building blocks that integrate with standard `net.Conn` and `net.Listener` types without introducing heavy abstractions.

## Contents
- [Highlights](#highlights)
- [Installation](#installation)
- [Library guide](#library-guide)
	- [Buffered connections](#buffered-connections)
	- [Framed connections](#framed-connections)
	- [Runtime-routable server](#runtime-routable-server)
	- [Tunneling and UDP over TCP](#tunneling-and-udp-over-tcp)
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

- Buffered connections: `NewBufConn` adds buffered read/write with explicit `Flush`.
- Framed connections: `NewFramedConn` adds a simple 4-byte length-prefixed frame protocol.
- Connection router/server: `Server[ID]` accepts on a listener and routes new conns to handlers you register at runtime.
- Tunneling: `Tun` and `TunMaster[ID]` wire two connections together for bidirectional relay (useful to bridge UDP over a framed TCP stream, add TLS, etc.).
- Chainable tunnel CLI and URI builder: compose transports and wrappers with `uri.URI` in code or via the `netx tun` command.

## Installation

```bash
go get github.com/pedramktb/go-netx@latest
```

Import as:

```go
import netx "github.com/pedramktb/go-netx"
```

## Library guide

### Buffered connections

```go
c, _ := net.Dial("tcp", addr)
bc := netx.NewBufConn(c, netx.WithBufReaderSize(8<<10), netx.WithBufWriterSize(8<<10))

_, _ = bc.Write([]byte("hello"))
_ = bc.Flush() // ensure data is written now
```

Notes:

- `NewBufConn` returns a type that implements `net.Conn` plus `Flush() error`.
- `Close()` will attempt to `Flush()` and close, returning a joined error if any.

### Framed connections

```go
rawClient, rawServer := net.Pipe()
defer rawClient.Close(); defer rawServer.Close()

client := netx.NewFramedConn(rawClient)                 // default max frame size 32KiB
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

### Tunneling and UDP over TCP

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

### Programmatic URIs

The same chain syntax is exposed via the `uri` package for embedding tunnels in your own applications.

```go
ctx := context.Background() // handle cancellation in real applications

listen := uri.URI{Listener: true}
_ = listen.UnmarshalText([]byte("tcp+tls[cert=...hex...,key=...hex...]://:9000"))

ln, _ := listen.Listen(ctx)

peer := uri.URI{}
_ = peer.UnmarshalText([]byte("udp+aesgcm[key=...hex...]://127.0.0.1:5555"))

peerConn, _ := peer.Dial(ctx)

serverConn, _ := ln.Accept()

tun := netx.Tun{Conn: serverConn, Peer: peerConn}
go tun.Relay(ctx)
```

`uri.URI` takes care of instantiating the transport, applying each wrapper in order, and enforcing listener/client-side parameter validation.

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

- All wrappers implement `net.Conn` where applicable to remain drop-in.
- Server routes use copy-on-write updates; `SetRoute`/`RemoveRoute` are safe to call concurrently.
- Unhandled connections are dropped immediately after all routes decline.
- `Shutdown(ctx)` will close listeners, then wait for tracked connections until `ctx` is done, after which remaining connections are force-closed.

## CLI

An extendable CLI is available at `cmd/netx` with an initial `tun` subcommand to relay between chainable endpoints.

### Quick start

1. Install the CLI.

	 ```bash
	 go install github.com/pedramktb/go-netx/cmd/netx@latest
	 ```

2. Compose listener and dialer URIs. Quote them so shells do not mangle the `+`, `[`, or `,` characters.

	 ```bash
	 netx tun \
		 --from "tcp+tls[cert=$(cat server.crt | xxd -p),key=$(cat server.key | xxd -p)]://:9000" \
		 --to   "udp+aesgcm[key=00112233445566778899aabbccddeeff]://127.0.0.1:5555"
	 ```

3. Watch the logs. Adjust verbosity with `--log debug`, or hit `Ctrl+C` for a graceful shutdown.

### Install and upgrade

```bash
go install github.com/pedramktb/go-netx/cmd/netx@latest
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
	--from tcp+tls[cert=server.crt,key=server.key]://:9000 \
	--to tcp+tls[cert=client.crt]+buffered[size=8192]+framed[maxsize=4096]+aesgcm[key=00112233445566778899aabbccddeeff]://example.com:9443

# Example: UDP DTLS server to UDP aesgcm client
netx tun \
	--from udp+dtls[cert=server.crt,key=server.key]://:4444 \
	--to udp+aesgcm[key=00112233445566778899aabbccddeeff]://10.0.0.10:5555
```

Options:

- `--from <chain>://listenAddr` - Incoming side chain URI (required)
- `--to <chain>://connectAddr` - Peer side chain URI (required)
- `--log <level>` - Log level: debug|info|warn|error (default: info)
- `-h` - Show help

### Chain syntax reference

Chains use the form `<chain>://host:port` where `<chain>` is a `+`-separated list starting with a base transport (`tcp` or `udp`), optionally followed by wrappers with parameters in brackets.

**Supported base transports:**

- `tcp` - TCP listener or dialer
- `udp` - UDP listener or dialer

**Supported wrappers:**

- `tls` - Transport Layer Security
	- Server params: `cert`, `key`
	- Client params: `cert` (optional, for SPKI pinning), `servername` (required if cert not provided)

- `utls` - TLS with client fingerprint camouflage via uTLS
	- Client-side only
	- Params: `cert` (optional, for SPKI pinning), `servername` (required if cert not provided), `hello` (optional: chrome, firefox, ios, android, safari, edge, randomized, randomizednoalpn; default: chrome)

- `dtls` - Datagram Transport Layer Security
	- Server params: `cert`, `key`
	- Client params: `cert` (optional, for SPKI pinning), `servername` (required if cert not provided)

- `tlspsk` - TLS with pre-shared key (TLS 1.2, cipher: TLS_PSK_WITH_AES_256_CBC_SHA)
	- Params: `key`, `identity`

- `dtlspsk` - DTLS with pre-shared key (cipher: TLS_PSK_WITH_AES_128_GCM_SHA256)
	- Params: `key`, `identity`

- `aesgcm` - AES-GCM encryption with passive IV exchange
	- Params: `key`, `maxpacket` (optional, default: 32768)

- `buffered` - Buffered read/write for better performance
	- Params: `size` (optional, default: 4096)

- `framed` - Length-prefixed frames for packet semantics over streams
	- Params: `maxsize` (optional, default: 32768)

- `ssh` - SSH tunneling via "direct-tcpip" channels
	- Server params: `key` (optional, required with pass), `pass` (optional), `pubkey` (optional, required if no pass)
	- Client params: `pubkey`, `pass` (optional), `key` (optional, required if no pass)

**Notes:**
- All passwords, keys and certificates must be provided as hex-encoded strings.
- When using `cert` for client-side `tls`/`utls`/`dtls`, default validation is disabled and a manual SPKI (SubjectPublicKeyInfo) hash comparison is performed against the provided certificate. This is certificate pinning and will fail if the server presents a different key.
