# Clippers Live Hub

A desktop auto-clipper for live gameplay and VODs. It watches a screen, a window,
a stream or a video file, finds the loud, busy moments, and exports ready-to-post
vertical clips with the subject framed and the audio cleaned up.

Everything runs locally. No accounts, no uploads, no paid services.

```
capture ──▶ rolling buffer ──▶ highlight detection ──▶ 9:16 reframe ──▶ clean audio ──▶ clip.mp4
```

## What it does

| Stage | Implementation |
| --- | --- |
| Capture | `ffmpeg` screen / window / stream / file input |
| Buffer | 2-second MPEG-TS segments in a rolling on-disk ring |
| Detection | Audio loudness vs. a running baseline, plus scene-change activity |
| Reframe | Face detection (pure-Go `pigo` cascade) with a smoothed, animated crop |
| Audio | High-pass, FFT denoise, EBU R128 loudness normalisation to -14 LUFS |
| Captions | Optional, via a local `whisper.cpp` binary |
| Export | H.264 1080x1920, AAC 48 kHz, `+faststart` |

## Requirements

- **ffmpeg** and **ffprobe** — either on `PATH`, or placed in a `bin/` folder
  next to the executable.
- Windows 10/11 for screen and window capture. Linux and macOS work for file
  and stream input (X11/avfoundation capture is wired up but less tested).
- Optional: a `whisper.cpp` binary and a model file, if you want burned-in
  captions.

## Run

Download `ClippersLiveHub.exe`, put `ffmpeg.exe` and `ffprobe.exe` in `bin/`
beside it, then double-click. The control panel opens at
<http://127.0.0.1:8765>.

1. Pick a source: **Screen**, **Window**, **Stream** or **Video**.
2. Press **Start clipping**.
3. Highlights appear in the Clips grid as they are exported.

### Source notes

- **Screen** — captures the primary display. On Windows add an audio device
  name (for example `Stereo Mix`) to capture system sound; without it the clip
  has no audio.
- **Window** — captures one window by its exact title.
- **Stream** — any `http(s)`, `rtmp`, `rtsp`, `srt` or `udp` URL that ffmpeg can
  read.
- **Video** — analyses a finished file start to finish, then exports every
  highlight it finds.

## How detection works

Loudness alone is a poor trigger: a stream that is loud the whole time would
never produce a clip. Instead the detector keeps a slow exponential baseline of
the stream's loudness and scores each moment by how far it rises above that
baseline. A 20 dB jump counts as a full-scale event. Scene changes add a bounded
bonus so visually dramatic but quiet moments still register.

A highlight opens when the score crosses the sensitivity threshold, follows the
peak, and closes once the score falls away for a second. The clip is cut from
`peak - pre-roll` to `peak + post-roll`, capped at the maximum length, with a
cooldown that stops one long moment becoming five overlapping clips.

If the source has no audio track, or is silent, detection falls back to visual
activity alone.

## Settings

| Setting | Meaning |
| --- | --- |
| Sensitivity | Score needed to open a highlight. Lower = more clips. |
| Pre-roll / Post-roll | Seconds kept before and after the peak. |
| Max length | Hard cap on clip duration. |
| Face-tracked crop | Follows the largest detected face; falls back to centre. |
| Clean audio | Denoise, high-pass and normalise loudness. |
| Burn captions | Requires `whisper.cpp`. |

## Build from source

Requires Go 1.27 or newer.

```bash
# Linux / macOS
./scripts/build-linux.sh

# Windows executable (cross-compiled, no cgo)
./scripts/build-windows.sh
```

Output lands in `dist/`.

Run the tests (the end-to-end tests need `ffmpeg` and are skipped without it):

```bash
go test ./... -count=1
```

## Command line

```
clipper [-port 8765] [-out DIR] [-config config.json] [-host 127.0.0.1] [-open=false]
```

The process serves the UI over HTTP on the loopback interface. It does not
expose anything to the network by default.

## Project layout

```
cmd/clipper          entry point and flags
internal/api         local HTTP + SSE control surface
internal/capture     ffmpeg segmenter and rolling ring buffer
internal/analyze     loudness/scene parsing and the highlight detector
internal/face        pure-Go face detection
internal/reframe     9:16 crop geometry, smoothing, sendcmd animation
internal/render      single-pass export with reframe, audio, captions
internal/caption     whisper.cpp integration
internal/pipeline    orchestration of live and file sessions
internal/webui       embedded control panel
```

## Limitations

- **Android is not implemented.** This build is the Windows/Linux desktop
  application. An APK needs a separate Android app using `MediaProjection` for
  capture, with the same detection and export logic behind it. That is a
  follow-up, not something this repository can produce.
- Face detection is a frontal-face cascade. It handles a facecam well; it does
  not track profiles or full-body subjects.
- One capture source per session. Gameplay plus facecam as a stacked layout is
  not built yet; the crop follows the face within a single source.
- Captions need `whisper.cpp` installed separately; they are skipped otherwise.

## Using this responsibly

Only capture footage you own or have permission to record. Downloading or
re-recording someone else's stream can break that platform's terms of service
and copyright. Clip your own gameplay, your own VODs, or content you are
licensed to use.

## License

MIT. See [LICENSE](LICENSE). Face detection uses
[pigo](https://github.com/esimov/pigo) (MIT).
