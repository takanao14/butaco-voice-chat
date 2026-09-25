# Butako voice chat

Go API and TypeScript Safari UI for one local push-to-talk conversation. The
application sends WAV audio to Lemonade, uses Gemma for a short Japanese reply,
and synthesizes it with VOICEVOX. It keeps audio and conversation text in memory
for the request and does not write request bodies or prompts to application logs.

## Local development

Requires Go 1.22+, Node.js 22+, and Docker on an ARM64 Mac. VOICEVOX's official
CPU image is pinned by version and digest in `compose.yaml`.

```sh
docker compose up -d voicevox
npm ci
npm run build
go run ./cmd/butako
```

Open `http://localhost:8080` on the development machine. iPhone Safari needs
an HTTPS deployment to use the microphone. The application defaults to the
existing Lemonade Gateway at `https://lemonade.prd.butaco.net`; set
`LEMONADE_URL` for another endpoint. `VOICEVOX_URL` defaults to
`http://127.0.0.1:50021`.

`GET /api/status` distinguishes a disconnected Lemonade server from unloaded
models. `POST /api/conversation` accepts 16 kHz mono PCM16 WAV (up to 2 MiB)
and returns transcript, display text, speech text, and Base64 WAV in one JSON
response. `CONVERSATION_DEADLINE_SECONDS` sets the shared API and browser
deadline (120 seconds by default). The browser obtains it from `/api/config`.

`CHARACTER_FILE` points to an optional JSON file that overrides the built-in
Butako character. Omitted fields keep their defaults, unknown fields stop the
server at startup, and the file is read once at startup:

```json
{
  "name": "ブタコ",
  "credit": "VOICEVOX:ずんだもん",
  "systemPrompt": "あなたはブタのぬいぐるみ『ブタコ』です。…",
  "asrPrompt": "ブタコ、フゴー、マンチェスター・ユナイテッド",
  "speaker": 3,
  "speechReplacements": [{"from": "Manchester United", "to": "マンチェスター・ユナイテッド"}],
  "hallucinations": ["ご視聴ありがとうございました"]
}
```

`name` and `credit` appear in the UI; keep `credit` in VOICEVOX's
`VOICEVOX:<speaker name>` form for the selected `speaker`.
`speechReplacements` rewrite the reply before synthesis, and transcripts
matching `hallucinations` are treated as silence. An optional `football` object
(`{"teams": [{"espnId": "359", "name": "アーセナル", "fan": "ユーザー"}]}`)
enables match facts: a background fetcher reads the teams' Premier League,
Champions League, League Cup, and FA Cup matches and the Premier League table
from ESPN's unofficial API every 30 minutes, and each conversation appends a
Japanese summary to the system prompt. Times, results, and table order are
computed in `internal/football`, not by the model. If no refresh has
succeeded within three hours, the summary says match data is unavailable.
`BUTAKO_ESPN_LIVE=1 go test -run TestESPNLive -v ./internal/football` checks
the live API shape. The default values are in
`internal/butako/character.go`.

VOICEVOX logs `audio_query` text by default. The development container's
stdout and stderr are discarded so those access lines are not retained. The
production VOICEVOX deployment must apply the same privacy constraint.
