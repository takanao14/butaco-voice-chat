// Package football keeps recent match data for a few teams and renders it as
// facts for the conversation prompt. It never receives user speech.
package football

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

// Config selects the teams to follow. Team IDs are ESPN team IDs.
type Config struct {
	Teams []Team `json:"teams"`
}

type Team struct {
	ID   string `json:"espnId"`
	Name string `json:"name"`
	// Fan names who supports the team in the conversation, e.g. "ユーザー".
	Fan string `json:"fan"`
}

func (c Config) Validate() error {
	if len(c.Teams) == 0 {
		return errors.New("football.teams must not be empty")
	}
	for _, team := range c.Teams {
		if team.ID == "" || team.Name == "" {
			return errors.New("football.teams entries need espnId and name")
		}
	}
	return nil
}

// Match is one fixture as seen from the source, not from a followed team.
type Match struct {
	ID          string
	Competition string // ESPN league slug, e.g. "eng.1"
	Kickoff     time.Time
	State       string // "pre", "in", or "post"
	Completed   bool
	Home, Away  Side
	Goals       []Goal
}

type Side struct {
	TeamID   string
	Name     string
	Score    int
	HasScore bool
	Winner   bool
}

type Goal struct {
	Minute string
	Scorer string
	Assist string
	TeamID string
	Kind   string // ESPN key event type, e.g. "Goal - Header", "Own Goal"
}

type Standing struct {
	TeamID                                    string
	Rank, Points, Played, Wins, Draws, Losses int
}

// Snapshot is one complete refresh of every followed team.
type Snapshot struct {
	FetchedAt time.Time
	Matches   map[string][]Match // by followed team ID, oldest first
	Standings []Standing         // Premier League table
}

type Source interface {
	Fetch(ctx context.Context, teams []Team) (Snapshot, error)
}

// Service refreshes a Snapshot in the background and renders prompt facts.
type Service struct {
	source   Source
	teams    []Team
	interval time.Duration
	maxAge   time.Duration

	mu       sync.RWMutex
	snapshot *Snapshot
}

func NewService(source Source, config Config) *Service {
	return &Service{source: source, teams: config.Teams, interval: 30 * time.Minute, maxAge: 3 * time.Hour}
}

// Run refreshes immediately and then every interval until ctx is done. A
// failed refresh keeps the previous snapshot until it is older than maxAge.
func (s *Service) Run(ctx context.Context) {
	for {
		s.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.interval):
		}
	}
}

func (s *Service) refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	snapshot, err := s.source.Fetch(ctx, s.teams)
	if err != nil {
		log.Printf("football refresh failed: %v", err)
		return
	}
	s.mu.Lock()
	s.snapshot = &snapshot
	s.mu.Unlock()
}

// Facts returns the prompt section for now. It reports unavailable data
// rather than serving a snapshot older than maxAge.
func (s *Service) Facts(now time.Time) string {
	s.mu.RLock()
	snapshot := s.snapshot
	s.mu.RUnlock()
	if snapshot == nil || now.Sub(snapshot.FetchedAt) > s.maxAge {
		return unavailableFacts
	}
	return Summarize(*snapshot, s.teams, now)
}
