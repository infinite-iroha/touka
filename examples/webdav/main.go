package main

import (
	"log"
	"os"
	"time"

	"github.com/infinite-iroha/touka"
	"github.com/infinite-iroha/touka/webdav"
)

func main() {
	r := touka.Default()

	// Create a directory for the OS file system.
	if err := os.MkdirAll("public", 0755); err != nil {
		log.Fatal(err)
	}

	// Serve the "public" directory on the "/webdav/" route.
	closer, err := webdav.Serve(r, "/webdav", "public")
	if err != nil {
		log.Fatal(err)
	}
	defer closer.Close()

	log.Println("Touka WebDAV Server starting on :8080...")
	if err := r.RunShutdown(":8080", 10*time.Second); err != nil {
		log.Fatalf("Touka server failed to start: %v", err)
	}
}
