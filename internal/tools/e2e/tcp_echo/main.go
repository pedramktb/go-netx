//go:build e2e
// +build e2e

package main

import (
	"bufio"
	"io"
	"log"
	"net"
	"os"
)

func main() {
	addr := "127.0.0.1:28080"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("tcp echo listening on %s", ln.Addr())
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go func(conn net.Conn) {
			defer conn.Close()
			r := bufio.NewReader(conn)
			buf := make([]byte, 4096)
			for {
				n, err := r.Read(buf)
				if n > 0 {
					_, _ = conn.Write(buf[:n])
				}
				if err != nil {
					if err != io.EOF {
						log.Println("read err:", err)
					}
					return
				}
			}
		}(c)
	}
}
