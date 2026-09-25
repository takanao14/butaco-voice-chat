package football

import (
	"fmt"
	"strings"
	"time"
)

var jst = time.FixedZone("JST", 9*60*60)

const unavailableFacts = "【試合情報】現在、試合情報を取得できていません。試合の結果、日程、順位、選手を聞かれたら、推測せず分からないと答えてください。"

var competitionNames = map[string]string{
	"eng.1":          "プレミアリーグ",
	"uefa.champions": "チャンピオンズリーグ",
	"eng.league_cup": "リーグカップ",
	"eng.fa":         "FAカップ",
}

// clubNames maps ESPN display names to Japanese. Unknown clubs keep the ESPN
// name, and the prompt asks the model not to read out Latin letters.
var clubNames = map[string]string{
	"AFC Bournemouth":         "ボーンマス",
	"Arsenal":                 "アーセナル",
	"Aston Villa":             "アストン・ヴィラ",
	"Brentford":               "ブレントフォード",
	"Brighton & Hove Albion":  "ブライトン",
	"Burnley":                 "バーンリー",
	"Chelsea":                 "チェルシー",
	"Coventry City":           "コヴェントリー",
	"Crystal Palace":          "クリスタル・パレス",
	"Everton":                 "エヴァートン",
	"Fulham":                  "フラム",
	"Hull City":               "ハル・シティ",
	"Ipswich Town":            "イプスウィッチ",
	"Leeds United":            "リーズ",
	"Leicester City":          "レスター",
	"Liverpool":               "リヴァプール",
	"Manchester City":         "マンチェスター・シティ",
	"Manchester United":       "マンチェスター・ユナイテッド",
	"Newcastle United":        "ニューカッスル",
	"Nottingham Forest":       "ノッティンガム・フォレスト",
	"Southampton":             "サウサンプトン",
	"Sunderland":              "サンダーランド",
	"Tottenham Hotspur":       "トッテナム",
	"West Ham United":         "ウェストハム",
	"Wolverhampton Wanderers": "ウルヴァーハンプトン",
	"Atletico Madrid":         "アトレティコ・マドリード",
	"Barcelona":               "バルセロナ",
	"Bayern Munich":           "バイエルン・ミュンヘン",
	"Borussia Dortmund":       "ドルトムント",
	"Inter Milan":             "インテル",
	"Internazionale":          "インテル",
	"Juventus":                "ユヴェントス",
	"Lille":                   "リール",
	"Napoli":                  "ナポリ",
	"Paris Saint-Germain":     "パリ・サンジェルマン",
	"Real Madrid":             "レアル・マドリード",
}

func clubName(name string) string {
	if japanese, ok := clubNames[name]; ok {
		return japanese
	}
	return name
}

var weekdays = []string{"日", "月", "火", "水", "木", "金", "土"}

func formatDate(t time.Time) string {
	t = t.In(jst)
	return fmt.Sprintf("%d月%d日（%s）", t.Month(), t.Day(), weekdays[t.Weekday()])
}

func formatTime(t time.Time) string {
	return formatDate(t) + fmt.Sprintf("%d時%02d分", t.In(jst).Hour(), t.In(jst).Minute())
}

// relativeDay compares JST calendar dates, so a match at 23:00 yesterday is
// "昨日" even when less than 24 hours have passed.
func relativeDay(t, now time.Time) string {
	a, b := t.In(jst), now.In(jst)
	days := int(time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, jst).Sub(time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, jst)).Hours() / 24)
	switch {
	case days == 0:
		return "今日"
	case days == -1:
		return "昨日"
	case days == 1:
		return "明日"
	case days < 0:
		return fmt.Sprintf("%d日前", -days)
	default:
		return fmt.Sprintf("%d日後", days)
	}
}

// perspective returns the followed team's side, the opponent, and whether the
// team plays at home.
func perspective(match Match, teamID string) (Side, Side, bool) {
	if match.Home.TeamID == teamID {
		return match.Home, match.Away, true
	}
	return match.Away, match.Home, false
}

func result(match Match, teamID string) string {
	us, them, _ := perspective(match, teamID)
	switch {
	case us.Score > them.Score:
		return "勝ち"
	case us.Score < them.Score:
		return "負け"
	case us.Winner:
		return "引き分け（PK戦で勝ち）"
	case them.Winner:
		return "引き分け（PK戦で負け）"
	default:
		return "引き分け"
	}
}

func venue(home bool) string {
	if home {
		return "ホーム"
	}
	return "アウェイ"
}

func describe(match Match, teamID string, now time.Time) string {
	_, them, home := perspective(match, teamID)
	return fmt.Sprintf("%s（%s）%s、%sで%sと対戦", formatTime(match.Kickoff), relativeDay(match.Kickoff, now),
		competitionNames[match.Competition], venue(home), clubName(them.Name))
}

// minute turns ESPN clocks such as "31'" and "90'+2'" into "31分" and "90+2分".
func minute(clock string) string {
	return strings.ReplaceAll(clock, "'", "") + "分"
}

func goalLine(match Match) string {
	if len(match.Goals) == 0 {
		return ""
	}
	var parts []string
	for _, goal := range match.Goals {
		side := match.Away.Name
		if goal.TeamID == match.Home.TeamID {
			side = match.Home.Name
		}
		part := fmt.Sprintf("%s %s %s", clubName(side), minute(goal.Minute), goal.Scorer)
		switch {
		case strings.Contains(goal.Kind, "Own Goal"):
			part = fmt.Sprintf("%s %s %sのオウンゴール", clubName(side), minute(goal.Minute), goal.Scorer)
		case strings.Contains(goal.Kind, "Penalty"):
			part += "（PK）"
		case goal.Assist != "":
			part += "（アシスト " + goal.Assist + "）"
		}
		parts = append(parts, part)
	}
	return "得点: " + strings.Join(parts, "、") + "。"
}

// Summarize renders the snapshot as prompt facts. Every judgement the model
// could get wrong (time zone, result side, table order) is computed here.
func Summarize(snapshot Snapshot, teams []Team, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "【試合情報】ESPN から %s に取得。日時は日本時間。今日は %s。\n", formatTime(snapshot.FetchedAt), formatDate(now))
	b.WriteString("スコアはそのチームから見た順で書いてある。この情報にない試合結果、日程、順位、選手は推測せず、分からないと答えること。試合中の途中経過は分からない。\n")
	standings := map[string]Standing{}
	for _, standing := range snapshot.Standings {
		standings[standing.TeamID] = standing
	}
	for _, team := range teams {
		label := team.Name
		if team.Fan != "" {
			label += "（" + team.Fan + "が応援）"
		}
		fmt.Fprintf(&b, "\n■%s\n", label)
		matches := snapshot.Matches[team.ID]
		var form []string
		var next *Match
		for i := len(matches) - 1; i >= 0; i-- {
			match := matches[i]
			switch {
			case match.Completed:
				if len(form) == 0 {
					us, them, _ := perspective(match, team.ID)
					fmt.Fprintf(&b, "直近の試合: %sし、%d-%d で%s。%s\n", describe(match, team.ID, now), us.Score, them.Score, result(match, team.ID), goalLine(match))
				}
				if len(form) < 5 {
					form = append(form, result(match, team.ID))
				}
			case match.State == "in":
				fmt.Fprintf(&b, "現在試合中: %s中。途中経過は分からない。\n", describe(match, team.ID, now))
			case match.State == "pre" && match.Kickoff.After(now):
				next = &matches[i]
			}
		}
		if len(form) == 0 {
			b.WriteString("直近の試合: 情報なし。\n")
		}
		today := "なし"
		for _, match := range matches {
			if relativeDay(match.Kickoff, now) == "今日" {
				today = "あり（" + describe(match, team.ID, now) + "）"
			}
		}
		fmt.Fprintf(&b, "今日の試合: %s。\n", today)
		if next != nil {
			fmt.Fprintf(&b, "次の試合: %sする予定。\n", describe(*next, team.ID, now))
		} else {
			b.WriteString("次の試合: 情報なし。\n")
		}
		if len(form) > 0 {
			fmt.Fprintf(&b, "直近%d試合の結果（新しい順）: %s。\n", len(form), strings.Join(form, "、"))
		}
		if standing, ok := standings[team.ID]; ok {
			fmt.Fprintf(&b, "プレミアリーグ順位: %d位、勝点%d（%d試合 %d勝%d分%d敗）。\n",
				standing.Rank, standing.Points, standing.Played, standing.Wins, standing.Draws, standing.Losses)
		}
	}
	if len(teams) == 2 {
		first, firstOK := standings[teams[0].ID]
		second, secondOK := standings[teams[1].ID]
		if firstOK && secondOK {
			upper, upperRank, lower, lowerRank := teams[0].Name, first.Rank, teams[1].Name, second.Rank
			if second.Rank < first.Rank {
				upper, upperRank, lower, lowerRank = lower, lowerRank, upper, upperRank
			}
			fmt.Fprintf(&b, "\n順位の比較: %s（%d位）のほうが%s（%d位）より上。\n", upper, upperRank, lower, lowerRank)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
