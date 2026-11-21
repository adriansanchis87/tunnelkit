// Command tunnelkit-client keeps the reverse SSH tunnel to the ingress alive
// (+ /metrics + speedtest responder). Without the SSH server, the binary stays
// small and cross-compiles easily to OpenWrt. It does not depend on x/crypto.
//
//	tunnelkit-client            keeps the tunnel alive (default)
//	tunnelkit-client responder  standalone speedtest responder (host without a tunnel)
//	tunnelkit-client version
package main

import (
	"fmt"
	"os"

	"github.com/adriansanchis87/tunnelkit/internal/client"
	"github.com/adriansanchis87/tunnelkit/internal/config"
	"github.com/adriansanchis87/tunnelkit/internal/speedtest"
)

var version = "0.1.0-dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "-v", "--version":
			fmt.Println("tunnelkit-client", version)
			return
		case "responder":
			must(speedtest.RunResponder(config.Load(os.Args[2:])))
			return
		}
	}
	must(client.Run(config.Load(os.Args[1:])))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "tunnelkit-client:", err)
		os.Exit(1)
	}
}
