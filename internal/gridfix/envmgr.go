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

// EnvStatus is the settings-card probe result.
type EnvStatus struct {
	BasePython  string // discovered system python ("" = none found)
	BaseVersion string // e.g. "3.12.10"
	EnvPython   string // managed venv python ("" = not installed)
	EngineOK    bool   // beat_this imports in the venv
	Versions    *Versions
	Device      string
	TorchCUDA   bool // installed torch is a CUDA build ("+cuNNN" version)
	GPUPresent  bool // an NVIDIA GPU/driver is present on this host
}

// EnvManager creates + probes the managed Python environment for the beat engine.
type EnvManager struct {
	DataDir    string // config.DataPath("gridfix")
	PythonPath string // user override for the base interpreter ("" = auto-discover)
}

func (m *EnvManager) envDir() string { return filepath.Join(m.DataDir, "env") }

// EnvPython returns the venv interpreter path if the venv exists ("" otherwise).
func (m *EnvManager) EnvPython() string {
	p := filepath.Join(m.envDir(), "Scripts", "python.exe")
	if runtime.GOOS != "windows" {
		p = filepath.Join(m.envDir(), "bin", "python")
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

// Status probes base python + venv + engine importability (no model load).
func (m *EnvManager) Status(ctx context.Context) EnvStatus {
	var st EnvStatus
	st.BasePython, st.BaseVersion = m.FindPython(ctx)
	st.EnvPython = m.EnvPython()
	if st.EnvPython == "" {
		return st
	}
	eng := &Engine{Python: st.EnvPython, DataDir: m.DataDir}
	defer eng.Stop()
	if v, dev, err := eng.Ping(ctx, false); err == nil {
		st.EngineOK, st.Versions, st.Device = true, v, dev
		st.TorchCUDA = v != nil && strings.Contains(v.Torch, "+cu")
	}
	st.GPUPresent = nvidiaPresent()
	return st
}

// nvidiaPresent reports an NVIDIA driver on this host (nvml.dll / nvidia-smi) - the gate
// for offering the CUDA-acceleration install.
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

// InstallCUDA upgrades the managed env's torch to the CUDA build (multi-GB download).
// Requires the engine to be installed first; the GPU toggle gates on the resulting
// TorchCUDA status, never on this having been merely attempted.
func (m *EnvManager) InstallCUDA(ctx context.Context, progress func(string)) error {
	py := m.EnvPython()
	if py == "" {
		return fmt.Errorf("beat engine not installed - install it first")
	}
	emit := func(s string) {
		if progress != nil {
			progress(s)
		}
	}
	emit("installing CUDA PyTorch build - this downloads several GB...")
	pip := []string{"-m", "pip", "install", "--no-input", "--disable-pip-version-check",
		"--progress-bar", "off", "--upgrade", "--index-url", torchIndexCUDA, "torch"}
	if err := m.stream(ctx, emit, py, pip...); err != nil {
		return fmt.Errorf("CUDA torch install failed: %w", err)
	}
	emit("verifying CUDA engine...")
	eng := &Engine{Python: py, DataDir: m.DataDir}
	defer eng.Stop()
	v, _, err := eng.Ping(ctx, false)
	if err != nil {
		return fmt.Errorf("engine verification failed: %w", err)
	}
	if v == nil || !strings.Contains(v.Torch, "+cu") {
		return fmt.Errorf("torch is still the CPU build after install")
	}
	emit("CUDA acceleration installed")
	return nil
}

// Install creates the venv and installs the pinned engine (CPU torch baseline - CUDA
// is a separate explicit upgrade via InstallCUDA), streaming tool output to progress.
func (m *EnvManager) Install(ctx context.Context, progress func(string)) error {
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
	emit("creating virtual environment...")
	if err := m.stream(ctx, emit, base, "-m", "venv", m.envDir()); err != nil {
		return fmt.Errorf("venv creation failed: %w", err)
	}
	py := m.EnvPython()
	if py == "" {
		return fmt.Errorf("venv created but interpreter missing")
	}
	pip := []string{"-m", "pip", "install", "--no-input", "--disable-pip-version-check", "--progress-bar", "off"}
	emit("installing PyTorch (CPU build) - this downloads a large package...")
	if err := m.stream(ctx, emit, py, append(pip, "--index-url", torchIndexCPU, "torch")...); err != nil {
		return fmt.Errorf("torch install failed: %w", err)
	}
	emit("installing Beat This! beat tracker...")
	if err := m.stream(ctx, emit, py, append(pip, pinBeatThis, "soundfile")...); err != nil {
		return fmt.Errorf("beat-this install failed: %w", err)
	}
	emit("verifying engine...")
	eng := &Engine{Python: py, DataDir: m.DataDir}
	defer eng.Stop()
	if _, _, err := eng.Ping(ctx, false); err != nil {
		return fmt.Errorf("engine verification failed: %w", err)
	}
	emit("engine installed")
	return nil
}

// Uninstall removes the managed venv + model cache.
func (m *EnvManager) Uninstall() error {
	if err := os.RemoveAll(m.envDir()); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(m.DataDir, "cache"))
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
