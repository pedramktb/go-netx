module github.com/pedramktb/go-netx/cli

go 1.25.7

replace (
	github.com/pedramktb/go-netx/drivers/aesgcm => ../drivers/aesgcm
	github.com/pedramktb/go-netx/drivers/dnst => ../drivers/dnst
	github.com/pedramktb/go-netx/drivers/dtls => ../drivers/dtls
	github.com/pedramktb/go-netx/drivers/dtlspsk => ../drivers/dtlspsk
	github.com/pedramktb/go-netx/drivers/ssh => ../drivers/ssh
	github.com/pedramktb/go-netx/drivers/tls => ../drivers/tls
	github.com/pedramktb/go-netx/drivers/tlspsk => ../drivers/tlspsk
	github.com/pedramktb/go-netx/drivers/utls => ../drivers/utls
	github.com/pedramktb/go-netx/proto/aesgcm => ../proto/aesgcm
	github.com/pedramktb/go-netx/proto/dnst => ../proto/dnst
	github.com/pedramktb/go-netx/proto/ssh => ../proto/ssh
)

require (
	github.com/miekg/dns v1.1.72
	github.com/pedramktb/go-netx v1.2.0
	github.com/pedramktb/go-netx/drivers/aesgcm v0.0.0-00010101000000-000000000000
	github.com/pedramktb/go-netx/drivers/dnst v0.0.0-00010101000000-000000000000
	github.com/pedramktb/go-netx/drivers/dtls v0.0.0-00010101000000-000000000000
	github.com/pedramktb/go-netx/drivers/dtlspsk v0.0.0-00010101000000-000000000000
	github.com/pedramktb/go-netx/drivers/ssh v0.0.0-00010101000000-000000000000
	github.com/pedramktb/go-netx/drivers/tls v0.0.0-00010101000000-000000000000
	github.com/pedramktb/go-netx/drivers/tlspsk v0.0.0-00010101000000-000000000000
	github.com/pedramktb/go-netx/drivers/utls v0.0.0-00010101000000-000000000000
	github.com/spf13/cobra v1.10.2
)

require (
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/klauspost/compress v1.18.4 // indirect
	github.com/pedramktb/go-netx/proto/aesgcm v0.0.0-00010101000000-000000000000 // indirect
	github.com/pedramktb/go-netx/proto/dnst v0.0.0-00010101000000-000000000000 // indirect
	github.com/pedramktb/go-netx/proto/ssh v0.0.0-00010101000000-000000000000 // indirect
	github.com/pion/dtls/v3 v3.1.2 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/transport/v3 v3.1.1 // indirect
	github.com/pion/transport/v4 v4.0.1 // indirect
	github.com/raff/tls-ext v1.0.0 // indirect
	github.com/raff/tls-psk v1.0.0 // indirect
	github.com/refraction-networking/utls v1.8.2 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/mod v0.33.0 // indirect
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/tools v0.42.0 // indirect
)
