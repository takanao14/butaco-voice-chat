// Probe page for Phase 6 B (conversation mode). It checks iPhone Safari
// behaviour that the main UI would depend on and records the results as text.
// It sends nothing to the server.

const logView = document.querySelector<HTMLElement>("#log")!;
const lines: string[] = [];
const started = performance.now();

function log(message: string): void {
  const seconds = ((performance.now() - started) / 1000).toFixed(1).padStart(6);
  lines.push(`${seconds}s ${message}`);
  logView.textContent = lines.join("\n");
  logView.scrollTop = logView.scrollHeight;
}

const sleep = (ms: number): Promise<void> => new Promise(resolve => setTimeout(resolve, ms));
const errorText = (error: unknown): string => error instanceof Error ? `${error.name}: ${error.message}` : String(error);

// A 2.5-second chime, generated locally so the page needs no server audio.
function chimeURL(): string {
  const rate = 24_000;
  const notes = [523.25, 659.25, 783.99, 1046.5];
  const samples = new Int16Array(rate * 2.5);
  for (let i = 0; i < samples.length; i++) {
    const t = i / rate;
    const note = notes[Math.min(notes.length - 1, Math.floor(t / 0.6))]!;
    const envelope = Math.exp(-3 * (t % 0.6));
    samples[i] = Math.round(Math.sin(2 * Math.PI * note * t) * envelope * 0.5 * 32767);
  }
  const buffer = new ArrayBuffer(44 + samples.byteLength);
  const view = new DataView(buffer);
  const text = (offset: number, value: string): void => {
    for (let i = 0; i < value.length; i++) view.setUint8(offset + i, value.charCodeAt(i));
  };
  text(0, "RIFF"); view.setUint32(4, buffer.byteLength - 8, true);
  text(8, "WAVE"); text(12, "fmt "); view.setUint32(16, 16, true);
  view.setUint16(20, 1, true); view.setUint16(22, 1, true);
  view.setUint32(24, rate, true); view.setUint32(28, rate * 2, true);
  view.setUint16(32, 2, true); view.setUint16(34, 16, true);
  text(36, "data"); view.setUint32(40, samples.byteLength, true);
  new Int16Array(buffer, 44).set(samples);
  return URL.createObjectURL(new Blob([buffer], { type: "audio/wav" }));
}

const chime = chimeURL();
const player = new Audio();
player.preload = "auto";

async function play(label: string): Promise<boolean> {
  player.src = chime;
  try {
    await player.play();
    log(`${label}: 再生開始 OK`);
  } catch (error) {
    log(`${label}: 再生開始 失敗 (${errorText(error)})`);
    return false;
  }
  await new Promise<void>(resolve => player.addEventListener("ended", () => resolve(), { once: true }));
  log(`${label}: 再生終了`);
  return true;
}

async function openMic(label: string): Promise<MediaStream | null> {
  const before = performance.now();
  try {
    const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    const settings = stream.getAudioTracks()[0]?.getSettings() ?? {};
    log(`${label}: マイク OK (${Math.round(performance.now() - before)}ms, echoCancellation=${String(settings.echoCancellation)})`);
    return stream;
  } catch (error) {
    log(`${label}: マイク 失敗 (${errorText(error)})`);
    return null;
  }
}

function closeMic(stream: MediaStream | null): void {
  stream?.getTracks().forEach(track => track.stop());
}

// Peak input level over a period, to see whether playback leaks into the mic.
function meter(context: AudioContext, stream: MediaStream): (() => number) & { stop: () => void } {
  const analyser = context.createAnalyser();
  analyser.fftSize = 1024;
  context.createMediaStreamSource(stream).connect(analyser);
  const samples = new Float32Array(analyser.fftSize);
  let peak = 0;
  const timer = window.setInterval(() => {
    analyser.getFloatTimeDomainData(samples);
    let energy = 0;
    for (const sample of samples) energy += sample * sample;
    peak = Math.max(peak, Math.sqrt(energy / samples.length));
  }, 50);
  const read = (): number => { const value = peak; peak = 0; return value; };
  read.stop = (): void => window.clearInterval(timer);
  return read;
}

const buttons = [...document.querySelectorAll<HTMLButtonElement>("button:not([data-note]):not(#copy):not(#lock-stop)")];

function exclusive(task: () => Promise<void>): () => void {
  return () => {
    buttons.forEach(button => { button.disabled = true; });
    void task().catch(error => log(`例外: ${errorText(error)}`)).finally(() => {
      buttons.forEach(button => { button.disabled = false; });
    });
  };
}

document.querySelector("#play-closed")!.addEventListener("click", exclusive(async () => {
  log("--- 1a マイクを使わずに再生");
  await play("1a");
}));

document.querySelector("#play-open")!.addEventListener("click", exclusive(async () => {
  log("--- 1b マイクを開いたまま再生");
  const context = new AudioContext();
  const stream = await openMic("1b");
  if (!stream) return;
  await context.resume();
  const peak = meter(context, stream);
  await sleep(1000);
  log(`1b: 再生前の入力レベル ${peak().toFixed(4)}`);
  await play("1b");
  log(`1b: 再生中の入力レベル ${peak().toFixed(4)}（大きいほど再生音がマイクに入っている）`);
  peak.stop();
  closeMic(stream);
  await context.close();
}));

document.querySelector("#play-reopened")!.addEventListener("click", exclusive(async () => {
  log("--- 1c マイクを開いて閉じてから再生");
  closeMic(await openMic("1c"));
  await sleep(500);
  await play("1c");
}));

document.querySelector("#permission")!.addEventListener("click", exclusive(async () => {
  log("--- 2 マイクを 3 回開き直す");
  for (let i = 1; i <= 3; i++) {
    closeMic(await openMic(`2-${i}`));
    await sleep(500);
  }
}));

async function conversationLoop(keepOpen: boolean): Promise<void> {
  const name = keepOpen ? "3b" : "3a";
  log(`--- ${name} 会話の繰り返し（${keepOpen ? "マイクを開いたまま" : "毎回開き直す"}）`);
  const context = new AudioContext();
  let stream: MediaStream | null = null;
  for (let turn = 1; turn <= 3; turn++) {
    const label = `${name}-${turn}`;
    if (!stream) stream = await openMic(label);
    if (!stream) break;
    await context.resume();
    log(`${label}: AudioContext ${context.state}`);
    const peak = meter(context, stream);
    const recorder = new MediaRecorder(stream);
    let bytes = 0;
    recorder.ondataavailable = event => { bytes += event.data.size; };
    const stopped = new Promise<void>(resolve => { recorder.onstop = () => resolve(); });
    recorder.start();
    await sleep(2000);
    recorder.stop();
    await stopped;
    log(`${label}: 録音 ${bytes} bytes、入力レベル ${peak().toFixed(4)}`);
    if (!keepOpen) { closeMic(stream); stream = null; }
    await sleep(2000);
    const played = await play(label);
    if (keepOpen) log(`${label}: 再生中の入力レベル ${peak().toFixed(4)}`);
    peak.stop();
    if (!played) break;
  }
  closeMic(stream);
  await context.close();
  log(`${name}: 終了`);
}

document.querySelector("#loop-reopen")!.addEventListener("click", exclusive(() => conversationLoop(false)));
document.querySelector("#loop-open")!.addEventListener("click", exclusive(() => conversationLoop(true)));

const lockStop = document.querySelector<HTMLButtonElement>("#lock-stop")!;
let lockCleanup: (() => Promise<void>) | null = null;

document.querySelector("#lock-start")!.addEventListener("click", exclusive(async () => {
  log("--- 4 画面ロックとアプリ切り替え");
  let wakeLock: WakeLockSentinel | null = null;
  if ("wakeLock" in navigator) {
    try {
      wakeLock = await navigator.wakeLock.request("screen");
      log("4: Wake Lock OK");
      wakeLock.addEventListener("release", () => log("4: Wake Lock 解除"));
    } catch (error) {
      log(`4: Wake Lock 失敗 (${errorText(error)})`);
    }
  } else {
    log("4: Wake Lock 非対応");
  }
  const stream = await openMic("4");
  if (!stream) return;
  const track = stream.getAudioTracks()[0]!;
  track.addEventListener("mute", () => log("4: マイク mute"));
  track.addEventListener("unmute", () => log("4: マイク unmute"));
  track.addEventListener("ended", () => log("4: マイク ended"));
  const recorder = new MediaRecorder(stream);
  let bytes = 0;
  recorder.ondataavailable = event => { bytes += event.data.size; };
  recorder.onerror = () => log("4: MediaRecorder error");
  recorder.start(1000);
  const visibility = (): void => log(`4: 画面 ${document.visibilityState}、録音 ${recorder.state}、track ${track.readyState}${track.muted ? "（mute）" : ""}`);
  document.addEventListener("visibilitychange", visibility);
  lockStop.disabled = false;
  lockCleanup = async () => {
    document.removeEventListener("visibilitychange", visibility);
    log(`4: 終了時 録音 ${recorder.state}、track ${track.readyState}、受信 ${bytes} bytes`);
    if (recorder.state !== "inactive") recorder.stop();
    closeMic(stream);
    await wakeLock?.release().catch(() => undefined);
    lockStop.disabled = true;
  };
}));

lockStop.addEventListener("click", () => { void lockCleanup?.(); lockCleanup = null; });

document.querySelectorAll<HTMLButtonElement>("button[data-note]").forEach(button => {
  button.addEventListener("click", () => log(`メモ: ${button.dataset.note}`));
});

document.querySelector("#copy")!.addEventListener("click", () => {
  const text = `UA: ${navigator.userAgent}\n${lines.join("\n")}`;
  navigator.clipboard.writeText(text).then(() => log("記録をコピーしました"), error => {
    log(`コピー失敗 (${errorText(error)})。記録を選択したので長押しでコピーしてください`);
    const range = document.createRange();
    range.selectNodeContents(logView);
    window.getSelection()?.removeAllRanges();
    window.getSelection()?.addRange(range);
  });
});

log(`UA: ${navigator.userAgent}`);
