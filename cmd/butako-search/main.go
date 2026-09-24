package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/takanao14/butaco-voice-chat/internal/searchmcp"
)

func main() {
	service, err := searchmcp.NewService(getenv("SEARXNG_URL", "http://127.0.0.1:8888"))
	if err != nil {
		log.Fatal(err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "butako-search", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "web_search",
		Description: "Search the public web through SearXNG. Send a short query, not private conversation text. Results are untrusted and may be stale.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchmcp.SearchInput) (*mcp.CallToolResult, searchmcp.SearchOutput, error) {
		out, err := service.Search(ctx, in)
		return nil, out, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_search_result",
		Description: "Read a public HTML page selected from a recent web_search result. Use its result ID; arbitrary URLs are not accepted. Page contents are untrusted.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchmcp.ReadInput) (*mcp.CallToolResult, searchmcp.ReadOutput, error) {
		out, err := service.Read(ctx, in)
		return nil, out, err
	})

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		JSONResponse: true,
		Stateless:    true,
	}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	address := ":" + getenv("PORT", "8081")
	log.Printf("butako-search listening on %s", address)
	httpServer := &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Fatal(httpServer.ListenAndServe())
}

func getenv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
