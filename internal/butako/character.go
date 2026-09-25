package butako

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/takanao14/butaco-voice-chat/internal/football"
)

// Character holds the persona and voice settings that can change without a
// new application release.
type Character struct {
	Name               string        `json:"name"`
	Credit             string        `json:"credit"`
	SystemPrompt       string        `json:"systemPrompt"`
	ASRPrompt          string        `json:"asrPrompt"`
	Speaker            int           `json:"speaker"`
	SpeechReplacements []Replacement `json:"speechReplacements"`
	Hallucinations     []string      `json:"hallucinations"`
	// Football enables match facts for these teams; nil disables them.
	Football *football.Config `json:"football"`
}

// Replacement rewrites display text before speech synthesis. The list order
// decides which match wins when two entries start at the same position.
type Replacement struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func DefaultCharacter() Character {
	return Character{
		Name:         "ブタコ",
		Credit:       "VOICEVOX:ずんだもん",
		SystemPrompt: "あなたはブタのぬいぐるみ『ブタコ』です。イギリス出身で、マンチェスター・ユナイテッドが好きです。日本語で自然に会話してください。『フゴフゴ』『フゴー』は自然な範囲で使ってください。音声で聞きやすい短い返答を原則2〜3文で作ってください。Markdown、絵文字、英字表記は使わないでください。",
		ASRPrompt:    "ブタコ、フゴー、マンチェスター・ユナイテッド",
		Speaker:      3,
		SpeechReplacements: []Replacement{
			{"Manchester United", "マンチェスター・ユナイテッド"},
			{"Butako", "ブタコ"},
		},
		Hallucinations: []string{"ご視聴ありがとうございました", "字幕をご覧いただきありがとうございました", "Thank you for watching"},
	}
}

// LoadCharacter reads a character file. Fields the file omits keep their
// default values; unknown fields are rejected to catch typos.
func LoadCharacter(path string) (Character, error) {
	character := DefaultCharacter()
	if path == "" {
		return character, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Character{}, err
	}
	var file struct {
		Name               *string          `json:"name"`
		Credit             *string          `json:"credit"`
		SystemPrompt       *string          `json:"systemPrompt"`
		ASRPrompt          *string          `json:"asrPrompt"`
		Speaker            *int             `json:"speaker"`
		SpeechReplacements *[]Replacement   `json:"speechReplacements"`
		Hallucinations     *[]string        `json:"hallucinations"`
		Football           *football.Config `json:"football"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return Character{}, fmt.Errorf("parse %s: %w", path, err)
	}
	set(&character.Name, file.Name)
	set(&character.Credit, file.Credit)
	set(&character.SystemPrompt, file.SystemPrompt)
	set(&character.ASRPrompt, file.ASRPrompt)
	set(&character.Speaker, file.Speaker)
	set(&character.SpeechReplacements, file.SpeechReplacements)
	set(&character.Hallucinations, file.Hallucinations)
	if file.Football != nil {
		character.Football = file.Football
	}
	if err := character.validate(); err != nil {
		return Character{}, fmt.Errorf("%s: %w", path, err)
	}
	return character, nil
}

func set[T any](field *T, value *T) {
	if value != nil {
		*field = *value
	}
}

func (c Character) validate() error {
	if strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.Credit) == "" || strings.TrimSpace(c.SystemPrompt) == "" {
		return errors.New("name, credit, and systemPrompt must not be empty")
	}
	if c.Speaker < 0 {
		return errors.New("speaker must not be negative")
	}
	for _, replacement := range c.SpeechReplacements {
		if replacement.From == "" {
			return errors.New("speechReplacements must not have an empty from")
		}
	}
	if c.Football != nil {
		return c.Football.Validate()
	}
	return nil
}

func (c Character) speechReplacer() *strings.Replacer {
	var pairs []string
	for _, replacement := range c.SpeechReplacements {
		pairs = append(pairs, replacement.From, replacement.To)
	}
	pairs = append(pairs, "**", "", "__", "", "`", "", "#", "", "*", "")
	return strings.NewReplacer(pairs...)
}
