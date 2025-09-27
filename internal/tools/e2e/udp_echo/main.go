//go:build e2e
// +build e2e

package main

import (
	"log"
	"net"
	"os"
	"time"
)

func main() {
	addr := "127.0.0.1:28081"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	a, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		log.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", a)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("udp echo listening on %s", conn.LocalAddr())
	buf := make([]byte, 65535)
	for {
		n, ra, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Fatal(err)
		}
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.WriteToUDP(buf[:n], ra)
	}
}
