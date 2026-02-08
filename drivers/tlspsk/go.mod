module github.com/pedramktb/go-netx/drivers/tlspsk

go 1.25.6

replace github.com/pedramktb/go-netx => ../../

require (
	github.com/pedramktb/go-netx v0.0.0-00010101000000-000000000000
	github.com/raff/tls-ext v1.0.0
	github.com/raff/tls-psk v1.0.0
)

require (
	github.com/pion/transport/v3 v3.1.1 // indirect
	golang.org/x/crypto v0.47.0 // indirect
	golang.org/x/net v0.49.0 // indirect
	golang.org/x/sys v0.40.0 // indirect
)
