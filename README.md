# netx

Small, focused extensions to Go's "net" standard library.

This library provides a few composable building blocks that integrate with standard `net.Conn` and `net.Listener` types without introducing heavy abstractions.

- Buffered connections: `NewBufConn` adds buffered read/write with explicit `Flush`.
- Framed connections: `NewFramedConn` adds a simple 4‑byte length‑prefixed frame protocol.
- Connection router/server: `Server[ID]` accepts on a listener and routes new conns to handlers you register at runtime.
- Tunneling: `Tun` and `TunMaster[ID]` wire two connections together for bidirectional relay (useful to bridge UDP over a framed TCP stream, add TLS, etc.).

## Install

```bash
go get github.com/pedramktb/go-netx@latest
```

Import as:

```go
import netx "github.com/pedramktb/go-netx"
```

## Quick examples

### Buffered connection (net.Conn + Flush)

```go
c, _ := net.Dial("tcp", addr)
bc := netx.NewBufConn(c, netx.WithBufReaderSize(8<<10), netx.WithBufWriterSize(8<<10))

_, _ = bc.Write([]byte("hello"))
_ = bc.Flush() // ensure data is written now
```

Notes:

- `NewBufConn` returns a type that implements `net.Conn` plus `Flush() error`.
- `Close()` will attempt to `Flush()` and close, returning a joined error if any.

### Framed connection (length‑prefixed)

```go
rawClient, rawServer := net.Pipe()
defer rawClient.Close(); defer rawServer.Close()

client := netx.NewFramedConn(rawClient)                 // default max frame size 32KiB
server := netx.NewFramedConn(rawServer, netx.WithMaxFrameSize(64<<10))

msg := []byte("hello frame")
_, _ = client.Write(msg) // sends a 4‑byte big‑endian length header then payload

buf := make([]byte, len(msg))
_, _ = io.ReadFull(server, buf) // reads exactly one frame (may deliver across multiple Read calls)
```

Notes:

- Each `Write(p)` sends one frame. Empty frames are allowed and read as `n=0, err=nil`.
- If an incoming frame exceeds `maxFrameSize`, `Read` returns `ErrFrameTooLarge`.
- If the underlying conn also supports `Flush` (e.g., `BufConn`), `Write` flushes to coalesce header+payload.

### Runtime‑routable server

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

// Hot‑swap or remove routes at runtime
s.SetRoute("plain", newHandler)
s.RemoveRoute("tls")

// Graceful shutdown
ctx, cancel := context.WithTimeout(context.Background(), time.Second)
defer cancel()
_ = s.Shutdown(ctx) // waits for tracked connections or force‑closes on deadline
```

Handler contract:

- Return `(matched=false, _ )` quickly if the connection is not yours; the server will try the next route.
- If you take ownership, return `(true, closer)`. Use `closed()` exactly once when you are logically done so the server stops tracking it.
- If you return `nil` for the closer, the server will track the original `conn`.
- `Close()` immediately stops accepting and closes tracked connections. `Shutdown(ctx)` stops accepting and waits for tracked connections until `ctx` is done, then force‑closes.

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

- `Tun.Relay(ctx)` runs two half‑duplex copies until either side closes; `Close()` shuts both sides.
- `BufferSize` controls the copy buffer (default 32KiB).
- `TunMaster.SetRoute` starts `Relay` in a goroutine and calls the server's `closed()` when finished; it also logs tunnel start/close using the configured `Logger`.

## Logging

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

## Design notes and guarantees

- All wrappers implement `net.Conn` where applicable to remain drop‑in.
- Server routes use copy‑on‑write updates; `SetRoute`/`RemoveRoute` are safe to call concurrently.
- Unhandled connections are dropped immediately after all routes decline.
- `Shutdown(ctx)` will close listeners, then wait for tracked connections until `ctx` is done, after which remaining connections are force‑closed.

## CLI

An extendable CLI is available at `cmd/netx` with an initial `tun` subcommand to relay between chainable endpoints.

Build:

```bash
task build
```

Install and use:

```bash
go install github.com/pedramktb/go-netx/cmd/netx@latest

# Show help
netx tun --help

# Example: TCP TLS server to TCP TLS+framed+aesgcm client
netx tun --from tcp+tls[cert=server.crt,key=server.key] \
		 --to tcp+tls[serverName=example.com,insecure=true]+buffered[buf=8192]+framed[maxFrame=4096]+aesgcm[key=00112233445566778899aabbccddeeff] \
		 tcp://:9000 tcp://example.com:9443
```

Chain syntax:

- Base: `tcp` or `udp`
- Wrappers:
  - `tls[cert=...,key=...]` (server) or `tls[serverName=...,ca=...,insecure=true]` (client)
  - `dtls[cert=...,key=...]` (server) or `dtls[serverName=...,ca=...,insecure=true]` (client) with UDP
  - `tlspsk[key=...]` (With a deprecated library and TLS1.2, use at your own risk!)
  - `dtlspsk[key=...]`
  - `aesgcm[key=<hex>,maxPacket=32768]`
  - `buffered[buf=4096]`
  - `framed[maxFrame=32768]`

Notes:

- Endpoints use URI form: `<chain>://host:port`
- You can chain multiple wrappers on either side; the tool uses `TunMaster` under the hood.
