// Command vapid prints a fresh VAPID key pair for the push configuration.
package main

import (
	"fmt"
	"os"

	"github.com/vivianobiako/qless/api/internal/push"
)

func main() {
	private, public, err := push.GenerateKeys()
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate VAPID keys:", err)
		os.Exit(1)
	}
	fmt.Printf("VAPID_PUBLIC_KEY=%s\nVAPID_PRIVATE_KEY=%s\n", public, private)
}
