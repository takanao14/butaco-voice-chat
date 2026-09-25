package butako

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// NewHandler serves the UI and API. facts may be nil.
func NewHandler(config Config, facts Facts) http.Handler {
	s := &service{config: config, client: &http.Client{}, speech: config.Character.speechReplacer(), facts: facts}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"deadlineMs":    config.Deadline.Milliseconds(),
			"maxAudioBytes": maxAudioBytes,
			"name":          config.Character.Name,
			"credit":        config.Character.Credit,
		})
	})
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("POST /api/conversation", s.conversation)
	staticFiles := http.FileServer(http.Dir(config.StaticDir))
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r.Header.Del("If-Modified-Since")
		r.Header.Del("If-None-Match")
		staticFiles.ServeHTTP(w, r)
	}))
	return mux
}

func (s *service) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	status := "unavailable"
	if raw, err := s.get(ctx, s.config.LemonadeURL, "/api/v1/health"); err == nil {
		var health struct {
			Status string `json:"status"`
			Loaded []struct {
				Name   string `json:"model_name"`
				Status string `json:"status"`
			} `json:"all_models_loaded"`
		}
		if json.Unmarshal(raw, &health) == nil && health.Status == "ok" {
			status = "model_unloaded"
			asrReady, llmReady := false, false
			for _, model := range health.Loaded {
				if model.Name == s.config.ASRModel && model.Status == "ready" {
					asrReady = true
				}
				if model.Name == s.config.LLMModel && model.Status == "ready" {
					llmReady = true
				}
			}
			if asrReady && llmReady {
				status = "ready"
			}
		}
	}
	voicevoxReady := false
	if _, err := s.get(ctx, s.config.VoicevoxURL, "/version"); err == nil {
		voicevoxReady = true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"lemonade":      status,
		"voicevoxReady": voicevoxReady,
	})
}

func (s *service) conversation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), s.config.Deadline)
	defer cancel()
	// Rejecting other media types forces a CORS preflight for cross-site posts.
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "audio/wav" {
		writeError(w, http.StatusUnsupportedMediaType, "invalid_audio")
		return
	}
	if r.ContentLength > maxAudioBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "audio_too_large")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAudioBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "audio_too_large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_audio")
		}
		return
	}
	if err := validateWAV(body); err != nil {
		if errors.Is(err, errSilence) {
			writeError(w, http.StatusUnprocessableEntity, "silence")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_audio")
		}
		return
	}
	history, err := parseHistory(r.Header.Get(historyHeader))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_history")
		return
	}
	result, err := s.converse(ctx, body, history)
	if err != nil {
		var stage *stageError
		if errors.As(err, &stage) {
			writeError(w, stage.status, stage.code)
		} else {
			writeError(w, http.StatusBadGateway, "conversation_failed")
		}
		return
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > maxResponseBytes {
		writeError(w, http.StatusBadGateway, "response_too_large")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

const (
	historyHeader   = "X-Butako-History"
	maxHistoryTurns = 3
	maxHistoryRunes = 500
)

// parseHistory decodes the browser's recent turns: base64url of a JSON array.
// The server keeps nothing between requests.
func parseHistory(value string) ([]Turn, error) {
	if value == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	var history []Turn
	if err := json.Unmarshal(raw, &history); err != nil {
		return nil, err
	}
	if len(history) > maxHistoryTurns {
		return nil, errors.New("too many turns")
	}
	for _, turn := range history {
		for _, text := range []string{turn.User, turn.Assistant} {
			if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > maxHistoryRunes {
				return nil, errors.New("invalid turn")
			}
		}
	}
	return history, nil
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
