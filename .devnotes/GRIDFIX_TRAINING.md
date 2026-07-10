# gridfix training backend (`internal/gridfix/train`)

Fine-tunes the Beat This! model on the DJ's own verified grids. Flow: user marks
tracks "grid verified" (`gridfix.VerifiedStore`, BPM+marker captured at mark
time) → `train.BuildManifest` → `Trainer.Start` spawns `trainer.py` (one-shot
child, sysexec Hide + BelowNormalPriority + job object; ctx cancel kills the
tree) → JSONL events stream back → fine-tuned `.ckpt` lands in outDir →
selectable as active model via `Engine.Checkpoint` (`GRIDFIX_CHECKPOINT`).
`ListCheckpoints(dir)` enumerates them, newest first.

## Manifest (`train-manifest.json`)

```json
{"audio": [{"path": "...", "bpm": 174.0, "startMs": 120.5}],
 "outDir": "...", "baseCheckpoint": "final0|<path>.ckpt",
 "epochs": 3, "lr": 5e-5, "valSplit": 0.1, "seed": 1}
```

`BuildManifest` skips entries whose audio no longer exists / bpm<=0
(returns skipped count), errors under 2 usable tracks (train+val minimum).

## Events (trainer.py stdout, one JSON/line)

```
{"ev":"start","tracks":N,"device":"cpu|cuda"}
{"ev":"epoch","n":i,"loss":x,"valFBeat":x,"valFDown":x}
{"ev":"done","checkpoint":"<path>","report":{"beforeFBeat":x,"afterFBeat":x,"improved":bool}}
{"ev":"error","msg":"..."}
```

stderr = human log (prep progress, tracebacks) → `Trainer.OnLog`.

## Training loop (trainer.py)

- Annotations from the verified constant grid: beat = `startMs + k*60/bpm`
  within audio; downbeat every 4th beat counted from the marker (both
  directions, `k%4==0`).
- Audio decode identical to runner.py (soundfile → ffmpeg f32le fallback),
  soxr → 22050, `beat_this.preprocessing.LogMelSpect` (fps 50).
- Loss: `beat_this.model.loss.ShiftTolerantBCELoss` (beat + downbeat) — the
  paper's ±3-frame shift tolerance lives in the loss's max-pool, so NO manual
  target widening. AdamW, decay 0.01 only on ndim≥2 params (mirrors
  PLBeatThis), grad-clip 1.0, 1500-frame chunks, batch 2.
- Split train/val **by track** never by chunk (chunk splits leak the piece).
- Val metric: full-piece `split_predict_aggregate` + `Postprocessor("minimal")`
  → own compact F-measure (±70 ms, trim first 5 s — mir_eval semantics,
  greedy sorted matching).
- Checkpoint saved as Lightning-shaped dict `{"state_dict":{"model.*"},
  "hyper_parameters":...}` → `beat_this.inference.load_model(path)` accepts it
  directly; loadability is verified in-process before `done` is emitted.

## Deps decision

**No new pip deps.** The engine env (`EnvManager.Install`: torch + beat-this
1.1.0 + soundfile) already ships everything the loop touches (torchaudio,
einops, soxr, rotary-embedding come with beat-this). `beat_this.model.pl_module`
would need pytorch_lightning + mir_eval — deliberately not imported.
`InstallTrainingDeps` therefore only verifies imports.

## Caveat: easy tracks teach nothing

Tracks Traktor already grids perfectly are near-zero-gradient (the model
already nails them) — a training set of easy four-to-the-floor gives a
feel-good run with no capability gain. Prioritize hand-gridded
MANUAL_GRIDDING_PREP-style tracks (soft transients, swing, sparse intros) when
marking verified. The per-track val split + before/after F-beat report is the
acceptance gate: only adopt a checkpoint whose `afterFBeat >= beforeFBeat`
(`improved`), and keep `final0` selectable as fallback.
