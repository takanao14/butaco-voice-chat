package butaco

import (
	"bytes"
	"encoding/base64"
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
		Deadline:    500 * time.Millisecond,
		StaticDir:   t.TempDir(),
		Character:   DefaultCharacter(),
	}, nil)
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

func readStream(t *testing.T, recorder *httptest.ResponseRecorder) []streamEvent {
	t.Helper()
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/x-ndjson") {
		t.Fatalf("content type = %q", got)
	}
	var events []streamEvent
	decoder := json.NewDecoder(recorder.Body)
	for decoder.More() {
		var event streamEvent
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func eventTypes(events []streamEvent) string {
	var types []string
	for _, event := range events {
		types = append(types, event.Type)
	}
	return strings.Join(types, ",")
}

func TestConversationSuccess(t *testing.T) {
	recorder := postConversation(newTestHandler(t, upstream{}), "audio/wav", testWAV(16000, 0.3))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	events := readStream(t, recorder)
	if eventTypes(events) != "text,audio,done" {
		t.Fatalf("events = %s", eventTypes(events))
	}
	if events[0].Transcript != "ブタコ、元気？" || events[0].SpeechText != "フゴー、元気だよ！" || events[0].Parts != 1 || events[1].AudioWAVBase64 == "" {
		t.Fatalf("unexpected events: %+v", events[:2])
	}
}

func TestConversationStreamsSentences(t *testing.T) {
	var spoken []string
	fake := upstream{
		chat: respond(200, `{"choices":[{"message":{"content":"フゴー。前の試合は負けちゃったよ。次はリーズ戦だね！絶対に勝ってほしいな。"}}]}`),
		audioQuery: func(w http.ResponseWriter, r *http.Request) {
			spoken = append(spoken, r.URL.Query().Get("text"))
			_, _ = io.WriteString(w, `{}`)
		},
	}
	recorder := postConversation(newTestHandler(t, fake), "audio/wav", testWAV(16000, 0.3))
	events := readStream(t, recorder)
	if eventTypes(events) != "text,audio,audio,audio,done" || events[0].Parts != 3 {
		t.Fatalf("events = %s, parts = %d", eventTypes(events), events[0].Parts)
	}
	for i, event := range events[1:4] {
		if event.Index != i {
			t.Errorf("audio %d has index %d", i, event.Index)
		}
	}
	want := []string{"フゴー。前の試合は負けちゃったよ。", "次はリーズ戦だね！", "絶対に勝ってほしいな。"}
	if strings.Join(spoken, "|") != strings.Join(want, "|") {
		t.Fatalf("spoken = %q", spoken)
	}
}

func TestConversationStreamReportsLaterFailure(t *testing.T) {
	calls := 0
	fake := upstream{
		chat: respond(200, `{"choices":[{"message":{"content":"前の試合は負けちゃったよ。次はリーズ戦だね。"}}]}`),
		synthesis: func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			if calls++; calls > 1 {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(testWAV(1600, 0.3))
		},
	}
	recorder := postConversation(newTestHandler(t, fake), "audio/wav", testWAV(16000, 0.3))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	events := readStream(t, recorder)
	if eventTypes(events) != "text,audio,error" || events[2].Error["code"] != "tts_failed" {
		t.Fatalf("events = %s, last = %+v", eventTypes(events), events[len(events)-1])
	}
}

func TestSplitSentences(t *testing.T) {
	for text, want := range map[string][]string{
		"フゴー、元気だよ！": {"フゴー、元気だよ！"},
		"フゴー。前の試合は負けちゃったよ。次はリーズ戦だね！楽しみだな。": {"フゴー。前の試合は負けちゃったよ。", "次はリーズ戦だね！楽しみだな。"},
		"長い文だけど句点がないまま終わる":                 {"長い文だけど句点がないまま終わる"},
		"前の試合は負けちゃったよ。フゴー":                 {"前の試合は負けちゃったよ。フゴー"},
		"Is it good? Yes!":               {"Is it good? Yes!"},
		"Is it really good? Yes, it is!": {"Is it really good?", "Yes, it is!"},
	} {
		if got := splitSentences(text); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("splitSentences(%q) = %q, want %q", text, got, want)
		}
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
		Character:   DefaultCharacter(),
	}, nil)
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

func TestConversationUsesCharacter(t *testing.T) {
	var asrPrompt, systemPrompt, speaker, synthesisSpeaker string
	character := DefaultCharacter()
	character.ASRPrompt = "モモ"
	character.SystemPrompt = "あなたはモモです。"
	character.Speaker = 1
	character.SpeechReplacements = []Replacement{{"Momo", "モモ"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		asrPrompt = r.FormValue("prompt")
		_, _ = io.WriteString(w, `{"text":"こんにちは"}`)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []chatMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		systemPrompt = request.Messages[0].Content
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Momo だよ"}}]}`)
	})
	mux.HandleFunc("/audio_query", func(w http.ResponseWriter, r *http.Request) {
		speaker = r.URL.Query().Get("speaker")
		if text := r.URL.Query().Get("text"); text != "モモ だよ" {
			t.Errorf("speech text = %q", text)
		}
		_, _ = io.WriteString(w, `{}`)
	})
	mux.HandleFunc("/synthesis", func(w http.ResponseWriter, r *http.Request) {
		synthesisSpeaker = r.URL.Query().Get("speaker")
		_, _ = w.Write(testWAV(1600, 0.3))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	handler := NewHandler(Config{LemonadeURL: server.URL, VoicevoxURL: server.URL, Deadline: time.Second, Character: character}, nil)
	recorder := postConversation(handler, "audio/wav", testWAV(16000, 0.3))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	if asrPrompt != "モモ" || systemPrompt != "あなたはモモです。" || speaker != "1" || synthesisSpeaker != "1" {
		t.Fatalf("asr=%q system=%q speaker=%q/%q", asrPrompt, systemPrompt, speaker, synthesisSpeaker)
	}
}

type staticFacts string

func (f staticFacts) Facts(time.Time) string { return string(f) }

func TestConversationAppendsFacts(t *testing.T) {
	var systemPrompt string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/audio/transcriptions", respond(200, `{"text":"アーセナル勝った？"}`))
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []chatMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		systemPrompt = request.Messages[0].Content
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"勝ったフゴー"}}]}`)
	})
	mux.HandleFunc("/audio_query", respond(200, `{}`))
	mux.HandleFunc("/synthesis", respond(200, string(testWAV(1600, 0.3))))
	server := httptest.NewServer(mux)
	defer server.Close()
	character := DefaultCharacter()
	handler := NewHandler(Config{LemonadeURL: server.URL, VoicevoxURL: server.URL, Deadline: time.Second, Character: character}, staticFacts("【試合情報】テスト"))
	if recorder := postConversation(handler, "audio/wav", testWAV(16000, 0.3)); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	if systemPrompt != character.SystemPrompt+"\n\n【試合情報】テスト" {
		t.Fatalf("system prompt = %q", systemPrompt)
	}
}

func encodeHistory(t *testing.T, turns any) string {
	t.Helper()
	raw, err := json.Marshal(turns)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func TestConversationSendsHistory(t *testing.T) {
	var messages []chatMessage
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/audio/transcriptions", respond(200, `{"text":"誰が決めたの？"}`))
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []chatMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		messages = request.Messages
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"クーニャだよ"}}]}`)
	})
	mux.HandleFunc("/audio_query", respond(200, `{}`))
	mux.HandleFunc("/synthesis", respond(200, string(testWAV(1600, 0.3))))
	server := httptest.NewServer(mux)
	defer server.Close()
	handler := NewHandler(Config{LemonadeURL: server.URL, VoicevoxURL: server.URL, Deadline: time.Second, Character: DefaultCharacter()}, nil)

	request := httptest.NewRequest(http.MethodPost, "/api/conversation", bytes.NewReader(testWAV(16000, 0.3)))
	request.Header.Set("Content-Type", "audio/wav")
	request.Header.Set(historyHeader, encodeHistory(t, []Turn{{"ユナイテッドどうだった？", "フラムと引き分けだよ"}}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var roles, contents []string
	for _, message := range messages {
		roles = append(roles, message.Role)
		contents = append(contents, message.Content)
	}
	if strings.Join(roles, ",") != "system,user,assistant,user" ||
		contents[1] != "ユナイテッドどうだった？" || contents[2] != "フラムと引き分けだよ" || contents[3] != "誰が決めたの？" {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestParseHistory(t *testing.T) {
	long := strings.Repeat("あ", maxHistoryRunes+1)
	turn := Turn{"こんにちは", "フゴー"}
	for name, test := range map[string]struct {
		value string
		ok    bool
		turns int
	}{
		"empty":           {"", true, 0},
		"three turns":     {encodeHistory(t, []Turn{turn, turn, turn}), true, 3},
		"four turns":      {encodeHistory(t, []Turn{turn, turn, turn, turn}), false, 0},
		"too long":        {encodeHistory(t, []Turn{{long, "x"}}), false, 0},
		"blank assistant": {encodeHistory(t, []Turn{{"x", " "}}), false, 0},
		"not base64":      {"@@@", false, 0},
		"not json":        {base64.RawURLEncoding.EncodeToString([]byte("nope")), false, 0},
	} {
		turns, err := parseHistory(test.value)
		if (err == nil) != test.ok || len(turns) != test.turns {
			t.Errorf("%s: turns=%d err=%v", name, len(turns), err)
		}
	}
}

func TestConversationRejectsInvalidHistory(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/conversation", bytes.NewReader(testWAV(16000, 0.3)))
	request.Header.Set("Content-Type", "audio/wav")
	request.Header.Set(historyHeader, "@@@")
	recorder := httptest.NewRecorder()
	newTestHandler(t, upstream{}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || errorCode(t, recorder) != "invalid_history" {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
}
