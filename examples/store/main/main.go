// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

// The store, served — or described, when asked for its manifest.
package main

import (
	"log"
	"os"

	"github.com/zap-proto/zip/examples/store"
)

func main() {
	app := (&store.Store{}).Ops()
	if done, err := app.Described(); done {
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	addrs := os.Args[1:]
	if len(addrs) == 0 {
		addrs = []string{":9654", "http://:8081"}
	}
	if err := app.Listen(addrs...); err != nil {
		log.Fatal(err)
	}
}
