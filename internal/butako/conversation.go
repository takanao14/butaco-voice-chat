package butako

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// replyText is one turn before speech synthesis.
type replyText struct {
	Transcript  string
	DisplayText string
	SpeechText  string
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type stageError struct {
	code   string
	status int
}

func (e *stageError) Error() string { return e.code }

type service struct {
	config Config
	client *http.Client
	speech *strings.Replacer
	facts  Facts
}

// Facts supplies reference information appended to the system prompt.
type Facts interface {
	Facts(now time.Time) string
}

// Turn is one earlier exchange the browser keeps and sends back.
type Turn struct {
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}

// prepare runs speech recognition and the reply; synthesis is streamed
// sentence by sentence by the handler.
func (s *service) prepare(ctx context.Context, wav []byte, history []Turn) (replyText, error) {
	transcript, err := s.transcribe(ctx, wav)
	if err != nil {
		return replyText{}, err
	}
	if emptyOrHallucinated(transcript, s.config.Character.Hallucinations) {
		return replyText{}, &stageError{"silence", http.StatusUnprocessableEntity}
	}
	display, err := s.reply(ctx, history, transcript)
	if err != nil {
		return replyText{}, err
	}
	speech := speechText(s.speech, display)
	if speech == "" {
		return replyText{}, &stageError{"llm_failed", http.StatusBadGateway}
	}
	return replyText{Transcript: transcript, DisplayText: display, SpeechText: speech}, nil
}

func (s *service) transcribe(ctx context.Context, wav []byte) (string, error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for name, value := range map[string]string{
		"model":    s.config.ASRModel,
		"language": "ja",
		"prompt":   s.config.Character.ASRPrompt,
	} {
		if err := form.WriteField(name, value); err != nil {
			return "", &stageError{"asr_failed", http.StatusBadGateway}
		}
	}
	file, err := form.CreateFormFile("file", "recording.wav")
	if err != nil {
		return "", &stageError{"asr_failed", http.StatusBadGateway}
	}
	if _, err := file.Write(wav); err != nil {
		return "", &stageError{"asr_failed", http.StatusBadGateway}
	}
	if err := form.Close(); err != nil {
		return "", &stageError{"asr_failed", http.StatusBadGateway}
	}
	raw, err := s.post(ctx, s.config.LemonadeURL, "/v1/audio/transcriptions", form.FormDataContentType(), body.Bytes(), "asr_failed")
	if err != nil {
		return "", err
	}
	var response struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return "", &stageError{"asr_failed", http.StatusBadGateway}
	}
	return strings.TrimSpace(response.Text), nil
}

func (s *service) reply(ctx context.Context, history []Turn, transcript string) (string, error) {
	request := struct {
		Model           string        `json:"model"`
		Messages        []chatMessage `json:"messages"`
		ReasoningEffort string        `json:"reasoning_effort"`
		MaxTokens       int           `json:"max_tokens"`
		Stream          bool          `json:"stream"`
	}{Model: s.config.LLMModel, ReasoningEffort: "none", MaxTokens: 160}
	system := s.config.Character.SystemPrompt
	if s.facts != nil {
		system += "\n\n" + s.facts.Facts(time.Now())
	}
	request.Messages = []chatMessage{{"system", system}}
	for _, turn := range history {
		request.Messages = append(request.Messages, chatMessage{"user", turn.User}, chatMessage{"assistant", turn.Assistant})
	}
	request.Messages = append(request.Messages, chatMessage{"user", transcript})
	body, err := json.Marshal(request)
	if err != nil {
		return "", &stageError{"llm_failed", http.StatusBadGateway}
	}
	raw, err := s.post(ctx, s.config.LemonadeURL, "/v1/chat/completions", "application/json", body, "llm_failed")
	if err != nil {
		return "", err
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &response) != nil || len(response.Choices) == 0 {
		return "", &stageError{"llm_failed", http.StatusBadGateway}
	}
	text := strings.TrimSpace(response.Choices[0].Message.Content)
	if text == "" {
		return "", &stageError{"llm_failed", http.StatusBadGateway}
	}
	return text, nil
}

func (s *service) synthesize(ctx context.Context, text string) ([]byte, error) {
	speaker := strconv.Itoa(s.config.Character.Speaker)
	query := url.Values{"text": {text}, "speaker": {speaker}}
	raw, err := s.post(ctx, s.config.VoicevoxURL, "/audio_query?"+query.Encode(), "application/json", nil, "tts_failed")
	if err != nil {
		return nil, err
	}
	if !json.Valid(raw) {
		return nil, &stageError{"tts_failed", http.StatusBadGateway}
	}
	path := "/synthesis?speaker=" + speaker
	audio, err := s.post(ctx, s.config.VoicevoxURL, path, "application/json", raw, "tts_failed")
	if err != nil {
		return nil, err
	}
	if len(audio) < 12 || string(audio[:4]) != "RIFF" || string(audio[8:12]) != "WAVE" {
		return nil, &stageError{"tts_failed", http.StatusBadGateway}
	}
	return audio, nil
}

func (s *service) post(ctx context.Context, base, path, contentType string, body []byte, failure string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, &stageError{failure, http.StatusBadGateway}
	}
	request.Header.Set("Content-Type", contentType)
	response, err := s.client.Do(request)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, &stageError{"timeout", http.StatusGatewayTimeout}
		}
		if base == s.config.LemonadeURL {
			return nil, &stageError{"lemonade_unavailable", http.StatusServiceUnavailable}
		}
		return nil, &stageError{failure, http.StatusBadGateway}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &stageError{failure, http.StatusBadGateway}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return nil, &stageError{failure, http.StatusBadGateway}
	}
	return raw, nil
}

func (s *service) get(ctx context.Context, base, path string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream HTTP %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, 1<<20))
}
