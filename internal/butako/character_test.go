package butako

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeCharacter(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "character.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadCharacterDefaultsWithoutFile(t *testing.T) {
	character, err := LoadCharacter("")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(character, DefaultCharacter()) {
		t.Fatalf("character = %+v", character)
	}
}

func TestLoadCharacterOverridesOnlyGivenFields(t *testing.T) {
	path := writeCharacter(t, `{
		"name": "モモ",
		"speaker": 0,
		"speechReplacements": [{"from": "Momo", "to": "モモ"}],
		"hallucinations": []
	}`)
	character, err := LoadCharacter(path)
	if err != nil {
		t.Fatal(err)
	}
	defaults := DefaultCharacter()
	if character.Name != "モモ" || character.Speaker != 0 {
		t.Fatalf("name/speaker not overridden: %+v", character)
	}
	if character.SystemPrompt != defaults.SystemPrompt || character.Credit != defaults.Credit {
		t.Fatal("omitted fields should keep defaults")
	}
	if got := speechText(character.speechReplacer(), "Momo と Butako"); got != "モモ と Butako" {
		t.Fatalf("replacements should replace the defaults, got %q", got)
	}
	if emptyOrHallucinated("ご視聴ありがとうございました", character.Hallucinations) {
		t.Fatal("an empty hallucination list should disable the defaults")
	}
}

func TestLoadCharacterRejectsInvalidFiles(t *testing.T) {
	for name, content := range map[string]string{
		"unknown field":    `{"systemPromt": "typo"}`,
		"empty name":       `{"name": " "}`,
		"negative speaker": `{"speaker": -1}`,
		"empty from":       `{"speechReplacements": [{"from": "", "to": "x"}]}`,
		"not json":         `name: ブタコ`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadCharacter(writeCharacter(t, content)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if _, err := LoadCharacter(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}
