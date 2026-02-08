//go:build e2e
// +build e2e

package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: tcp_client host:port message")
		os.Exit(2)
	}
	addr, msg := os.Args[1], os.Args[2]
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
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
	r := bufio.NewReader(c)
	buf := make([]byte, len(msg))
	if _, err := r.Read(buf); err != nil {
		fmt.Println("read error:", err)
		os.Exit(1)
	}
	fmt.Print(string(buf))
}
