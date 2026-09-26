package football

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestESPNLive checks the real ESPN API shape. Run it with
// BUTACO_ESPN_LIVE=1 go test -run TestESPNLive -v ./internal/football
func TestESPNLive(t *testing.T) {
	if os.Getenv("BUTACO_ESPN_LIVE") == "" {
		t.Skip("set BUTACO_ESPN_LIVE=1 to call ESPN")
	}
	teams := []Team{{ID: "359", Name: "アーセナル", Fan: "ユーザー"}, {ID: "360", Name: "マンチェスター・ユナイテッド", Fan: "ブタコ"}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	snapshot, err := NewESPN().Fetch(ctx, teams)
	if err != nil {
		t.Fatal(err)
	}
	for _, team := range teams {
		if len(snapshot.Matches[team.ID]) == 0 {
			t.Errorf("no matches for %s", team.Name)
		}
	}
	t.Log("\n" + Summarize(snapshot, teams, time.Now()))
}
