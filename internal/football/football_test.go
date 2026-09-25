package football

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 2026-09-25 17:00 JST.
var testNow = time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)

var testTeams = []Team{{ID: "1", Name: "アーセナル", Fan: "ユーザー"}, {ID: "2", Name: "マンチェスター・ユナイテッド", Fan: "ブタコ"}}

func event(id, date, state string, completed bool, home, away string) string {
	competitor := func(side, spec string) string {
		// spec is "id:name:score:winner"; score "" means no score yet.
		parts := strings.Split(spec, ":")
		score := "null"
		if parts[2] != "" {
			score = `{"value":0,"displayValue":"` + parts[2] + `"}`
		}
		return `{"homeAway":"` + side + `","winner":` + parts[3] + `,"team":{"id":"` + parts[0] + `","displayName":"` + parts[1] + `"},"score":` + score + `}`
	}
	completedJSON := "false"
	if completed {
		completedJSON = "true"
	}
	return `{"id":"` + id + `","date":"` + date + `","competitions":[{"status":{"type":{"state":"` + state + `","completed":` + completedJSON + `}},"competitors":[` +
		competitor("home", home) + `,` + competitor("away", away) + `]}]}`
}

func events(items ...string) string { return `{"events":[` + strings.Join(items, ",") + `]}` }

func standingsJSON() string {
	var entries []string
	for rank := 1; rank <= 12; rank++ {
		id := "90" + strconv.Itoa(rank)
		switch rank {
		case 2:
			id = "1"
		case 12:
			id = "2"
		}
		entries = append(entries, `{"team":{"id":"`+id+`"},"stats":[{"name":"rank","value":`+strconv.Itoa(rank)+`},{"name":"points","value":`+strconv.Itoa(20-rank)+`},{"name":"gamesPlayed","value":5},{"name":"wins","value":4},{"name":"ties","value":0},{"name":"losses","value":1}]}`)
	}
	return `{"children":[{"standings":{"entries":[` + strings.Join(entries, ",") + `]}}]}`
}

func fakeESPN(t *testing.T, responses map[string]string) *ESPN {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		if body, ok := responses[key]; ok {
			if body == "500" {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(body))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/schedule") {
			_, _ = w.Write([]byte(`{"events":[]}`))
			return
		}
		t.Errorf("unexpected request %s", key)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return &ESPN{Client: server.Client(), BaseURL: server.URL, Now: func() time.Time { return testNow }}
}

const base = "/apis/site/v2/sports/soccer/"

func testResponses() map[string]string {
	return map[string]string{
		base + "eng.1/teams/1/schedule": events(
			event("100", "2026-09-19T14:00Z", "post", true, "331:Brighton & Hove Albion:3:true", "1:Arsenal:0:false"),
			// Last season's match falls outside the window and must be dropped.
			event("90", "2026-03-01T15:00Z", "post", true, "1:Arsenal:5:true", "20:Hull City:0:false"),
		),
		base + "eng.1/teams/1/schedule?fixture=true": events(
			event("101", "2026-10-10T11:30Z", "pre", false, "1:Arsenal::false", "357:Leeds United::false"),
			event("102", "2026-10-18T15:30Z", "pre", false, "393:Nottingham Forest::false", "1:Arsenal::false"),
		),
		base + "uefa.champions/teams/1/schedule": events(
			event("103", "2026-09-09T19:00Z", "post", true, "1:Arsenal:1:true", "500:Napoli:1:false"),
		),
		base + "eng.1/teams/2/schedule": events(
			event("200", "2026-09-24T12:00Z", "post", true, "2:Manchester United:2:true", "363:Fulham:1:false"),
		),
		base + "eng.league_cup/teams/2/schedule": events(
			event("201", "2026-09-25T07:00Z", "in", false, "2:Manchester United:0:false", "364:Everton:0:false"),
		),
		base + "eng.1/summary?event=100": `{"keyEvents":[
			{"scoringPlay":true,"type":{"text":"Goal"},"clock":{"displayValue":"31'"},"team":{"id":"331"},"participants":[{"athlete":{"displayName":"Pascal Gross"}},{"athlete":{"displayName":"Charalambos Kostoulas"}}]},
			{"scoringPlay":false,"type":{"text":"Yellow Card"},"clock":{"displayValue":"40'"},"team":{"id":"1"},"participants":[{"athlete":{"displayName":"Someone"}}]},
			{"scoringPlay":true,"type":{"text":"Penalty - Scored"},"clock":{"displayValue":"45'+2'"},"team":{"id":"331"},"participants":[{"athlete":{"displayName":"Danny Welbeck"}}]},
			{"scoringPlay":true,"type":{"text":"Own Goal"},"clock":{"displayValue":"57'"},"team":{"id":"331"},"participants":[{"athlete":{"displayName":"William Saliba"}}]},
			{"scoringPlay":true,"type":{"text":"Goal"},"clock":{"displayValue":""},"team":{"id":"1"},"participants":[{"athlete":{"displayName":"Shootout Kick"}}]}
		]}`,
		base + "eng.1/summary?event=200":         `{"keyEvents":[]}`,
		"/apis/v2/sports/soccer/eng.1/standings": standingsJSON(),
	}
}

func TestFetchAndSummarize(t *testing.T) {
	snapshot, err := fakeESPN(t, testResponses()).Fetch(context.Background(), testTeams)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(snapshot.Matches["1"]); got != 4 {
		t.Fatalf("arsenal matches = %d, want 4 (last season's match dropped)", got)
	}
	if got := len(snapshot.Matches["1"][1].Goals); got != 3 {
		t.Fatalf("goals = %d, want 3 (card and clockless play skipped)", got)
	}

	summary := Summarize(snapshot, testTeams, testNow)
	for _, want := range []string{
		"今日は 9月25日（金）。",
		"■アーセナル（ユーザーが応援）",
		"直近の試合: 9月19日（土）23時00分（6日前）プレミアリーグ、アウェイでブライトンと対戦し、0-3 で負け。",
		"ブライトン 31分 Pascal Gross（アシスト Charalambos Kostoulas）",
		"ブライトン 45+2分 Danny Welbeck（PK）",
		"ブライトン 57分 William Salibaのオウンゴール",
		"次の試合: 10月10日（土）20時30分（15日後）プレミアリーグ、ホームでリーズと対戦する予定。",
		"直近2試合の結果（新しい順）: 負け、引き分け（PK戦で勝ち）。",
		"プレミアリーグ順位: 2位、勝点18",
		"直近の試合: 9月24日（木）21時00分（昨日）プレミアリーグ、ホームでフラムと対戦し、2-1 で勝ち。",
		"現在試合中: 9月25日（金）16時00分（今日）リーグカップ、ホームでエヴァートンと対戦中。途中経過は分からない。",
		"次の試合: 情報なし。",
		"今日の試合: なし。",
		"今日の試合: あり（9月25日（金）16時00分（今日）リーグカップ、ホームでエヴァートンと対戦）。",
		"順位の比較: アーセナル（2位）のほうがマンチェスター・ユナイテッド（12位）より上。",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "Hull") || strings.Contains(summary, "Shootout") {
		t.Errorf("summary includes dropped data:\n%s", summary)
	}
}

func TestFetchFailsOnUnexpectedData(t *testing.T) {
	for name, override := range map[string]map[string]string{
		"http error":                 {base + "eng.fa/teams/2/schedule?fixture=true": "500"},
		"not json":                   {base + "eng.1/teams/1/schedule": "<html>"},
		"completed match no score":   {base + "eng.1/teams/1/schedule": events(event("100", "2026-09-19T14:00Z", "post", true, "331:Brighton::true", "1:Arsenal::false"))},
		"bad date":                   {base + "eng.1/teams/1/schedule": events(event("100", "yesterday", "post", true, "331:Brighton:1:true", "1:Arsenal:0:false"))},
		"standings with few entries": {"/apis/v2/sports/soccer/eng.1/standings": `{"children":[{"standings":{"entries":[]}}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			responses := testResponses()
			for key, value := range override {
				responses[key] = value
			}
			if _, err := fakeESPN(t, responses).Fetch(context.Background(), testTeams); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

type stubSource struct {
	snapshot Snapshot
	err      error
}

func (s *stubSource) Fetch(context.Context, []Team) (Snapshot, error) { return s.snapshot, s.err }

func TestServiceFacts(t *testing.T) {
	source := &stubSource{err: errors.New("down")}
	service := NewService(source, Config{Teams: testTeams})
	service.refresh(context.Background())
	if got := service.Facts(testNow); got != unavailableFacts {
		t.Fatalf("before any success: %q", got)
	}

	source.snapshot, source.err = Snapshot{FetchedAt: testNow, Matches: map[string][]Match{}}, nil
	service.refresh(context.Background())
	if got := service.Facts(testNow); !strings.Contains(got, "■アーセナル") {
		t.Fatalf("after success: %q", got)
	}

	source.err = errors.New("down")
	service.refresh(context.Background())
	if got := service.Facts(testNow.Add(time.Hour)); !strings.Contains(got, "■アーセナル") {
		t.Fatalf("a failed refresh should keep the previous snapshot: %q", got)
	}
	if got := service.Facts(testNow.Add(3*time.Hour + time.Minute)); got != unavailableFacts {
		t.Fatalf("stale snapshot should be unavailable: %q", got)
	}
}

func TestRelativeDay(t *testing.T) {
	for kickoff, want := range map[string]string{
		"2026-09-24T14:59:00Z": "昨日", // 23:59 JST on the 24th
		"2026-09-24T15:00:00Z": "今日", // 00:00 JST on the 25th
		"2026-09-25T15:00:00Z": "明日",
		"2026-09-20T12:00:00Z": "5日前",
		"2026-10-05T12:00:00Z": "10日後",
	} {
		at, _ := time.Parse(time.RFC3339, kickoff)
		if got := relativeDay(at, testNow); got != want {
			t.Errorf("relativeDay(%s) = %q, want %q", kickoff, got, want)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	if err := (Config{}).Validate(); err == nil {
		t.Error("empty teams should fail")
	}
	if err := (Config{Teams: []Team{{ID: "359"}}}).Validate(); err == nil {
		t.Error("team without name should fail")
	}
	if err := (Config{Teams: testTeams}).Validate(); err != nil {
		t.Error(err)
	}
}
