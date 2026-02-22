module github.com/pedramktb/go-netx/drivers/aesgcm

go 1.25.7

replace github.com/pedramktb/go-netx/proto/aesgcm => ../../proto/aesgcm

require (
	github.com/pedramktb/go-netx v1.2.0
	github.com/pedramktb/go-netx/proto/aesgcm v0.0.0-00010101000000-000000000000
)

require (
	github.com/pion/transport/v3 v3.1.1 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
)
