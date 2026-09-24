package searchmcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	maxQueryRunes    = 200
	maxSearchResults = 8
	maxSearchBytes   = 1 << 20
	maxStoredResults = 256
	resultLifetime   = 10 * time.Minute
)

type SearchInput struct {
	Query     string `json:"query" jsonschema:"Short web search query, without the full conversation or audio transcript"`
	Freshness string `json:"freshness,omitempty" jsonschema:"Optional time range: day, month, or year"`
	Language  string `json:"language,omitempty" jsonschema:"Optional language: ja-JP, en-US, or all"`
}

type SearchResult struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Excerpt     string `json:"excerpt"`
	PublishedAt string `json:"publishedAt,omitempty"`
	RetrievedAt string `json:"retrievedAt"`
}

type SearchOutput struct {
	Results       []SearchResult `json:"results"`
	FailedEngines []string       `json:"failedEngines,omitempty"`
}

type storedResult struct {
	title   string
	url     string
	expires time.Time
}

type Service struct {
	searxngURL string
	searchHTTP *http.Client
	pageHTTP   *http.Client
	mu         sync.Mutex
	results    map[string]storedResult
}

func NewService(searxngURL string) (*Service, error) {
	u, err := url.Parse(searxngURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("SEARXNG_URL must be an http(s) origin")
	}
	return &Service{
		searxngURL: strings.TrimRight(u.String(), "/"),
		searchHTTP: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		pageHTTP: newPageClient(),
		results:  make(map[string]storedResult),
	}, nil
}

func (s *Service) Search(ctx context.Context, in SearchInput) (SearchOutput, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" || len([]rune(query)) > maxQueryRunes {
		return SearchOutput{}, errors.New("query must contain 1 to 200 characters")
	}
	if in.Freshness != "" && in.Freshness != "day" && in.Freshness != "month" && in.Freshness != "year" {
		return SearchOutput{}, errors.New("freshness must be day, month, or year")
	}
	if in.Language != "" && in.Language != "ja-JP" && in.Language != "en-US" && in.Language != "all" {
		return SearchOutput{}, errors.New("language must be ja-JP, en-US, or all")
	}
	lang := in.Language
	if lang == "" {
		lang = "ja-JP"
	}
	values := url.Values{"q": {query}, "format": {"json"}, "language": {lang}}
	if in.Freshness != "" {
		values.Set("time_range", in.Freshness)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.searxngURL+"/search", strings.NewReader(values.Encode()))
	if err != nil {
		return SearchOutput{}, errors.New("could not create search request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.searchHTTP.Do(request)
	if err != nil {
		return SearchOutput{}, errors.New("search service unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return SearchOutput{}, fmt.Errorf("search service returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxSearchBytes+1))
	if err != nil || len(data) > maxSearchBytes {
		return SearchOutput{}, errors.New("search response unavailable or too large")
	}
	var parsed struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Content       string `json:"content"`
			PublishedDate string `json:"publishedDate"`
		} `json:"results"`
		UnresponsiveEngines [][]string `json:"unresponsive_engines"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return SearchOutput{}, errors.New("invalid search response")
	}
	now := time.Now().UTC()
	output := SearchOutput{Results: make([]SearchResult, 0, maxSearchResults)}
	for _, failure := range parsed.UnresponsiveEngines {
		if len(output.FailedEngines) == 8 {
			break
		}
		if len(failure) >= 2 {
			output.FailedEngines = append(output.FailedEngines, truncate(failure[0], 60)+": "+truncate(failure[1], 100))
		}
	}
	seen := make(map[string]bool)
	for _, item := range parsed.Results {
		if len(output.Results) == maxSearchResults {
			break
		}
		if seen[item.URL] || !validPageURL(item.URL) || strings.TrimSpace(item.Title) == "" {
			continue
		}
		seen[item.URL] = true
		id, err := randomID()
		if err != nil {
			return SearchOutput{}, errors.New("could not identify search result")
		}
		output.Results = append(output.Results, SearchResult{
			ID:          id,
			Title:       truncate(item.Title, 180),
			URL:         item.URL,
			Excerpt:     truncate(plainText(item.Content), 500),
			PublishedAt: item.PublishedDate,
			RetrievedAt: now.Format(time.RFC3339),
		})
		s.remember(id, storedResult{title: item.Title, url: item.URL, expires: now.Add(resultLifetime)})
	}
	return output, nil
}

func (s *Service) remember(id string, result storedResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, existing := range s.results {
		if time.Now().After(existing.expires) {
			delete(s.results, key)
		}
	}
	for len(s.results) >= maxStoredResults {
		for key := range s.results {
			delete(s.results, key)
			break
		}
	}
	s.results[id] = result
}

func (s *Service) lookup(id string) (storedResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, ok := s.results[id]
	if !ok || time.Now().After(result.expires) {
		delete(s.results, id)
		return storedResult{}, false
	}
	return result, true
}

func randomID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func truncate(value string, max int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > max {
		runes = runes[:max]
	}
	return string(runes)
}
