// Command tunnelkit-server is the ingress SSH tunnel server (accepts reverse
// tunnels with public-key auth + permitlisten) plus the speedtest initiator
// used to measure clients over the tunnel. It uses x/crypto/ssh.
//
//	tunnelkit-server            starts the SSH tunnel server (default)
//	tunnelkit-server speedtest  measures a client over the tunnel (--speedtest-addr)
//	tunnelkit-server version
package main

import (
	"fmt"
	"os"

	"github.com/adriansanchis87/tunnelkit/internal/config"
	"github.com/adriansanchis87/tunnelkit/internal/speedtest"
	"github.com/adriansanchis87/tunnelkit/internal/tunnelserver"
)

var version = "0.1.0-dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "-v", "--version":
			fmt.Println("tunnelkit-server", version)
			return
		case "speedtest":
			must(speedtest.RunInitiator(config.Load(os.Args[2:])))
			return
		}
	}
	must(tunnelserver.Run(config.Load(os.Args[1:])))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "tunnelkit-server:", err)
		os.Exit(1)
	}
}
