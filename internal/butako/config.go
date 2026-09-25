package butako

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
	LemonadeURL  string
	VoicevoxURL  string
	SearchMCPURL string
	ASRModel     string
	LLMModel     string
	Speaker      int
	Deadline     time.Duration
	StaticDir    string
}

func ConfigFromEnv() Config {
	speaker := 3
	if value, err := strconv.Atoi(os.Getenv("VOICEVOX_SPEAKER")); err == nil {
		speaker = value
	}
	deadline := 120 * time.Second
	if value, err := strconv.Atoi(os.Getenv("CONVERSATION_DEADLINE_SECONDS")); err == nil && value > 0 {
		deadline = time.Duration(value) * time.Second
	}
	return Config{
		LemonadeURL:  env("LEMONADE_URL", "https://lemonade.prd.butaco.net"),
		VoicevoxURL:  env("VOICEVOX_URL", "http://127.0.0.1:50021"),
		SearchMCPURL: os.Getenv("SEARCH_MCP_URL"),
		ASRModel:     env("ASR_MODEL", "Whisper-Small"),
		LLMModel:     env("LLM_MODEL", "Gemma-4-12B-it-MTP-GGUF"),
		Speaker:      speaker,
		Deadline:     deadline,
		StaticDir:    env("STATIC_DIR", "dist/public"),
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
