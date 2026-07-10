# rave-mate gridfix trainer: one-shot fine-tune child. argv[1] = manifest.json:
#   {"audio":[{"path":...,"bpm":...,"startMs":...}],"outDir":...,
#    "baseCheckpoint":"final0"|path,"epochs":N,"lr":...,"valSplit":0.1,"seed":...}
# Emits one JSON event per line on stdout:
#   {"ev":"start","tracks":N,"device":...}
#   {"ev":"epoch","n":i,"loss":...,"valFBeat":...,"valFDown":...}
#   {"ev":"done","checkpoint":...,"report":{"beforeFBeat":...,"afterFBeat":...,"improved":...}}
#   {"ev":"error","msg":...}
# Targets derive from each track's DJ-verified constant grid (beat = startMs +
# k*60/bpm within the audio, downbeat every 4th counted from the marker).
# Compact custom loop on beat_this primitives only (LogMelSpect,
# ShiftTolerantBCELoss - the shift tolerance lives in the loss, no manual target
# widening - Postprocessor, split_predict_aggregate). Deliberately avoids
# beat_this.model.pl_module: it drags in pytorch_lightning + mir_eval, neither
# of which the engine env installs. Saved checkpoint mirrors the Lightning dict
# shape ({"state_dict":{"model.*"},"hyper_parameters":...}) so
# beat_this.inference.load_model() accepts it as checkpoint_path.
import json
import math
import os
import random
import subprocess
import sys
import time
from dataclasses import dataclass
from pathlib import Path

SOUNDFILE_EXTS = {".mp3", ".flac", ".wav", ".aif", ".aiff", ".ogg"}
SR = 22050
FPS = 50
CHUNK = 1500  # frames = 30 s, the model's training length
BORDER = 6  # 2 * loss tolerance; edges discarded in full-piece predict
BATCH = 2
EVAL_TRIM = 5.0  # beat_this eval convention: ignore beats in the first 5 s
EVAL_WINDOW = 0.07  # standard +-70 ms F-measure tolerance

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(errors="replace")


def emit(**kw):
    print(json.dumps(kw), flush=True)


def log(msg):
    print(msg, file=sys.stderr, flush=True)


def load_audio_any(path: Path):
    """Return (mono float32 signal, samplerate). ffmpeg fallback for m4a/aac etc.
    Byte-identical behavior with runner.py so train and inference audio match."""
    import numpy as np
    if path.suffix.lower() in SOUNDFILE_EXTS:
        try:
            import soundfile as sf
            signal, sr = sf.read(str(path), dtype="float32", always_2d=True)
            return signal.mean(axis=1), sr
        except Exception:
            pass  # fall through to ffmpeg
    sr = 22050
    ffmpeg = os.environ.get("GRIDFIX_FFMPEG", "ffmpeg")
    proc = subprocess.run(
        [ffmpeg, "-v", "error", "-i", str(path), "-ac", "1", "-ar", str(sr),
         "-f", "f32le", "-"],
        capture_output=True, check=True)
    return np.frombuffer(proc.stdout, dtype=np.float32), sr


def grid_times(bpm: float, start_ms: float, dur: float):
    """Beat + downbeat times (s) of the constant grid across [0, dur]."""
    period = 60.0 / bpm
    start = start_ms / 1000.0
    k0 = math.ceil((0.0 - start) / period - 1e-9)
    k1 = math.floor((dur - start) / period + 1e-9)
    beats, downs = [], []
    for k in range(k0, k1 + 1):
        t = start + k * period
        beats.append(t)
        if k % 4 == 0:  # bar phase anchored on the marker, both directions
            downs.append(t)
    return beats, downs


@dataclass
class Track:
    spect: object  # (T,128) log-mel, cpu
    tb: object  # (T,) framewise beat target
    td: object  # (T,) framewise downbeat target
    beat_times: list
    down_times: list


def prepare(item, spect_fn) -> Track:
    import soxr
    import torch
    signal, sr = load_audio_any(Path(item["path"]))
    if getattr(signal, "ndim", 1) == 2:
        signal = signal.mean(axis=1)
    if sr != SR:
        signal = soxr.resample(signal, in_rate=sr, out_rate=SR)
    dur = len(signal) / SR
    with torch.no_grad():
        spect = spect_fn(torch.as_tensor(signal, dtype=torch.float32))
    T = spect.shape[0]
    beats, downs = grid_times(float(item["bpm"]), float(item["startMs"]), dur)
    tb = torch.zeros(T)
    td = torch.zeros(T)
    for t in beats:
        f = round(t * FPS)
        if 0 <= f < T:
            tb[f] = 1.0
    for t in downs:
        f = round(t * FPS)
        if 0 <= f < T:
            td[f] = 1.0
    return Track(spect, tb, td, beats, downs)


def f_measure(ref, est, window=EVAL_WINDOW):
    """Beat F-measure, mir_eval semantics: trim first 5 s, greedy one-to-one
    matching within +-window on sorted lists."""
    ref = [t for t in ref if t >= EVAL_TRIM]
    est = [t for t in est if t >= EVAL_TRIM]
    if not ref or not est:
        return 0.0
    hits = 0
    j = 0
    for r in ref:
        while j < len(est) and est[j] < r - window:
            j += 1
        if j < len(est) and abs(est[j] - r) <= window:
            hits += 1
            j += 1
    if hits == 0:
        return 0.0
    p = hits / len(est)
    r = hits / len(ref)
    return 2 * p * r / (p + r)


def val_scores(model, val, postp, device, split_predict_aggregate):
    """Mean beat/downbeat F-measure over the val tracks (full-piece predict)."""
    import torch
    was_training = model.training
    model.eval()
    fbs, fds = [], []
    with torch.inference_mode():
        for tr in val:
            pred = split_predict_aggregate(
                tr.spect.to(device), CHUNK, BORDER, "keep_first", model)
            beat, down = postp(pred["beat"].float().cpu(),
                               pred["downbeat"].float().cpu())
            fbs.append(f_measure(tr.beat_times, [float(b) for b in beat]))
            fds.append(f_measure(tr.down_times, [float(d) for d in down]))
    if was_training:
        model.train()
    return sum(fbs) / len(fbs), sum(fds) / len(fds)


def make_batch(train, chunk_ids):
    import torch
    import torch.nn.functional as F
    xs, tbs, tds, masks = [], [], [], []
    for ti, s in chunk_ids:
        tr = train[ti]
        sp = tr.spect[s:s + CHUNK]
        tb = tr.tb[s:s + CHUNK]
        td = tr.td[s:s + CHUNK]
        n = sp.shape[0]
        mask = torch.zeros(CHUNK, dtype=torch.bool)
        mask[:n] = True
        if n < CHUNK:
            sp = F.pad(sp, (0, 0, 0, CHUNK - n))
            tb = F.pad(tb, (0, CHUNK - n))
            td = F.pad(td, (0, CHUNK - n))
        xs.append(sp)
        tbs.append(tb)
        tds.append(td)
        masks.append(mask)
    return (torch.stack(xs), torch.stack(tbs), torch.stack(tds),
            torch.stack(masks))


def main():
    manifest = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
    items = manifest["audio"]
    out_dir = Path(manifest["outDir"])
    base = manifest.get("baseCheckpoint") or "final0"
    epochs = int(manifest.get("epochs") or 3)
    lr = float(manifest.get("lr") or 5e-5)
    val_split = float(manifest.get("valSplit") or 0.1)
    seed = int(manifest.get("seed") or 1)
    if len(items) < 2:
        raise RuntimeError("need at least 2 verified tracks (train + val split)")

    import inspect

    import numpy as np
    import torch
    from beat_this.inference import (load_checkpoint, load_model,
                                     split_predict_aggregate)
    from beat_this.model.beat_tracker import BeatThis
    from beat_this.model.loss import ShiftTolerantBCELoss
    from beat_this.model.postprocessor import Postprocessor
    from beat_this.preprocessing import LogMelSpect
    from beat_this.utils import replace_state_dict_key

    want = os.environ.get("GRIDFIX_DEVICE", "auto")
    device = ("cuda" if torch.cuda.is_available() else "cpu") if want == "auto" else want
    random.seed(seed)
    np.random.seed(seed & 0xFFFFFFFF)
    torch.manual_seed(seed)

    ckpt = load_checkpoint(base, "cpu")
    hparams = dict(ckpt["hyper_parameters"])
    model_kw = {k: v for k, v in hparams.items()
                if k in set(inspect.signature(BeatThis).parameters)}
    model = BeatThis(**model_kw)
    model.load_state_dict(
        replace_state_dict_key(dict(ckpt["state_dict"]), "model.", ""))
    model = model.to(device)

    emit(ev="start", tracks=len(items), device=device)

    spect_fn = LogMelSpect(device="cpu")
    tracks = []
    for it in items:
        log(f"preparing {it['path']}")
        tracks.append(prepare(it, spect_fn))

    # split train/val by track, never by chunk (chunk-level splits leak)
    idx = list(range(len(tracks)))
    random.shuffle(idx)
    n_val = min(max(1, round(val_split * len(tracks))), len(tracks) - 1)
    val = [tracks[i] for i in idx[:n_val]]
    train = [tracks[i] for i in idx[n_val:]]
    log(f"{len(train)} train / {len(val)} val tracks, device={device}, lr={lr}")

    postp = Postprocessor(type="minimal", fps=FPS)
    before_fb, before_fd = val_scores(model, val, postp, device,
                                      split_predict_aggregate)
    log(f"baseline val F-beat {before_fb:.4f} F-down {before_fd:.4f}")

    loss_fn = ShiftTolerantBCELoss().to(device)
    # AdamW, weight decay only on >=2-dim tensors (mirrors PLBeatThis)
    decay = [p for p in model.parameters() if p.requires_grad and p.ndim >= 2]
    rest = [p for p in model.parameters() if p.requires_grad and p.ndim < 2]
    opt = torch.optim.AdamW([{"params": decay, "weight_decay": 0.01},
                             {"params": rest, "weight_decay": 0.0}], lr=lr)

    # fixed non-overlapping 30 s chunks; a trailing sub-chunk shorter than
    # CHUNK is only kept when it is the track's sole chunk (then padded+masked)
    chunks = [(ti, s) for ti, tr in enumerate(train)
              for s in range(0, max(len(tr.spect) - CHUNK, 0) + 1, CHUNK)]

    model.train()
    fb, fd = before_fb, before_fd
    for epoch in range(1, epochs + 1):
        random.shuffle(chunks)
        losses = []
        for i in range(0, len(chunks), BATCH):
            x, tb, td, mask = make_batch(train, chunks[i:i + BATCH])
            x, tb, td = x.to(device), tb.to(device), td.to(device)
            mask = mask.to(device)
            pred = model(x)
            loss = loss_fn(pred["beat"], tb, mask) + loss_fn(pred["downbeat"], td, mask)
            opt.zero_grad(set_to_none=True)
            loss.backward()
            torch.nn.utils.clip_grad_norm_(model.parameters(), 1.0)
            opt.step()
            losses.append(float(loss.detach()))
        fb, fd = val_scores(model, val, postp, device, split_predict_aggregate)
        emit(ev="epoch", n=epoch, loss=sum(losses) / max(len(losses), 1),
             valFBeat=fb, valFDown=fd)

    out_dir.mkdir(parents=True, exist_ok=True)
    stamp = time.strftime("%Y%m%d-%H%M")
    path = out_dir / f"finetuned-{stamp}.ckpt"
    n = 2
    while path.exists():
        path = out_dir / f"finetuned-{stamp}-{n}.ckpt"
        n += 1
    sd = {"model." + k: v.detach().cpu() for k, v in model.state_dict().items()}
    torch.save({"state_dict": sd, "hyper_parameters": hparams}, str(path))
    load_model(str(path), "cpu")  # verify the artifact round-trips through inference
    emit(ev="done", checkpoint=str(path),
         report={"beforeFBeat": before_fb, "afterFBeat": fb,
                 "improved": fb >= before_fb})


if __name__ == "__main__":
    try:
        main()
    except Exception as e:  # one terminal error event, non-zero exit
        emit(ev="error", msg=f"{type(e).__name__}: {e}")
        sys.exit(1)
