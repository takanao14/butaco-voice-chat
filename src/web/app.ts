type Availability = "ready" | "model_unloaded" | "unavailable";
type Config = { deadlineMs: number; maxAudioBytes: number };
type Status = { lemonade: Availability; voicevoxReady: boolean };
type Conversation = {
  transcript: string;
  displayText: string;
  speechText: string;
  audioWavBase64: string;
};

const recordButton = document.querySelector<HTMLButtonElement>("#record")!;
const availability = document.querySelector<HTMLElement>("#availability")!;
const state = document.querySelector<HTMLElement>("#state")!;
const answer = document.querySelector<HTMLElement>("#answer")!;
const transcript = document.querySelector<HTMLElement>("#transcript")!;
const reply = document.querySelector<HTMLElement>("#reply")!;
const audio = document.querySelector<HTMLAudioElement>("#audio")!;

let config: Config = { deadlineMs: 120_000, maxAudioBytes: 2 * 1024 * 1024 };
let currentStatus: Status = { lemonade: "unavailable", voicevoxReady: false };
let recorder: MediaRecorder | null = null;
let stream: MediaStream | null = null;
let chunks: Blob[] = [];
let startedAt = 0;
let busy = false;
let audioURL: string | null = null;

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

function clearAnswer(): void {
  audio.pause();
  audio.removeAttribute("src");
  audio.load();
  if (audioURL) URL.revokeObjectURL(audioURL);
  audioURL = null;
  transcript.textContent = "";
  reply.textContent = "";
  answer.hidden = true;
}

function updateButton(): void {
  if (recorder?.state === "recording") {
    recordButton.disabled = false;
    recordButton.textContent = "録音を止める";
    recordButton.classList.add("recording");
    return;
  }
  recordButton.classList.remove("recording");
  recordButton.textContent = busy ? "処理中…" : "話しかける";
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
    availability.textContent = "ブタコのサーバーに接続できません";
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

async function sendWAV(wav: Blob): Promise<void> {
  const controller = new AbortController();
  const timeout = window.setTimeout(() => controller.abort(), config.deadlineMs);
  try {
    const response = await fetch("/api/conversation", {
      method: "POST",
      headers: { "Content-Type": "audio/wav" },
      body: wav,
      signal: controller.signal,
    });
    if (!response.ok) {
      const failure = await response.json().catch(() => null) as { error?: { code?: string } } | null;
      throw new Error(failure?.error?.code || "network_error");
    }
    const result = await response.json() as Conversation;
    transcript.textContent = result.transcript;
    reply.textContent = result.displayText;
    audioURL = URL.createObjectURL(decodeBase64WAV(result.audioWavBase64));
    audio.src = audioURL;
    answer.hidden = false;
    setState("返事ができました。再生ボタンから聞いてね。");
    try { await audio.play(); } catch { /* Safari may require another tap. */ }
  } finally {
    window.clearTimeout(timeout);
  }
}

async function handleRecording(chunksToProcess: Blob[], mimeType: string): Promise<void> {
  busy = true;
  updateButton();
  try {
    if (performance.now() - startedAt < 500) throw new Error("silence");
    setState("録音を変換しています…");
    const wav = await convertToWAV(new Blob(chunksToProcess, { type: mimeType }));
    if (wav.size > config.maxAudioBytes) throw new Error("audio_too_large");
    setState("ブタコが考えています…");
    await sendWAV(wav);
  } catch (error) {
    const code = error instanceof Error ? error.message : "network_error";
    setState(messages[code] || (error instanceof DOMException && error.name === "AbortError"
      ? messages.timeout! : "通信に失敗しました。もう一度試してね。"));
  } finally {
    busy = false;
    await pollStatus();
    updateButton();
  }
}

async function startRecording(): Promise<void> {
  busy = true;
  clearAnswer();
  updateButton();
  try {
    stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    const mimeType = ["audio/mp4", "audio/webm"].find(type => MediaRecorder.isTypeSupported(type));
    if (!mimeType) throw new Error("unsupported_audio");
    chunks = [];
    recorder = new MediaRecorder(stream, { mimeType });
    recorder.ondataavailable = event => { if (event.data.size) chunks.push(event.data); };
    recorder.onstop = () => {
      stream?.getTracks().forEach(track => track.stop());
      stream = null;
      recorder = null;
      void handleRecording(chunks, mimeType);
    };
    recorder.start();
    startedAt = performance.now();
    busy = false;
    setState("録音中です。話し終えたらボタンを押してね。");
    updateButton();
  } catch (error) {
    stream?.getTracks().forEach(track => track.stop());
    stream = null;
    busy = false;
    if (error instanceof DOMException && error.name === "NotAllowedError") {
      setState("マイクが許可されていません。Safari の設定を確認してね。");
    } else {
      setState("マイクを開始できませんでした。もう一度試してね。");
    }
    updateButton();
  }
}

recordButton.addEventListener("click", () => {
  if (recorder?.state === "recording") {
    recorder.stop();
    return;
  }
  if (!busy) void startRecording();
});

void (async () => {
  try {
    const response = await fetch("/api/config", { cache: "no-store" });
    if (response.ok) config = await response.json() as Config;
  } catch { /* Server defaults match the UI defaults. */ }
  setState(idleMessage);
  await pollStatus();
  window.setInterval(() => void pollStatus(), 10_000);
})();
