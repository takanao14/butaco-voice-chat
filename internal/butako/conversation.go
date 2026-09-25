package butako

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const systemPrompt = "あなたはブタのぬいぐるみ『ブタコ』です。イギリス出身で、マンチェスター・ユナイテッドが好きです。日本語で自然に会話してください。『フゴフゴ』『フゴー』は自然な範囲で使ってください。音声で聞きやすい短い返答を原則2〜3文で作ってください。Markdown、絵文字、英字表記は使わないでください。"
const asrPrompt = "ブタコ、フゴー、マンチェスター・ユナイテッド"

type conversationResult struct {
	Transcript     string   `json:"transcript"`
	DisplayText    string   `json:"displayText"`
	SpeechText     string   `json:"speechText"`
	AudioWAVBase64 string   `json:"audioWavBase64"`
	Sources        []source `json:"sources,omitempty"`
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
}

func (s *service) converse(ctx context.Context, wav []byte, webSearch bool) (conversationResult, error) {
	transcript, err := s.transcribe(ctx, wav)
	if err != nil {
		return conversationResult{}, err
	}
	if emptyOrHallucinated(transcript) {
		return conversationResult{}, &stageError{"silence", http.StatusUnprocessableEntity}
	}
	var display string
	var sources []source
	if webSearch || requestsSearch(transcript) {
		display, sources = s.searchReply(ctx, transcript)
	} else {
		display, err = s.reply(ctx, transcript)
		if err != nil {
			return conversationResult{}, err
		}
	}
	speech := speechText(display)
	if speech == "" {
		return conversationResult{}, &stageError{"llm_failed", http.StatusBadGateway}
	}
	audio, err := s.synthesize(ctx, speech)
	if err != nil {
		return conversationResult{}, err
	}
	return conversationResult{
		Transcript:     transcript,
		DisplayText:    display,
		SpeechText:     speech,
		AudioWAVBase64: base64.StdEncoding.EncodeToString(audio),
		Sources:        sources,
	}, nil
}

func (s *service) transcribe(ctx context.Context, wav []byte) (string, error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for name, value := range map[string]string{
		"model":    s.config.ASRModel,
		"language": "ja",
		"prompt":   asrPrompt,
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

func (s *service) reply(ctx context.Context, transcript string) (string, error) {
	return s.complete(ctx, systemPrompt, transcript, 160)
}

func (s *service) complete(ctx context.Context, instruction, input string, maxTokens int) (string, error) {
	request := struct {
		Model           string        `json:"model"`
		Messages        []chatMessage `json:"messages"`
		ReasoningEffort string        `json:"reasoning_effort"`
		MaxTokens       int           `json:"max_tokens"`
		Stream          bool          `json:"stream"`
	}{Model: s.config.LLMModel, ReasoningEffort: "none", MaxTokens: maxTokens}
	request.Messages = []chatMessage{{"system", instruction}, {"user", input}}
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
	query := url.Values{"text": {text}, "speaker": {strconv.Itoa(s.config.Speaker)}}
	raw, err := s.post(ctx, s.config.VoicevoxURL, "/audio_query?"+query.Encode(), "application/json", nil, "tts_failed")
	if err != nil {
		return nil, err
	}
	if !json.Valid(raw) {
		return nil, &stageError{"tts_failed", http.StatusBadGateway}
	}
	path := "/synthesis?speaker=" + strconv.Itoa(s.config.Speaker)
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
