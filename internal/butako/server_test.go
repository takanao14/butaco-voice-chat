package butako

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// upstream fakes Lemonade and VOICEVOX. Each field overrides one endpoint.
type upstream struct {
	health, transcription, chat, audioQuery, synthesis http.HandlerFunc
}

func respond(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// hang consumes the body first; the server only notices a client disconnect
// after the request body has been read.
func hang(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	<-r.Context().Done()
}

func newTestHandler(t *testing.T, fake upstream) http.Handler {
	t.Helper()
	pick := func(handler, fallback http.HandlerFunc) http.HandlerFunc {
		if handler != nil {
			return handler
		}
		return fallback
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", pick(fake.health, respond(200,
		`{"status":"ok","all_models_loaded":[{"model_name":"asr","status":"ready"},{"model_name":"llm","status":"ready"}]}`)))
	mux.HandleFunc("/v1/audio/transcriptions", pick(fake.transcription, respond(200, `{"text":"ブタコ、元気？"}`)))
	mux.HandleFunc("/v1/chat/completions", pick(fake.chat, respond(200, `{"choices":[{"message":{"content":"フゴー、元気だよ！"}}]}`)))
	mux.HandleFunc("/version", respond(200, `"0.25.2"`))
	mux.HandleFunc("/audio_query", pick(fake.audioQuery, respond(200, `{"accent_phrases":[]}`)))
	mux.HandleFunc("/synthesis", pick(fake.synthesis, respond(200, string(testWAV(1600, 0.3)))))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return NewHandler(Config{
		LemonadeURL: server.URL,
		VoicevoxURL: server.URL,
		ASRModel:    "asr",
		LLMModel:    "llm",
		Speaker:     3,
		Deadline:    500 * time.Millisecond,
		StaticDir:   t.TempDir(),
	})
}

func postConversation(handler http.Handler, contentType string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/conversation", bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %q", recorder.Body.String())
	}
	return body.Error.Code
}

func TestConversationSuccess(t *testing.T) {
	recorder := postConversation(newTestHandler(t, upstream{}), "audio/wav", testWAV(16000, 0.3))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var result conversationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Transcript != "ブタコ、元気？" || result.SpeechText != "フゴー、元気だよ！" || result.AudioWAVBase64 == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestConversationErrors(t *testing.T) {
	voice := testWAV(16000, 0.3)
	tests := []struct {
		name        string
		fake        upstream
		contentType string
		body        []byte
		status      int
		code        string
	}{
		{"cross-site text/plain", upstream{}, "text/plain", voice, 415, "invalid_audio"},
		{"not wav", upstream{}, "audio/wav", []byte(strings.Repeat("x", 100)), 400, "invalid_audio"},
		{"too large", upstream{}, "audio/wav", make([]byte, maxAudioBytes+1), 413, "audio_too_large"},
		{"silent", upstream{}, "audio/wav", testWAV(16000, 0), 422, "silence"},
		{"hallucinated", upstream{transcription: respond(200, `{"text":"ご視聴ありがとうございました。"}`)}, "audio/wav", voice, 422, "silence"},
		{"asr error", upstream{transcription: respond(500, "boom")}, "audio/wav", voice, 502, "asr_failed"},
		{"asr not json", upstream{transcription: respond(200, "boom")}, "audio/wav", voice, 502, "asr_failed"},
		{"llm error", upstream{chat: respond(500, "boom")}, "audio/wav", voice, 502, "llm_failed"},
		{"llm empty", upstream{chat: respond(200, `{"choices":[{"message":{"content":" "}}]}`)}, "audio/wav", voice, 502, "llm_failed"},
		{"llm emoji only", upstream{chat: respond(200, `{"choices":[{"message":{"content":"🐷"}}]}`)}, "audio/wav", voice, 502, "llm_failed"},
		{"tts query error", upstream{audioQuery: respond(500, "boom")}, "audio/wav", voice, 502, "tts_failed"},
		{"tts not wav", upstream{synthesis: respond(200, "not a wav")}, "audio/wav", voice, 502, "tts_failed"},
		{"tts too large", upstream{synthesis: respond(200, string(make([]byte, maxResponseBytes+1)))}, "audio/wav", voice, 502, "tts_failed"},
		{"response too large", upstream{synthesis: respond(200, string(testWAV(800000, 0.3)))}, "audio/wav", voice, 502, "response_too_large"},
		{"timeout", upstream{chat: hang}, "audio/wav", voice, 504, "timeout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := postConversation(newTestHandler(t, test.fake), test.contentType, test.body)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d (body %s)", recorder.Code, test.status, recorder.Body)
			}
			if code := errorCode(t, recorder); code != test.code {
				t.Fatalf("code = %q, want %q", code, test.code)
			}
		})
	}
}

func TestConversationLemonadeUnavailable(t *testing.T) {
	handler := NewHandler(Config{
		LemonadeURL: "http://127.0.0.1:1",
		VoicevoxURL: "http://127.0.0.1:1",
		Deadline:    time.Second,
	})
	recorder := postConversation(handler, "audio/wav", testWAV(16000, 0.3))
	if recorder.Code != http.StatusServiceUnavailable || errorCode(t, recorder) != "lemonade_unavailable" {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
}

func TestStatus(t *testing.T) {
	tests := []struct {
		name   string
		health http.HandlerFunc
		want   string
	}{
		{"ready", nil, "ready"},
		{"model unloaded", respond(200, `{"status":"ok","all_models_loaded":[{"model_name":"asr","status":"ready"}]}`), "model_unloaded"},
		{"health error", respond(503, "down"), "unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newTestHandler(t, upstream{health: test.health})
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))
			want := fmt.Sprintf(`{"lemonade":%q,"voicevoxReady":true}`, test.want)
			if got := strings.TrimSpace(recorder.Body.String()); got != want {
				t.Fatalf("body = %s, want %s", got, want)
			}
		})
	}
}
