//go:build ignore
// +build ignore

package main

import (
	"log"
	"os"

	"github.com/gordian-engine/gordian/gcrypto/gcryptotest"
)

func main() {
	f, err := os.Create("ed25519_keys.go")
	if err != nil {
		log.Fatal(err)
	}

	if err := gcryptotest.GenerateKeys(f); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
}
