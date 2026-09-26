type Availability = "ready" | "model_unloaded" | "unavailable";
type Config = { deadlineMs: number; maxAudioBytes: number; name: string; credit: string };
type Status = { lemonade: Availability; voicevoxReady: boolean };
// NDJSON events from /api/conversation: "text" first, one "audio" per
// sentence, then "done" or "error".
type StreamEvent =
  | { type: "text"; transcript: string; displayText: string; speechText: string; parts: number }
  | { type: "audio"; index: number; audioWavBase64: string }
  | { type: "done" }
  | { type: "error"; error: { code: string } };
type PlayOutcome = "played" | "blocked" | "stopped";

const recordButton = document.querySelector<HTMLButtonElement>("#record")!;
const availability = document.querySelector<HTMLElement>("#availability")!;
const state = document.querySelector<HTMLElement>("#state")!;
const answer = document.querySelector<HTMLElement>("#answer")!;
const transcript = document.querySelector<HTMLElement>("#transcript")!;
const reply = document.querySelector<HTMLElement>("#reply")!;
const audio = document.querySelector<HTMLAudioElement>("#audio")!;
const title = document.querySelector<HTMLElement>("#title")!;
const replyLabel = document.querySelector<HTMLElement>("#reply-label")!;
const credit = document.querySelector<HTMLElement>("#credit")!;

let config: Config = { deadlineMs: 120_000, maxAudioBytes: 2 * 1024 * 1024, name: "ブタコ", credit: "VOICEVOX:ずんだもん" };
let currentStatus: Status = { lemonade: "unavailable", voicevoxReady: false };
let recorder: MediaRecorder | null = null;
let stream: MediaStream | null = null;
let chunks: Blob[] = [];
let startedAt = 0;
let busy = false;
let audioURL: string | null = null;
// Sentences play through this element; the visible #audio holds the whole
// reply for replay once every sentence has arrived.
const player = new Audio();
let playing = false;
let stopPlaying: (() => void) | null = null;
let stopRecording: ((reason: "manual" | "hidden") => void) | null = null;
// One context for the page, created on the first tap: iOS Safari keeps
// contexts created without a user gesture suspended.
let audioContext: AudioContext | null = null;

// Conversation mode: after a reply finishes playing, listen again without a tap.
const continuous = document.querySelector<HTMLInputElement>("#continuous")!;
let conversing = false;
let conversationTurns = 0;
let wakeLock: WakeLockSentinel | null = null;

// Recent turns stay in this page's memory only and are sent with each request.
type Turn = { user: string; assistant: string };
const historyTurns = 3;
const historyTTLms = 10 * 60_000;
const historyRunes = 500;
let turns: Turn[] = [];
let lastTurnAt = 0;

const idleMessage = "ボタンを押して話しかけてね。";
const waitingMessage = "準備ができるまで少し待ってね。";

const messages: Record<string, string> = {
  silence: "声が聞き取れませんでした。もう一度話してね。",
  invalid_audio: "録音を処理できませんでした。もう一度試してね。",
  audio_too_large: "録音が長すぎます。短く話してね。",
  lemonade_unavailable: "Lemonade 利用不可（GPU 別用途または起動中）。少し待ってもう一度試してね。",
  asr_failed: "音声認識に失敗しました。もう一度試してね。",
  llm_failed: "返事を作れませんでした。もう一度試してね。",
  tts_failed: "音声を作れませんでした。もう一度試してね。",
  response_too_large: "返事の音声が大きすぎました。もう一度試してね。",
  timeout: "時間がかかりすぎました。もう一度試してね。",
};

function setState(message: string): void { state.textContent = message; }

function applyCharacter(): void {
  document.title = title.textContent = `${config.name}とおしゃべり`;
  replyLabel.textContent = `${config.name}の返事`;
  credit.textContent = `音声: ${config.credit}`;
}

function clearAnswer(): void {
  audio.pause();
  audio.hidden = true;
  audio.removeAttribute("src");
  audio.load();
  if (audioURL) URL.revokeObjectURL(audioURL);
  audioURL = null;
  transcript.textContent = "";
  reply.textContent = "";
  answer.hidden = true;
}

function updateButton(): void {
  if (playing) {
    recordButton.disabled = false;
    recordButton.classList.remove("recording");
    recordButton.textContent = conversing ? "会話を終える" : "再生を止める";
    return;
  }
  if (recorder?.state === "recording") {
    recordButton.disabled = false;
    recordButton.textContent = "録音を止める";
    recordButton.classList.add("recording");
    return;
  }
  recordButton.classList.remove("recording");
  recordButton.textContent = busy ? "処理中…" : conversing ? "会話を終える" : "話しかける";
  recordButton.disabled = busy || currentStatus.lemonade === "unavailable" || !currentStatus.voicevoxReady;
}

async function pollStatus(): Promise<void> {
  if (busy || recorder?.state === "recording") return;
  let serverReachable = true;
  try {
    const response = await fetch("/api/status", { cache: "no-store" });
    if (!response.ok) throw new Error("status failed");
    currentStatus = await response.json() as Status;
  } catch {
    serverReachable = false;
    currentStatus = { lemonade: "unavailable", voicevoxReady: false };
  }
  if (!serverReachable) {
    availability.textContent = `${config.name}のサーバーに接続できません`;
  } else if (currentStatus.lemonade === "unavailable") {
    availability.textContent = "Lemonade 利用不可（GPU 別用途または起動中）";
  } else if (!currentStatus.voicevoxReady) {
    availability.textContent = "音声合成を準備中です";
  } else if (currentStatus.lemonade === "model_unloaded") {
    availability.textContent = "モデル未ロード。会話開始時に読み込みます";
  } else {
    availability.textContent = "会話できます";
  }
  updateButton();
  // A turn may have started while the status request was in flight.
  if (busy) return;
  if (recordButton.disabled) setState(waitingMessage);
  else if (state.textContent === waitingMessage) setState(idleMessage);
}

function encodeWAV(samples: Float32Array): Blob {
  const buffer = new ArrayBuffer(44 + samples.length * 2);
  const view = new DataView(buffer);
  const label = (offset: number, value: string): void => {
    for (let i = 0; i < value.length; i++) view.setUint8(offset + i, value.charCodeAt(i));
  };
  label(0, "RIFF"); view.setUint32(4, buffer.byteLength - 8, true);
  label(8, "WAVE"); label(12, "fmt "); view.setUint32(16, 16, true);
  view.setUint16(20, 1, true); view.setUint16(22, 1, true);
  view.setUint32(24, 16_000, true); view.setUint32(28, 32_000, true);
  view.setUint16(32, 2, true); view.setUint16(34, 16, true);
  label(36, "data"); view.setUint32(40, samples.length * 2, true);
  for (let i = 0; i < samples.length; i++) {
    const value = Math.max(-1, Math.min(1, samples[i]!));
    view.setInt16(44 + i * 2, value < 0 ? value * 32768 : value * 32767, true);
  }
  return new Blob([buffer], { type: "audio/wav" });
}

async function convertToWAV(recording: Blob): Promise<Blob> {
  const context = new AudioContext();
  try {
    const decoded = await context.decodeAudioData(await recording.arrayBuffer());
    const frames = Math.ceil(decoded.duration * 16_000);
    const offline = new OfflineAudioContext(1, frames, 16_000);
    const source = offline.createBufferSource();
    source.buffer = decoded;
    source.connect(offline.destination);
    source.start();
    const rendered = await offline.startRendering();
    const samples = rendered.getChannelData(0);
    let energy = 0;
    for (const sample of samples) energy += sample * sample;
    if (samples.length < 8_000 || Math.sqrt(energy / samples.length) < 0.003) {
      throw new Error("silence");
    }
    return encodeWAV(samples);
  } finally {
    await context.close();
  }
}

function decodeBase64WAV(value: string): Blob {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return new Blob([bytes], { type: "audio/wav" });
}

function recentHistory(): Turn[] {
  if (performance.now() - lastTurnAt > historyTTLms) turns = [];
  return turns;
}

function remember(user: string, assistant: string): void {
  const clip = (text: string): string => Array.from(text).slice(0, historyRunes).join("");
  turns = [...recentHistory(), { user: clip(user), assistant: clip(assistant) }].slice(-historyTurns);
  lastTurnAt = performance.now();
}

// base64url of UTF-8 JSON, the form the server's X-Butaco-History expects.
function encodeHistory(items: Turn[]): string {
  let binary = "";
  for (const byte of new TextEncoder().encode(JSON.stringify(items))) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

async function* readEvents(response: Response): AsyncGenerator<StreamEvent> {
  const reader = response.body!.getReader();
  const decoder = new TextDecoder();
  let buffered = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (value) buffered += decoder.decode(value, { stream: true });
    let newline: number;
    while ((newline = buffered.indexOf("\n")) >= 0) {
      const line = buffered.slice(0, newline).trim();
      buffered = buffered.slice(newline + 1);
      if (line) yield JSON.parse(line) as StreamEvent;
    }
    if (done) {
      if (buffered.trim()) yield JSON.parse(buffered) as StreamEvent;
      return;
    }
  }
}

// playPart plays one sentence and reports whether it finished, was stopped
// from the button, or could not start (autoplay refused).
async function playPart(blob: Blob): Promise<"ended" | "stopped" | "blocked"> {
  const url = URL.createObjectURL(blob);
  try {
    player.src = url;
    const finished = new Promise<"ended" | "stopped">(resolve => {
      player.onended = () => resolve("ended");
      stopPlaying = () => { player.pause(); resolve("stopped"); };
    });
    try {
      await player.play();
    } catch {
      return "blocked";
    }
    return await finished;
  } finally {
    player.onended = null;
    stopPlaying = null;
    URL.revokeObjectURL(url);
  }
}

// concatWAV joins the sentence WAVs (same format) into one file for replay.
function concatWAV(parts: ArrayBuffer[]): Blob {
  let format: Uint8Array | null = null;
  const samples: Uint8Array[] = [];
  for (const part of parts) {
    const view = new DataView(part);
    for (let offset = 12; offset + 8 <= part.byteLength;) {
      const id = String.fromCharCode(...new Uint8Array(part, offset, 4));
      const size = view.getUint32(offset + 4, true);
      const length = Math.min(size, part.byteLength - offset - 8);
      if (id === "fmt ") format ??= new Uint8Array(part, offset + 8, 16);
      if (id === "data") samples.push(new Uint8Array(part, offset + 8, length));
      offset += 8 + size + (size % 2);
    }
  }
  const dataSize = samples.reduce((sum, chunk) => sum + chunk.byteLength, 0);
  const out = new Uint8Array(44 + dataSize);
  const view = new DataView(out.buffer);
  const label = (offset: number, value: string): void => {
    for (let i = 0; i < value.length; i++) out[offset + i] = value.charCodeAt(i);
  };
  label(0, "RIFF"); view.setUint32(4, out.byteLength - 8, true);
  label(8, "WAVE"); label(12, "fmt "); view.setUint32(16, 16, true);
  if (format) out.set(format, 20);
  label(36, "data"); view.setUint32(40, dataSize, true);
  let offset = 44;
  for (const chunk of samples) { out.set(chunk, offset); offset += chunk.byteLength; }
  return new Blob([out], { type: "audio/wav" });
}

// sendWAV plays each sentence as it arrives and reports how playback ended.
async function sendWAV(wav: Blob): Promise<PlayOutcome> {
  const controller = new AbortController();
  const timeout = window.setTimeout(() => controller.abort(), config.deadlineMs);
  const parts: ArrayBuffer[] = [];
  try {
    const headers: Record<string, string> = { "Content-Type": "audio/wav" };
    const recent = recentHistory();
    if (recent.length) headers["X-Butaco-History"] = encodeHistory(recent);
    const response = await fetch("/api/conversation", {
      method: "POST",
      headers,
      body: wav,
      signal: controller.signal,
    });
    if (!response.ok) {
      const failure = await response.json().catch(() => null) as { error?: { code?: string } } | null;
      throw new Error(failure?.error?.code || "network_error");
    }
    let outcome: PlayOutcome = "played";
    let failure: string | null = null;
    for await (const event of readEvents(response)) {
      if (event.type === "text") {
        remember(event.transcript, event.displayText);
        transcript.textContent = event.transcript;
        reply.textContent = event.displayText;
        answer.hidden = false;
        setState("返事を再生しています…");
      } else if (event.type === "audio") {
        const blob = decodeBase64WAV(event.audioWavBase64);
        parts.push(await blob.arrayBuffer());
        if (outcome !== "played") continue; // keep collecting for replay
        playing = true;
        updateButton();
        const result = await playPart(blob);
        if (result === "blocked") outcome = "blocked";
        if (result === "stopped") {
          outcome = "stopped";
          controller.abort();
          break;
        }
      } else if (event.type === "error") {
        failure = event.error.code;
        break;
      } else {
        break;
      }
    }
    if (failure) throw new Error(failure);
    return outcome;
  } finally {
    window.clearTimeout(timeout);
    playing = false;
    if (parts.length) {
      audioURL = URL.createObjectURL(concatWAV(parts));
      audio.src = audioURL;
      audio.hidden = false;
    }
  }
}

// Speech endpointing: sample the input level and stop after speech followed by
// silence. The noise floor is measured first and then tracks the room.
const endpoint = {
  intervalMs: 50,
  calibrationMs: 250,
  minThreshold: 0.01,
  thresholdRatio: 3,
  speechMs: 200,
  silenceMs: 1_200,
  noSpeechMs: 8_000,
};

type StopReason = "manual" | "silence" | "no_speech" | "limit" | "hidden";

function watchSpeech(context: AudioContext, input: MediaStream, onEnd: (reason: StopReason) => void): () => void {
  const analyser = context.createAnalyser();
  analyser.fftSize = 1024;
  context.createMediaStreamSource(input).connect(analyser);
  const samples = new Float32Array(analyser.fftSize);
  const started = performance.now();
  let floor = 0;
  let calibration = 0;
  let voicedMs = 0;
  let spoke = false;
  let lastVoice = started;
  const timer = window.setInterval(() => {
    analyser.getFloatTimeDomainData(samples);
    let energy = 0;
    for (const sample of samples) energy += sample * sample;
    const level = Math.sqrt(energy / samples.length);
    const now = performance.now();
    if (now - started < endpoint.calibrationMs) {
      calibration++;
      floor += (level - floor) / calibration;
      return;
    }
    const threshold = Math.max(endpoint.minThreshold, floor * endpoint.thresholdRatio);
    if (level > threshold) {
      voicedMs += endpoint.intervalMs;
      lastVoice = now;
      if (voicedMs >= endpoint.speechMs) spoke = true;
      // Rise slowly so steady background noise is absorbed but speech is not.
      floor += (level - floor) * 0.001;
    } else {
      floor += (level - floor) * (level < floor ? 0.1 : 0.01);
    }
    if (spoke && now - lastVoice >= endpoint.silenceMs) finish("silence");
    else if (!spoke && now - started >= endpoint.noSpeechMs) finish("no_speech");
  }, endpoint.intervalMs);
  let done = false;
  function finish(reason: StopReason): void {
    if (done) return;
    done = true;
    window.clearInterval(timer);
    onEnd(reason);
  }
  return () => { done = true; window.clearInterval(timer); };
}

function endConversation(message?: string): void {
  conversing = false;
  void wakeLock?.release().catch(() => undefined);
  wakeLock = null;
  if (message) setState(message);
  updateButton();
}

// handleRecording returns whether conversation mode should listen again.
async function handleRecording(chunksToProcess: Blob[], mimeType: string, reason: StopReason): Promise<boolean> {
  busy = true;
  updateButton();
  let outcome: PlayOutcome | null = null;
  try {
    if (reason === "hidden") {
      endConversation("画面を離れたので会話を終わったよ。");
      return false;
    }
    if (reason === "no_speech" && conversing && conversationTurns > 1) {
      endConversation("話しかけがなかったので会話を終わったよ。ボタンを押すとまた話せるよ。");
      return false;
    }
    if (reason === "no_speech" || performance.now() - startedAt < 500) throw new Error("silence");
    setState("録音を変換しています…");
    const wav = await convertToWAV(new Blob(chunksToProcess, { type: mimeType }));
    if (wav.size > config.maxAudioBytes) throw new Error("audio_too_large");
    setState(`${config.name}が考えています…`);
    outcome = await sendWAV(wav);
    if (outcome === "blocked") endConversation("返事ができました。再生ボタンから聞いてね。");
    else if (outcome === "stopped") endConversation(conversing ? "会話を終わったよ。ボタンを押すとまた話せるよ。" : "再生を止めたよ。");
    else if (!conversing) setState("返事ができました。もう一度聞くときは再生ボタンを押してね。");
  } catch (error) {
    const code = error instanceof Error ? error.message : "network_error";
    endConversation();
    setState(messages[code] || (error instanceof DOMException && error.name === "AbortError"
      ? messages.timeout! : "通信に失敗しました。もう一度試してね。"));
  } finally {
    busy = false;
    await pollStatus();
    updateButton();
  }
  return conversing && outcome === "played";
}

// Listen again after a reply has finished playing, unless the page was left.
async function continueConversation(): Promise<void> {
  if (!conversing) return;
  if (document.visibilityState !== "visible" || recordButton.disabled) {
    endConversation("会話を終わったよ。ボタンを押すとまた話せるよ。");
    return;
  }
  await startRecording();
}

async function startRecording(): Promise<void> {
  busy = true;
  if (conversing) conversationTurns++;
  clearAnswer();
  setState("マイクを準備しています…");
  updateButton();
  const context = audioContext ??= new AudioContext();
  let stopWatching = (): void => {};
  try {
    stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    const mimeType = ["audio/mp4", "audio/webm"].find(type => MediaRecorder.isTypeSupported(type));
    if (!mimeType) throw new Error("unsupported_audio");
    await context.resume();
    chunks = [];
    const current = new MediaRecorder(stream, { mimeType });
    recorder = current;
    let reason: StopReason = "manual";
    const stop = (why: StopReason): void => {
      if (current.state !== "recording") return;
      reason = why;
      current.stop();
    };
    stopRecording = stop;
    // 60 seconds of 16 kHz mono PCM16 stays under the 2 MiB request limit.
    const limit = window.setTimeout(() => stop("limit"), 60_000);
    stopWatching = watchSpeech(context, stream, stop);
    current.ondataavailable = event => { if (event.data.size) chunks.push(event.data); };
    current.onstop = () => {
      window.clearTimeout(limit);
      stopWatching();
      // Close the mic before playback: iOS plays quietly while it is open.
      stream?.getTracks().forEach(track => track.stop());
      stream = null;
      recorder = null;
      stopRecording = null;
      void handleRecording(chunks, mimeType, reason).then(next => {
        if (next) void continueConversation();
      });
    };
    current.start();
    startedAt = performance.now();
    busy = false;
    setState(conversing
      ? "どうぞ、話してね。（話し終えると自動で送るよ）"
      : "録音中です。話し終えると自動で送るよ。（ボタンでも止められます）");
    updateButton();
  } catch (error) {
    stopWatching();
    stream?.getTracks().forEach(track => track.stop());
    stream = null;
    busy = false;
    endConversation();
    if (error instanceof DOMException && error.name === "NotAllowedError") {
      setState("マイクが許可されていません。Safari の設定を確認してね。");
    } else {
      setState("マイクを開始できませんでした。もう一度試してね。");
    }
    updateButton();
  }
}

recordButton.addEventListener("click", () => {
  if (playing) {
    stopPlaying?.();
    return;
  }
  if (recorder?.state === "recording") {
    stopRecording?.("manual");
    return;
  }
  if (busy) return;
  if (conversing) {
    audio.pause();
    endConversation("会話を終わったよ。ボタンを押すとまた話せるよ。");
    return;
  }
  if (continuous.checked) {
    conversing = true;
    conversationTurns = 0;
  }
  // startRecording creates the AudioContext synchronously, inside this tap.
  void startRecording();
  if (conversing && "wakeLock" in navigator) {
    navigator.wakeLock.request("screen").then(lock => { wakeLock = lock; }, () => undefined);
  }
});

continuous.addEventListener("change", () => {
  // Turning the switch off lets the current turn finish without continuing.
  if (!continuous.checked && conversing) {
    conversing = false;
    void wakeLock?.release().catch(() => undefined);
    wakeLock = null;
    updateButton();
  }
});

// Leaving the page ends conversation mode; iOS keeps recording while hidden.
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState !== "hidden") return;
  if (recorder?.state === "recording") stopRecording?.("hidden");
  else if (conversing) endConversation("画面を離れたので会話を終わったよ。");
});

void (async () => {
  try {
    const response = await fetch("/api/config", { cache: "no-store" });
    if (response.ok) config = await response.json() as Config;
  } catch { /* Server defaults match the UI defaults. */ }
  applyCharacter();
  setState(idleMessage);
  await pollStatus();
  window.setInterval(() => void pollStatus(), 10_000);
})();
