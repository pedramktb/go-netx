package cli

const uriFormat = `URI Format:
	<transport>+<layer1>[layer1param1key=layer1param1value,layer1param2key=layer1param2value,...]+<layer2>+...://<address>

	Examples:
		tcp+tls[cert=$(cat server.crt | xxd -p),key=$(cat server.key | xxd -p)]://:9000
		tcp+tls[cert=$(cat client.crt | xxd -p)]+buffered[size=8192]+framed[maxsize=4096]+aesgcm[key=00112233445566778899aabbccddeeff]://example.com:9443

	Supported transports:
		- tcp: TCP listener or dialer
		- udp: UDP listener or dialer
		- icmp: ICMP listener or dialer

	Supported layers:
		- framed: length-prefixed frames for transports or layers that need packet semantics over streams.
			params: maxsize (optional, defaults to 32768)
		- buffered: buffered read/write for better performance when using framing.
			params: size (optional, defaults to 4096)
		- aesgcm: AES-GCM encryption. A passive 12-byte handshake exchanges IVs.
			params: key, maxpacket (optional, defaults to 32768)
		- ssh: SSH tunneling via "direct-tcpip" channels.
			server params: key, pass (optional), pubkey (optional, required if no pass)
			client options: pubkey, pass (optional), key (optional, required if no pass)
		- tls: Transport Layer Security
			server params: key, cert
			client params: cert (optional, for SPKI pinning), servername (required if cert not provided)
		- utls: TLS with client fingerprint camouflage via uTLS (github.com/refraction-networking/utls)
			client params: cert (optional, for SPKI pinning), servername (required if cert not provided), hello (optional, e.g. chrome, firefox, ios, android, safari, edge, randomized)
		- dtls: Datagram Transport Layer Security
			server params: key, cert
			client params: cert (optional, for SPKI pinning), servername (required if cert not provided)
		- tlspsk: TLS with pre-shared key. Cipher is TLS_DHE_PSK_WITH_AES_256_CBC_SHA.
			params: key
		- dtlspsk: DTLS with pre-shared key. Cipher is TLS_PSK_WITH_AES_128_GCM_SHA256.
			params: key

	Notes:
		- All passwords, keys and certificates must be provided as hex-encoded strings.
		- When using 'cert' for client-side TLS/uTLS/DTLS, default validation is disabled and a manual SPKI (SubjectPublicKeyInfo) hash comparison is performed
		against the provided certificate. This is certificate pinning and will fail if the server presents a different key.
		- SSH server must accept "direct-tcpip" channels (most do by default).
`
