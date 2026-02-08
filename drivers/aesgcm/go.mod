module github.com/pedramktb/go-netx/drivers/aesgcm

go 1.25.6

replace (
	github.com/pedramktb/go-netx => ../..
	github.com/pedramktb/go-netx/proto/aesgcm => ../../proto/aesgcm
)

require (
	github.com/pedramktb/go-netx v0.0.0-00010101000000-000000000000
	github.com/pedramktb/go-netx/proto/aesgcm v0.0.0-00010101000000-000000000000
)

require (
	github.com/pion/transport/v3 v3.1.1 // indirect
	golang.org/x/net v0.49.0 // indirect
	golang.org/x/sys v0.40.0 // indirect
)
