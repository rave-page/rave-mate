package gridfix

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"rave.page/mate/internal/sysexec"
)

// Known-good engine pins (verified against the reference tool; bump deliberately).
const (
	pinBeatThis = "beat-this==1.1.0"
	// torch index URLs: PyPI's Windows wheel is CPU-only, CUDA needs the cu index
	torchIndexCPU  = "https://download.pytorch.org/whl/cpu"
	torchIndexCUDA = "https://download.pytorch.org/whl/cu126"
)

// VariantStatus is one engine variant's probe result (CPU or CUDA venv).
type VariantStatus struct {
	Root     string // venv root dir ("" = not installed)
	Python   string // venv interpreter ("" = not installed)
	EngineOK bool   // beat_this imports in the venv
	Versions *Versions
	Device   string
	CUDA     bool // installed torch is a CUDA build ("+cuNNN" version)
}

// EnvStatus is the settings-card probe result. CPU and CUDA are independent installs
// (either, both, or neither may exist).
type EnvStatus struct {
	BasePython  string // discovered system python ("" = none found)
	BaseVersion string // e.g. "3.12.10"
	CPU         VariantStatus
	CUDA        VariantStatus // env-cuda; a legacy single env carrying CUDA torch reports here
	GPUPresent  bool          // an NVIDIA GPU/driver is present on this host
}

// SelectEngine picks interpreter + inference device for pref ("auto"|"cpu"|"cuda").
// auto = CUDA when installed and working, else CPU. A missing preferred variant falls
// back to the other one so a stale preference never bricks the fixer.
func (st EnvStatus) SelectEngine(pref string) (python, device string) {
	switch pref {
	case "cpu":
		if st.CPU.Python != "" {
			return st.CPU.Python, "cpu"
		}
		if st.CUDA.Python != "" {
			return st.CUDA.Python, "cpu" // CUDA build forced onto CPU
		}
	case "cuda":
		if st.CUDA.Python != "" {
			return st.CUDA.Python, "cuda"
		}
		if st.CPU.Python != "" {
			return st.CPU.Python, "cpu"
		}
	default: // auto
		if st.CUDA.EngineOK {
			return st.CUDA.Python, "cuda"
		}
		if st.CPU.Python != "" {
			return st.CPU.Python, "cpu"
		}
		if st.CUDA.Python != "" {
			return st.CUDA.Python, "cuda"
		}
	}
	return "", ""
}

// EnvManager creates + probes the managed Python environments for the beat engine.
type EnvManager struct {
	DataDir    string // config.DataPath("gridfix")
	PythonPath string // user override for the base interpreter ("" = auto-discover)
}

// envDir returns the variant venv root: env (CPU) / env-cuda.
func (m *EnvManager) envDir(cuda bool) string {
	if cuda {
		return filepath.Join(m.DataDir, "env-cuda")
	}
	return filepath.Join(m.DataDir, "env")
}

// EnvPython returns the variant venv interpreter path if it exists ("" otherwise).
func (m *EnvManager) EnvPython(cuda bool) string {
	return venvPython(m.envDir(cuda))
}

func venvPython(root string) string {
	p := filepath.Join(root, "Scripts", "python.exe")
	if runtime.GOOS != "windows" {
		p = filepath.Join(root, "bin", "python")
	}
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

var pyVersionRe = regexp.MustCompile(`Python (\d+)\.(\d+)\.(\d+)`)

// FindPython discovers a usable base interpreter: the override first, then the
// Windows launcher, then PATH names. Needs 3.10-3.14 (torch cp310-cp314 wheels).
func (m *EnvManager) FindPython(ctx context.Context) (path, version string) {
	type cand struct {
		exe  string
		args []string
	}
	var cands []cand
	if m.PythonPath != "" {
		cands = append(cands, cand{m.PythonPath, nil})
	}
	if runtime.GOOS == "windows" {
		cands = append(cands, cand{"py", []string{"-3"}})
	}
	cands = append(cands, cand{"python3", nil}, cand{"python", nil})
	for _, c := range cands {
		cmd := exec.CommandContext(ctx, c.exe, append(c.args, "--version")...)
		sysexec.Hide(cmd)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		mm := pyVersionRe.FindStringSubmatch(string(out))
		if mm == nil {
			continue
		}
		major, _ := strconv.Atoi(mm[1])
		minor, _ := strconv.Atoi(mm[2])
		if major != 3 || minor < 10 || minor > 14 {
			continue
		}
		// resolve the launcher/name to the real exe so the venv module runs on it
		resolve := exec.CommandContext(ctx, c.exe, append(c.args, "-c", "import sys;print(sys.executable)")...)
		sysexec.Hide(resolve)
		if exe, err := resolve.Output(); err == nil {
			if p := strings.TrimSpace(string(exe)); p != "" {
				return p, mm[1] + "." + mm[2] + "." + mm[3]
			}
		}
	}
	return "", ""
}

// Status probes base python + both variant venvs + engine importability (no model load).
func (m *EnvManager) Status(ctx context.Context) EnvStatus {
	var st EnvStatus
	st.BasePython, st.BaseVersion = m.FindPython(ctx)
	st.CPU = m.probeVariant(ctx, false)
	st.CUDA = m.probeVariant(ctx, true)
	// legacy single-env installs upgraded torch in place - report a CUDA-torch "env" as
	// the CUDA engine (env-cuda wins when both exist)
	if st.CPU.CUDA && st.CUDA.Python == "" {
		st.CUDA, st.CPU = st.CPU, VariantStatus{}
	}
	st.GPUPresent = nvidiaPresent()
	return st
}

// probeVariant pings one variant venv (spawns Python; ~seconds).
func (m *EnvManager) probeVariant(ctx context.Context, cuda bool) VariantStatus {
	var v VariantStatus
	v.Python = m.EnvPython(cuda)
	if v.Python == "" {
		return v
	}
	v.Root = m.envDir(cuda)
	eng := &Engine{Python: v.Python, DataDir: m.DataDir}
	defer eng.Stop()
	if vers, dev, err := eng.Ping(ctx, false); err == nil {
		v.EngineOK, v.Versions, v.Device = true, vers, dev
		v.CUDA = vers != nil && strings.Contains(vers.Torch, "+cu")
	}
	return v
}

// nvidiaPresent reports an NVIDIA driver on this host (nvml.dll / nvidia-smi) - drives
// the hint on the CUDA install (greyed out without one, never hidden).
func nvidiaPresent() bool {
	if runtime.GOOS == "windows" {
		if sysRoot := os.Getenv("SystemRoot"); sysRoot != "" {
			if _, err := os.Stat(filepath.Join(sysRoot, "System32", "nvml.dll")); err == nil {
				return true
			}
		}
	}
	_, err := exec.LookPath("nvidia-smi")
	return err == nil
}

// Install creates the variant venv and installs the pinned engine, streaming tool output
// to progress. CPU and CUDA are independent: either can be installed first, both can
// coexist (CUDA = multi-GB torch build, explicit opt-in).
func (m *EnvManager) Install(ctx context.Context, cuda bool, progress func(string)) error {
	base, ver := m.FindPython(ctx)
	if base == "" {
		return fmt.Errorf("no Python 3.10-3.14 found - install it from python.org (or the Microsoft Store) first")
	}
	emit := func(s string) {
		if progress != nil {
			progress(s)
		}
	}
	emit(fmt.Sprintf("using Python %s (%s)", ver, base))
	if err := os.MkdirAll(m.DataDir, 0o700); err != nil {
		return err
	}
	dir := m.envDir(cuda)
	emit("creating virtual environment...")
	if err := m.stream(ctx, emit, base, "-m", "venv", dir); err != nil {
		return fmt.Errorf("venv creation failed: %w", err)
	}
	py := venvPython(dir)
	if py == "" {
		return fmt.Errorf("venv created but interpreter missing")
	}
	pip := []string{"-m", "pip", "install", "--no-input", "--disable-pip-version-check", "--progress-bar", "off"}
	torchIndex := torchIndexCPU
	if cuda {
		torchIndex = torchIndexCUDA
		emit("installing PyTorch (CUDA build) - this downloads several GB...")
	} else {
		emit("installing PyTorch (CPU build) - this downloads a large package...")
	}
	if err := m.stream(ctx, emit, py, append(pip, "--index-url", torchIndex, "torch")...); err != nil {
		return fmt.Errorf("torch install failed: %w", err)
	}
	emit("installing Beat This! beat tracker...")
	if err := m.stream(ctx, emit, py, append(pip, pinBeatThis, "soundfile")...); err != nil {
		return fmt.Errorf("beat-this install failed: %w", err)
	}
	emit("verifying engine...")
	eng := &Engine{Python: py, DataDir: m.DataDir}
	defer eng.Stop()
	v, _, err := eng.Ping(ctx, false)
	if err != nil {
		return fmt.Errorf("engine verification failed: %w", err)
	}
	if cuda && (v == nil || !strings.Contains(v.Torch, "+cu")) {
		return fmt.Errorf("torch is not a CUDA build after install")
	}
	emit("engine installed")
	return nil
}

// Uninstall removes a variant venv by its root dir (must live under DataDir); the shared
// model cache is removed once no variant remains.
func (m *EnvManager) Uninstall(root string) error {
	if root == "" {
		return fmt.Errorf("nothing to remove")
	}
	rel, err := filepath.Rel(m.DataDir, root)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("refusing to remove %s: outside the managed dir", root)
	}
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	if m.EnvPython(false) == "" && m.EnvPython(true) == "" {
		return os.RemoveAll(filepath.Join(m.DataDir, "cache"))
	}
	return nil
}

// stream runs a command and forwards each stdout/stderr line to emit.
func (m *EnvManager) stream(ctx context.Context, emit func(string), exe string, args ...string) error {
	cmd := exec.CommandContext(ctx, exe, args...)
	sysexec.Hide(cmd)
	sysexec.BelowNormalPriority(cmd)
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = pw, pw // interleave; pip errors land in the same feed
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return err
	}
	_ = pw.Close() // child holds the write end now
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			emit(line)
		}
	}
	_ = pr.Close()
	return cmd.Wait()
}
