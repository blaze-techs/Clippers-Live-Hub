// On-device clip engine: highlight detection, 9:16 reframe render and save.
// Runs in the Android WebView and in any modern browser; no server, no wasm.

const MIME_CANDIDATES = [
  'video/mp4;codecs=avc1.42E01E,mp4a.40.2',
  'video/mp4',
  'video/webm;codecs=vp9,opus',
  'video/webm;codecs=vp8,opus',
  'video/webm',
];

export function pickMimeType() {
  if (typeof MediaRecorder === 'undefined') return '';
  return MIME_CANDIDATES.find((t) => MediaRecorder.isTypeSupported(t)) || '';
}

export function extensionFor(mime) {
  return mime.includes('mp4') ? 'mp4' : 'webm';
}

// Some WebViews accept an mp4 mimeType and then hand back a WebM blob, so
// trust the bytes rather than the label when naming the file.
export async function sniffExtension(blob) {
  try {
    const head = new Uint8Array(await blob.slice(0, 12).arrayBuffer());
    if (head[0] === 0x1a && head[1] === 0x45 && head[2] === 0xdf && head[3] === 0xa3) return 'webm';
    if (String.fromCharCode(head[4], head[5], head[6], head[7]) === 'ftyp') return 'mp4';
  } catch (err) {
    // fall through to the declared type
  }
  return extensionFor(blob.type || '');
}

function clamp(v, lo, hi) {
  return Math.min(hi, Math.max(lo, v));
}

function newVideoEl(url) {
  const v = document.createElement('video');
  v.src = url;
  v.muted = true;
  v.playsInline = true;
  v.preload = 'auto';
  v.crossOrigin = 'anonymous';
  return v;
}

function waitFor(el, event, timeoutMs = 10000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      el.removeEventListener(event, onEvent);
      reject(new Error(`timed out waiting for ${event}`));
    }, timeoutMs);
    function onEvent() {
      clearTimeout(timer);
      el.removeEventListener(event, onEvent);
      resolve();
    }
    el.addEventListener(event, onEvent, { once: true });
  });
}

async function seek(video, time) {
  if (Math.abs(video.currentTime - time) < 0.01) return;
  const done = waitFor(video, 'seeked');
  video.currentTime = time;
  await done;
}

// ---------------------------------------------------------------- highlights

function rmsEnvelope(buffer, windowSec) {
  const rate = buffer.sampleRate;
  const size = Math.max(1, Math.floor(rate * windowSec));
  const channels = [];
  for (let c = 0; c < buffer.numberOfChannels; c++) channels.push(buffer.getChannelData(c));
  const out = [];
  for (let i = 0; i < buffer.length; i += size) {
    let sum = 0;
    let n = 0;
    for (let j = i; j < Math.min(i + size, buffer.length); j++) {
      let s = 0;
      for (let c = 0; c < channels.length; c++) s += channels[c][j];
      s /= channels.length;
      sum += s * s;
      n++;
    }
    out.push({ t: i / rate, value: n ? Math.sqrt(sum / n) : 0 });
  }
  return out;
}

function normalise(values) {
  const sorted = [...values].sort((a, b) => a - b);
  const p95 = sorted[Math.floor(sorted.length * 0.95)] || 1;
  return values.map((v) => clamp(v / (p95 || 1), 0, 1));
}

// Frame difference on a 64x36 luma thumbnail; cheap and good enough to spot cuts.
async function sceneScores(url, duration, sampleCount) {
  const video = newVideoEl(url);
  const canvas = document.createElement('canvas');
  canvas.width = 64;
  canvas.height = 36;
  const ctx = canvas.getContext('2d', { willReadFrequently: true });
  const step = duration / sampleCount;
  const scores = [];
  let previous = null;
  try {
    await waitFor(video, 'loadeddata');
    for (let i = 0; i < sampleCount; i++) {
      const t = Math.min(duration - 0.05, i * step);
      await seek(video, t);
      ctx.drawImage(video, 0, 0, canvas.width, canvas.height);
      const data = ctx.getImageData(0, 0, canvas.width, canvas.height).data;
      const luma = new Float32Array(canvas.width * canvas.height);
      for (let p = 0, k = 0; p < data.length; p += 4, k++) {
        luma[k] = 0.299 * data[p] + 0.587 * data[p + 1] + 0.114 * data[p + 2];
      }
      if (previous) {
        let diff = 0;
        for (let k = 0; k < luma.length; k++) diff += Math.abs(luma[k] - previous[k]);
        scores.push({ t, value: diff / luma.length });
      } else {
        scores.push({ t, value: 0 });
      }
      previous = luma;
    }
  } finally {
    video.src = '';
  }
  return scores;
}

/**
 * Rank candidate moments from audio energy and visual cuts.
 * Returns clips of `clipLength` seconds centred on each peak.
 */
export async function detectHighlights(file, { windowSec = 0.5, clipLength = 20, maxClips = 6, sceneSamples = 240 } = {}) {
  const url = URL.createObjectURL(file);
  const AudioCtx = window.AudioContext || window.webkitAudioContext;
  let duration = 0;
  let audio = [];
  let scene = [];

  try {
    const probe = newVideoEl(url);
    await waitFor(probe, 'loadedmetadata');
    duration = probe.duration || 0;
    probe.src = '';

    const ctx = new AudioCtx();
    try {
      const buffer = await ctx.decodeAudioData(await file.arrayBuffer());
      const envelope = rmsEnvelope(buffer, windowSec);
      const norm = normalise(envelope.map((e) => e.value));
      audio = envelope.map((e, i) => ({ t: e.t, value: norm[i] }));
    } catch (err) {
      audio = [];
    } finally {
      ctx.close();
    }

    if (duration > 0) {
      const samples = clamp(Math.round(duration), 2, sceneSamples);
      scene = normalise((await sceneScores(url, duration, samples)).map((s) => s.value));
    }
  } finally {
    URL.revokeObjectURL(url);
  }

  const at = (series, t) => {
    if (!series.length) return 0;
    const idx = clamp(Math.round(t / windowSec), 0, series.length - 1);
    return series[idx] ?? 0;
  };

  const peaks = [];
  for (let i = 1; i < audio.length - 1; i++) {
    const prev = audio[i - 1].value;
    const next = audio[i + 1].value;
    if (audio[i].value >= prev && audio[i].value >= next && audio[i].value > 0.35) {
      peaks.push({ t: audio[i].t, audio: audio[i].value, scene: at(scene, audio[i].t) });
    }
  }
  if (!peaks.length && audio.length) {
    const loudest = audio.reduce((a, b) => (b.value > a.value ? b : a));
    peaks.push({ t: loudest.t, audio: loudest.value, scene: at(scene, loudest.t) });
  }

  const rank = (p) => p.audio * 0.65 + p.scene * 0.35;
  peaks.sort((a, b) => rank(b) - rank(a));

  // A clip can never be longer than the source, and two moments closer than
  // this would produce overlapping clips of the same footage.
  const target = Math.min(clipLength, duration || clipLength);
  const minGap = Math.max(3, target * 0.6);
  const chosen = [];
  for (const p of peaks) {
    if (chosen.length >= maxClips) break;
    if (chosen.some((c) => Math.abs(c.t - p.t) < minGap)) continue;
    chosen.push(p);
  }

  const clips = [];
  for (const p of chosen) {
    const start = clamp(p.t - target / 2, 0, Math.max(0, (duration || target) - target));
    const end = Math.min(duration || p.t + target / 2, start + target);
    if (clips.some((c) => Math.abs(c.start - start) < 0.75 && Math.abs(c.end - end) < 0.75)) continue;
    clips.push({
      start,
      end,
      center: p.t,
      score: Number(rank(p).toFixed(3)),
      audio: Number(p.audio.toFixed(3)),
      scene: Number(p.scene.toFixed(3)),
    });
  }
  return clips.sort((a, b) => b.score - a.score);
}

// -------------------------------------------------------------------- render

function drawCaption(ctx, text, width, height, style) {
  if (!text) return;
  const fontSize = Math.round(height * 0.042);
  ctx.font = `800 ${fontSize}px system-ui, -apple-system, "Segoe UI", Roboto, sans-serif`;
  ctx.textAlign = 'center';
  ctx.textBaseline = 'middle';

  const maxWidth = width * 0.86;
  const words = String(text).toUpperCase().split(/\s+/);
  const lines = [];
  let line = '';
  for (const word of words) {
    const candidate = line ? `${line} ${word}` : word;
    if (ctx.measureText(candidate).width > maxWidth && line) {
      lines.push(line);
      line = word;
    } else {
      line = candidate;
    }
  }
  if (line) lines.push(line);

  const lineHeight = fontSize * 1.18;
  const bottom = height * 0.88;
  const top = bottom - (lines.length - 1) * lineHeight;

  lines.forEach((text, i) => {
    const y = top + i * lineHeight;
    if (style === 'boxed') {
      const w = ctx.measureText(text).width;
      ctx.fillStyle = 'rgba(0,0,0,0.72)';
      ctx.fillRect((width - w) / 2 - fontSize * 0.4, y - lineHeight / 2, w + fontSize * 0.8, lineHeight);
      ctx.fillStyle = '#ffffff';
    } else if (style === 'minimal') {
      ctx.fillStyle = '#ffffff';
    } else {
      ctx.lineWidth = Math.max(4, fontSize * 0.14);
      ctx.strokeStyle = 'rgba(0,0,0,0.85)';
      ctx.lineJoin = 'round';
      ctx.strokeText(text, width / 2, y);
      ctx.fillStyle = '#ffffff';
    }
    ctx.fillText(text, width / 2, y);
  });
}

function drawFrame(ctx, video, opts) {
  const { width, height, zoom = 1, x = 0.5, y = 0.5, caption = '', captionStyle = 'bold', reaction = null } = opts;
  ctx.fillStyle = '#000';
  ctx.fillRect(0, 0, width, height);
  if (!video.videoWidth) return;

  // Cover-crop: take a zoom-scaled window of the source, then fit it to the
  // canvas the same way the desktop ffmpeg graph does.
  const srcW = video.videoWidth;
  const srcH = video.videoHeight;
  const cw = srcW / zoom;
  const ch = srcH / zoom;
  const cx = clamp(x, 0, 1) * (srcW - cw);
  const cy = clamp(y, 0, 1) * (srcH - ch);
  const scale = Math.max(width / cw, height / ch);
  const dw = cw * scale;
  const dh = ch * scale;
  ctx.drawImage(video, cx, cy, cw, ch, (width - dw) / 2, (height - dh) / 2, dw, dh);

  if (reaction && reaction.videoWidth) {
    const pw = Math.round(width * 0.34);
    const ph = Math.round((pw * reaction.videoHeight) / reaction.videoWidth);
    const px = width - pw - Math.round(width * 0.05);
    const py = height - ph - Math.round(height * 0.22);
    ctx.save();
    ctx.shadowColor = 'rgba(0,0,0,0.55)';
    ctx.shadowBlur = 24;
    ctx.drawImage(reaction, px, py, pw, ph);
    ctx.restore();
  }

  drawCaption(ctx, caption, width, height, captionStyle);
}

function audioGraph(videoEl, enabled) {
  const stream = videoEl.captureStream ? videoEl.captureStream() : null;
  if (!stream) return null;
  const tracks = stream.getAudioTracks();
  if (!tracks.length) return null;
  if (!enabled) return new MediaStream(tracks);

  const AudioCtx = window.AudioContext || window.webkitAudioContext;
  const ctx = new AudioCtx();
  const source = ctx.createMediaStreamSource(stream);
  const highpass = ctx.createBiquadFilter();
  highpass.type = 'highpass';
  highpass.frequency.value = 85;
  const compressor = ctx.createDynamicsCompressor();
  compressor.threshold.value = -18;
  compressor.knee.value = 6;
  compressor.ratio.value = 3;
  compressor.attack.value = 0.005;
  compressor.release.value = 0.2;
  const destination = ctx.createMediaStreamDestination();
  source.connect(highpass).connect(compressor).connect(destination);
  const out = new MediaStream(destination.stream.getAudioTracks());
  out._ctx = ctx;
  return out;
}

/**
 * Render [start, end) of the video to a vertical clip. Recording is real time:
 * a 20 s clip takes about 20 s on device.
 */
export async function exportClip(options) {
  const {
    videoEl,
    start = 0,
    end,
    width = 1080,
    height = 1920,
    zoom = 1,
    x = 0.5,
    y = 0.5,
    caption = '',
    captionStyle = 'bold',
    reactionEl = null,
    cleanAudio = true,
    fps = 30,
    bitrate = 8_000_000,
    onProgress = () => {},
  } = options;

  const mimeType = pickMimeType();
  if (!mimeType) throw new Error('This WebView cannot record video (MediaRecorder missing).');

  const from = Math.max(0, start);
  const to = Math.min(end ?? videoEl.duration, videoEl.duration);
  if (!(to > from)) throw new Error('Clip range is empty.');

  const canvas = document.createElement('canvas');
  canvas.width = width;
  canvas.height = height;
  const ctx = canvas.getContext('2d', { alpha: false });
  const drawOpts = { width, height, zoom, x, y, caption, captionStyle, reaction: reactionEl };

  videoEl.muted = false;
  videoEl.volume = 1;
  await seek(videoEl, from);
  drawFrame(ctx, videoEl, drawOpts);

  const stream = canvas.captureStream(fps);
  const audio = audioGraph(videoEl, cleanAudio);
  const audioTracks = audio ? audio.getAudioTracks() : [];
  audioTracks.forEach((t) => stream.addTrack(t));

  const recorder = new MediaRecorder(stream, {
    mimeType,
    videoBitsPerSecond: bitrate,
    audioBitsPerSecond: 128_000,
  });
  const chunks = [];
  recorder.ondataavailable = (e) => {
    if (e.data && e.data.size) chunks.push(e.data);
  };

  const finished = new Promise((resolve, reject) => {
    recorder.onstop = () => resolve(new Blob(chunks, { type: mimeType }));
    recorder.onerror = (e) => reject(e.error || new Error('Recorder failed'));
  });

  let stopped = false;
  const stop = () => {
    if (stopped) return;
    stopped = true;
    if (recorder.state !== 'inactive') recorder.stop();
    videoEl.pause();
    audioTracks.forEach((t) => t.stop());
    if (audio && audio._ctx) audio._ctx.close();
  };

  recorder.start(250);
  await videoEl.play();

  await new Promise((resolve) => {
    const tick = () => {
      if (stopped) return resolve();
      drawFrame(ctx, videoEl, drawOpts);
      const done = videoEl.currentTime >= to || videoEl.ended;
      onProgress({ phase: 'render', progress: clamp((videoEl.currentTime - from) / (to - from), 0, 1) });
      if (done) return resolve();
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  });

  stop();
  const blob = await finished;
  onProgress({ phase: 'render', progress: 1 });
  if (!blob.size) throw new Error('Recorder produced an empty file.');
  return { blob, mimeType, extension: await sniffExtension(blob), width, height, duration: to - from };
}

// ---------------------------------------------------------------------- save

function blobToBase64(blob) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onloadend = () => resolve(String(reader.result).split(',')[1] || '');
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(blob);
  });
}

export async function saveClip(blob, filename) {
  const capacitor = window.Capacitor;
  if (capacitor?.Plugins?.Filesystem) {
    const { Filesystem, Directory } = await import('@capacitor/filesystem');
    const data = await blobToBase64(blob);
    const written = await Filesystem.writeFile({
      path: `Clippers/${filename}`,
      data,
      directory: Directory.Documents,
      recursive: true,
    });
    if (capacitor.Plugins.Share) {
      const { Share } = await import('@capacitor/share');
      await Share.share({ title: filename, url: written.uri, dialogTitle: 'Save your clip' });
    }
    return { saved: true, uri: written.uri };
  }

  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 30000);
  return { saved: true, uri: url };
}
