package football

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// competitions are the ESPN league slugs followed for every team.
var competitions = []string{"eng.1", "uefa.champions", "eng.league_cup", "eng.fa"}

// window bounds how far back and ahead matches are kept. It also drops the
// previous season's cup matches that ESPN returns for some competitions.
const window = 60 * 24 * time.Hour

// ESPN reads ESPN's undocumented site API. See ADR-0056 in the homelab repo.
type ESPN struct {
	Client  *http.Client
	BaseURL string
	Now     func() time.Time
}

func NewESPN() *ESPN {
	return &ESPN{Client: &http.Client{Timeout: 15 * time.Second}, BaseURL: "https://site.api.espn.com", Now: time.Now}
}

func (e *ESPN) Fetch(ctx context.Context, teams []Team) (Snapshot, error) {
	now := e.Now()
	snapshot := Snapshot{FetchedAt: now, Matches: map[string][]Match{}}
	for _, team := range teams {
		byID := map[string]Match{}
		for _, competition := range competitions {
			for _, query := range []string{"", "?fixture=true"} {
				path := fmt.Sprintf("/apis/site/v2/sports/soccer/%s/teams/%s/schedule%s", competition, url.PathEscape(team.ID), query)
				var response scheduleResponse
				if err := e.get(ctx, path, &response); err != nil {
					return Snapshot{}, err
				}
				for _, event := range response.Events {
					match, err := event.match(competition)
					if err != nil {
						return Snapshot{}, fmt.Errorf("%s event %s: %w", competition, event.ID, err)
					}
					if match.Kickoff.After(now.Add(-window)) && match.Kickoff.Before(now.Add(window)) {
						byID[match.ID] = match
					}
				}
			}
		}
		matches := make([]Match, 0, len(byID))
		for _, match := range byID {
			matches = append(matches, match)
		}
		sort.Slice(matches, func(i, j int) bool { return matches[i].Kickoff.Before(matches[j].Kickoff) })
		if latest := latestCompleted(matches); latest >= 0 {
			goals, err := e.goals(ctx, matches[latest])
			if err != nil {
				return Snapshot{}, err
			}
			matches[latest].Goals = goals
		}
		snapshot.Matches[team.ID] = matches
	}
	standings, err := e.standings(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Standings = standings
	return snapshot, nil
}

func (e *ESPN) get(ctx context.Context, path string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, e.BaseURL+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := e.Client.Do(request)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", path, response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(target); err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	return nil
}

type scheduleResponse struct {
	Events []scheduleEvent `json:"events"`
}

type scheduleEvent struct {
	ID           string `json:"id"`
	Date         string `json:"date"`
	Competitions []struct {
		Status struct {
			Type struct {
				State     string `json:"state"`
				Completed bool   `json:"completed"`
			} `json:"type"`
		} `json:"status"`
		Competitors []struct {
			HomeAway string `json:"homeAway"`
			Winner   bool   `json:"winner"`
			Team     struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
			} `json:"team"`
			Score json.RawMessage `json:"score"`
		} `json:"competitors"`
	} `json:"competitions"`
}

func (event scheduleEvent) match(competition string) (Match, error) {
	kickoff, err := parseTime(event.Date)
	if err != nil {
		return Match{}, err
	}
	if event.ID == "" || len(event.Competitions) == 0 || len(event.Competitions[0].Competitors) != 2 {
		return Match{}, fmt.Errorf("unexpected event shape")
	}
	detail := event.Competitions[0]
	match := Match{
		ID:          event.ID,
		Competition: competition,
		Kickoff:     kickoff,
		State:       detail.Status.Type.State,
		Completed:   detail.Status.Type.Completed,
	}
	for _, competitor := range detail.Competitors {
		side := Side{TeamID: competitor.Team.ID, Name: competitor.Team.DisplayName, Winner: competitor.Winner}
		side.Score, side.HasScore = parseScore(competitor.Score)
		switch competitor.HomeAway {
		case "home":
			match.Home = side
		case "away":
			match.Away = side
		default:
			return Match{}, fmt.Errorf("unexpected homeAway %q", competitor.HomeAway)
		}
	}
	if match.Home.TeamID == "" || match.Away.TeamID == "" {
		return Match{}, fmt.Errorf("missing home or away team")
	}
	if match.Completed && !(match.Home.HasScore && match.Away.HasScore) {
		return Match{}, fmt.Errorf("completed match without score")
	}
	return match, nil
}

func parseTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04Z07:00"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unexpected date %q", value)
}

// parseScore accepts the schedule endpoint's object and the scoreboard's string.
func parseScore(raw json.RawMessage) (int, bool) {
	var object struct {
		DisplayValue string `json:"displayValue"`
	}
	text := ""
	if json.Unmarshal(raw, &object) == nil && object.DisplayValue != "" {
		text = object.DisplayValue
	} else if json.Unmarshal(raw, &text) != nil {
		return 0, false
	}
	score, err := strconv.Atoi(strings.TrimSpace(text))
	return score, err == nil
}

func latestCompleted(matches []Match) int {
	for i := len(matches) - 1; i >= 0; i-- {
		if matches[i].Completed {
			return i
		}
	}
	return -1
}

func (e *ESPN) goals(ctx context.Context, match Match) ([]Goal, error) {
	var response struct {
		KeyEvents []struct {
			ScoringPlay bool `json:"scoringPlay"`
			Type        struct {
				Text string `json:"text"`
			} `json:"type"`
			Clock struct {
				DisplayValue string `json:"displayValue"`
			} `json:"clock"`
			Team struct {
				ID string `json:"id"`
			} `json:"team"`
			Participants []struct {
				Athlete struct {
					DisplayName string `json:"displayName"`
				} `json:"athlete"`
			} `json:"participants"`
		} `json:"keyEvents"`
	}
	path := fmt.Sprintf("/apis/site/v2/sports/soccer/%s/summary?event=%s", match.Competition, url.QueryEscape(match.ID))
	if err := e.get(ctx, path, &response); err != nil {
		return nil, err
	}
	var goals []Goal
	for _, event := range response.KeyEvents {
		// The summary lists goals by minute, so plays without a clock are skipped.
		if !event.ScoringPlay || len(event.Participants) == 0 || event.Clock.DisplayValue == "" {
			continue
		}
		goal := Goal{
			Minute: event.Clock.DisplayValue,
			Scorer: event.Participants[0].Athlete.DisplayName,
			TeamID: event.Team.ID,
			Kind:   event.Type.Text,
		}
		if len(event.Participants) > 1 {
			goal.Assist = event.Participants[1].Athlete.DisplayName
		}
		goals = append(goals, goal)
	}
	return goals, nil
}

func (e *ESPN) standings(ctx context.Context) ([]Standing, error) {
	var response struct {
		Children []struct {
			Standings struct {
				Entries []struct {
					Team struct {
						ID string `json:"id"`
					} `json:"team"`
					Stats []struct {
						Name  string  `json:"name"`
						Value float64 `json:"value"`
					} `json:"stats"`
				} `json:"entries"`
			} `json:"standings"`
		} `json:"children"`
	}
	if err := e.get(ctx, "/apis/v2/sports/soccer/eng.1/standings", &response); err != nil {
		return nil, err
	}
	if len(response.Children) == 0 || len(response.Children[0].Standings.Entries) < 10 {
		return nil, fmt.Errorf("standings: unexpected shape")
	}
	var standings []Standing
	for _, entry := range response.Children[0].Standings.Entries {
		standing := Standing{TeamID: entry.Team.ID}
		for _, stat := range entry.Stats {
			value := int(stat.Value)
			switch stat.Name {
			case "rank":
				standing.Rank = value
			case "points":
				standing.Points = value
			case "gamesPlayed":
				standing.Played = value
			case "wins":
				standing.Wins = value
			case "ties":
				standing.Draws = value
			case "losses":
				standing.Losses = value
			}
		}
		if standing.TeamID == "" || standing.Rank == 0 {
			return nil, fmt.Errorf("standings: entry without team or rank")
		}
		standings = append(standings, standing)
	}
	return standings, nil
}
