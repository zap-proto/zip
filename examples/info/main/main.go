// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

// The info service, served.
//
// One verb decides: `info manifest <file>` writes what this app declares and
// exits, which is what zipc projects; anything else serves — ZAP on :9653 and
// HTTP on :8080, the same ops over both.
package main

import (
	"log"
	"os"

	"github.com/zap-proto/zip/examples/info"
)

func main() {
	app := (&info.Info{}).Ops()
	if done, err := app.Described(); done {
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	addrs := os.Args[1:]
	if len(addrs) == 0 {
		addrs = []string{":9653", "http://:8080"}
	}
	if err := app.Listen(addrs...); err != nil {
		log.Fatal(err)
	}
}
