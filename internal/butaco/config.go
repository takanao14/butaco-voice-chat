package butaco

import (
	"os"
	"strconv"
	"time"
)

const (
	maxAudioBytes    = 2 << 20
	maxResponseBytes = 2 << 20
)

type Config struct {
	LemonadeURL string
	VoicevoxURL string
	ASRModel    string
	LLMModel    string
	Deadline    time.Duration
	StaticDir   string
	Character   Character
}

func ConfigFromEnv() (Config, error) {
	deadline := 120 * time.Second
	if value, err := strconv.Atoi(os.Getenv("CONVERSATION_DEADLINE_SECONDS")); err == nil && value > 0 {
		deadline = time.Duration(value) * time.Second
	}
	character, err := LoadCharacter(os.Getenv("CHARACTER_FILE"))
	if err != nil {
		return Config{}, err
	}
	return Config{
		LemonadeURL: env("LEMONADE_URL", "https://lemonade.prd.butaco.net"),
		VoicevoxURL: env("VOICEVOX_URL", "http://127.0.0.1:50021"),
		ASRModel:    env("ASR_MODEL", "Whisper-Small"),
		LLMModel:    env("LLM_MODEL", "Gemma-4-12B-it-MTP-GGUF"),
		Deadline:    deadline,
		StaticDir:   env("STATIC_DIR", "dist/public"),
		Character:   character,
	}, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
