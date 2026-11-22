# AGENTS.md

Repository notes for future sessions.

## What this is

`Clippers Live Hub` is a Go desktop auto-clipper: capture -> rolling segment
buffer -> highlight detection -> 9:16 face-tracked reframe -> clean audio ->
MP4 export. It serves a local web UI on 127.0.0.1 and ships as a single
Windows/Linux binary. ffmpeg does all media work; Go orchestrates.

## Commands

```bash
go build ./...                 # compile everything
go vet ./...                   # static checks
go test ./... -count=1         # unit + end-to-end tests (needs ffmpeg)
./scripts/build-windows.sh     # dist/ClippersLiveHub.exe
./scripts/build-linux.sh       # dist/clippers-live-hub
```

The end-to-end tests (`internal/pipeline/e2e_test.go`) generate their own
synthetic fixtures with ffmpeg and skip when ffmpeg is missing.

## Clip Studio (timeline editor)

Added after the initial capture tool. Two rendering paths now exist:

- `internal/render` — single-shot reframe used by the live pipeline.
- `internal/compose` — renders a whole `project.Project` timeline into one
  ffmpeg filter graph (trim → crop → cover-scale → overlay → captions → mix).

Rules that keep it correct:

- **Preview and export must share the crop maths.** `compose.cropFilter` does
  crop → `scale=...:force_original_aspect_ratio=increase` → `crop` (cover,
  centred). `applyCrop()` in `index.html` reproduces exactly that. Changing one
  without the other silently breaks WYSIWYG.
- **Keyframed crops render.** `piecewise()` builds a nested `if(lt(t,...))`
  expression so pans are interpolated inside ffmpeg. Commas inside the
  expression must be escaped (`\,`) or they split the filter chain.
- **Nil slices become `[]` in `Project.Validate()`.** Otherwise `clips: null`
  reaches JS and `for (const c of tr.clips)` throws.
- **Undo is snapshot based** (`project.Editor`), 100 steps, stored server side;
  the UI only sends ops.
- **Writes are atomic**: `project.json.tmp` + fsync + rename, with recovery in
  `Store.Load`.

Tests worth running before touching the renderer:
`go test ./internal/compose/ ./internal/project/ ./internal/api/ -count=1`.
The compose tests assert a left-half crop keeps red, a right-half crop keeps
blue, and that burning captions changes the lower third — real pixels, not
mocks.

## Architecture decisions worth keeping

- **One ffmpeg process per live session.** The segmenter and the analysis
  filters run in the same process (`internal/capture/engine.go`) so segment
  timestamps and sample timestamps share a clock. Two processes drift and the
  clips land in the wrong place.
- **Never use `select='gt(scene,N)'` in the analysis graph.** It drops every
  frame on a static video, which makes ffmpeg exit with "at least one of its
  streams received no packets". Use `gte(scene,0)` and let the detector decide.
- **Loudness is scored against a running baseline, not an absolute level.** A
  constantly loud stream must still produce clips.
- **Crop animation uses `sendcmd`**, not a giant nested `if()` expression.
  `crop` accepts runtime `x`/`y` commands.
- **Face detection is `pigo`** (pure Go, cgo-free) so the Windows binary can be
  cross-compiled from Linux. It embeds its own cascade.
- **pigo v1.5.0 declares `go 1.27`**, so the module directive cannot be lowered.

## Gotchas

- Crop width/height must be even for yuv420p.
- `internal/webui/assets` is embedded with `go:embed`; new UI files must live
  under that directory.
- The UI is a single `index.html` with inline CSS/JS so it works offline.
