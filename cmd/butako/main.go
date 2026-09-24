package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/takanao14/butaco-voice-chat/internal/butako"
)

func main() {
	config := butako.ConfigFromEnv()
	addr := ":" + getenv("PORT", "8080")
	log.Printf("butako listening on %s", addr)
	server := &http.Server{
		Addr:              addr,
		Handler:           butako.NewHandler(config),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func getenv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
