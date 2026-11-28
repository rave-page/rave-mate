// Command feed is the Linux self-update e2e's fake release feed (stdlib only):
//
//	feed -keygen <keyfile>              write an Ed25519 seed, print the base64 public key
//	feed -sign <keyfile> -dir <d>       write <d>/latest.json.sig over the exact latest.json bytes
//	feed -serve <addr> -dir <d>         serve <d> under /feed/ (what the updater polls)
//
// Same signature format CI produces (base64 raw 64-byte Ed25519 over the manifest bytes).
// See contrib/linux-build/e2e/run.sh.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	keygen := flag.String("keygen", "", "write a new seed to this file, print the public key")
	sign := flag.String("sign", "", "sign <dir>/latest.json with the seed in this file")
	serve := flag.String("serve", "", "serve <dir> under /feed/ on this address")
	dir := flag.String("dir", ".", "feed directory")
	flag.Parse()
	switch {
	case *keygen != "":
		seed := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(*keygen, seed, 0o600); err != nil {
			log.Fatal(err)
		}
		fmt.Println(base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)))
	case *sign != "":
		seed, err := os.ReadFile(*sign)
		if err != nil || len(seed) != ed25519.SeedSize {
			log.Fatalf("seed: %v (len %d)", err, len(seed))
		}
		m, err := os.ReadFile(filepath.Join(*dir, "latest.json"))
		if err != nil {
			log.Fatal(err)
		}
		sig := base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(seed), m))
		if err := os.WriteFile(filepath.Join(*dir, "latest.json.sig"), []byte(sig), 0o644); err != nil {
			log.Fatal(err)
		}
	case *serve != "":
		fs := http.StripPrefix("/feed/", http.FileServer(http.Dir(*dir)))
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Printf("GET %s", r.URL.Path)
			fs.ServeHTTP(w, r)
		})
		srv := &http.Server{Addr: *serve, Handler: h, ReadHeaderTimeout: 10 * time.Second}
		log.Fatal(srv.ListenAndServe())
	default:
		flag.Usage()
		os.Exit(2)
	}
}
