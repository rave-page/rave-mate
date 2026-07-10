# rave-mate gridfix engine runner: persistent Beat This! beat-detection child.
# Loads the model once, then serves newline-JSON requests on stdin:
#   {"id":1,"op":"ping"}                      -> {"id":1,"ok":true,"device":...,"versions":{...}}
#   {"id":2,"op":"analyze","path":"C:\\x.mp3"} -> {"id":2,"beats":[...],"downbeats":[...]}
# Errors: {"id":N,"error":"..."}. One reply line per request. Exit on stdin EOF.
# Mirrors traktor-grid-fix's BeatTracker pipeline exactly (soundfile mono mean,
# ffmpeg f32le 22050 fallback) so detections match the reference tool.
import json
import os
import subprocess
import sys
from pathlib import Path

SOUNDFILE_EXTS = {".mp3", ".flac", ".wav", ".aif", ".aiff", ".ogg"}

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(errors="replace")


def load_audio_any(path: Path):
    """Return (mono float32 signal, samplerate). ffmpeg fallback for m4a/aac etc."""
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


class Tracker:
    def __init__(self):
        self.a2b = None
        self.device = None
        self.checkpoint = None

    def ensure(self):
        if self.a2b is not None:
            return
        import torch
        want = os.environ.get("GRIDFIX_DEVICE", "auto")
        if want == "auto":
            self.device = "cuda" if torch.cuda.is_available() else "cpu"
        else:
            self.device = want
        from beat_this.inference import Audio2Beats
        ckpt = os.environ.get("GRIDFIX_CHECKPOINT") or "final0"
        self.a2b = Audio2Beats(checkpoint_path=ckpt, device=self.device)
        self.checkpoint = ckpt

    def analyze(self, path: Path):
        self.ensure()
        signal, sr = load_audio_any(path)
        beats, downbeats = self.a2b(signal, sr)
        return [float(b) for b in beats], [float(d) for d in downbeats]


def versions():
    import importlib.metadata as m
    out = {"python": sys.version.split()[0]}
    for pkg in ("beat-this", "torch", "numpy", "soundfile"):
        try:
            out[pkg] = m.version(pkg)
        except Exception:
            out[pkg] = None
    return out


def main():
    tracker = Tracker()
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        rid = None
        try:
            req = json.loads(line)
            rid = req.get("id")
            op = req.get("op")
            if op == "ping":
                reply = {"id": rid, "ok": True, "versions": versions()}
                if req.get("load_model"):
                    tracker.ensure()
                    reply["device"] = tracker.device
                print(json.dumps(reply), flush=True)
            elif op == "analyze":
                beats, downbeats = tracker.analyze(Path(req["path"]))
                print(json.dumps({"id": rid, "beats": beats,
                                  "downbeats": downbeats,
                                  "device": tracker.device}), flush=True)
            else:
                print(json.dumps({"id": rid, "error": f"unknown op {op!r}"}), flush=True)
        except Exception as e:  # never die on a bad track - report and continue
            print(json.dumps({"id": rid, "error": f"{type(e).__name__}: {e}"}), flush=True)


if __name__ == "__main__":
    main()
