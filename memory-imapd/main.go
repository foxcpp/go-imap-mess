package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"

	"github.com/emersion/go-imap/server"
	"github.com/foxcpp/go-imap-sequpdate/memory"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "memory-imapd - Dumb IMAP4rev1 server exposing a rudimentary backend code for use with imaptest\n")
		fmt.Fprintf(os.Stderr, "Usage: %s <endpoint>\n", os.Args[0])
		os.Exit(2)
	}

	endpoint := os.Args[1]

	srv := server.New(memory.New())
	defer srv.Close()

	srv.AllowInsecureAuth = true

	l, err := net.Listen("tcp", endpoint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}

	go func() {
		if err := srv.Serve(l); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	<-sig
}

