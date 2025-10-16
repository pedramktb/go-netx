//go:build e2e
// +build e2e

package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: udp_client host:port message")
		os.Exit(2)
	}
	addr, msg := os.Args[1], os.Args[2]
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		fmt.Println("resolve error:", err)
		os.Exit(1)
	}
	c, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		fmt.Println("dial error:", err)
		os.Exit(1)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write([]byte(msg)); err != nil {
		fmt.Println("write error:", err)
		os.Exit(1)
	}
	buf := make([]byte, 65535)
	n, _, err := c.ReadFrom(buf)
	if err != nil {
		fmt.Println("read error:", err)
		os.Exit(1)
	}
	fmt.Print(string(buf[:n]))
}
