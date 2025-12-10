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

	// Create a new WebDAV handler with the OS file system.
	fs, err := webdav.NewOSFS("public")
	if err != nil {
		log.Fatal(err)
	}
	handler := webdav.NewHandler("/webdav", fs, webdav.NewMemLock(), log.New(os.Stdout, "", 0))

	// Mount the WebDAV handler on the "/webdav/" route.
	webdavMethods := []string{
		"OPTIONS", "GET", "HEAD", "DELETE", "PUT", "MKCOL", "COPY", "MOVE", "PROPFIND", "PROPPATCH", "LOCK", "UNLOCK",
	}
	r.HandleFunc(webdavMethods, "/webdav/*path", handler.ServeTouka)

	log.Println("Touka WebDAV Server starting on :8080...")
	if err := r.RunShutdown(":8080", 10*time.Second); err != nil {
		log.Fatalf("Touka server failed to start: %v", err)
	}
}
