module github.com/pedramktb/go-netx/drivers/dnst

go 1.25.7

replace github.com/pedramktb/go-netx/proto/dnst => ../../proto/dnst

require (
	github.com/pedramktb/go-netx v1.3.0
	github.com/pedramktb/go-netx/proto/dnst v0.0.0-20260222183208-e396224258be
)

require (
	github.com/miekg/dns v1.1.72 // indirect
	github.com/pion/transport/v3 v3.1.1 // indirect
	golang.org/x/mod v0.33.0 // indirect
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/tools v0.42.0 // indirect
)
