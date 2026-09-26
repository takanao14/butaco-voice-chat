package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/takanao14/butaco-voice-chat/internal/butaco"
	"github.com/takanao14/butaco-voice-chat/internal/football"
)

func main() {
	config, err := butaco.ConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	var facts butaco.Facts
	if config.Character.Football != nil {
		matches := football.NewService(football.NewESPN(), *config.Character.Football)
		go matches.Run(context.Background())
		facts = matches
	}
	addr := ":" + getenv("PORT", "8080")
	log.Printf("butaco listening on %s", addr)
	server := &http.Server{
		Addr:              addr,
		Handler:           butaco.NewHandler(config, facts),
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
