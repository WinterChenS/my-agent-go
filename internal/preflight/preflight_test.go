package preflight

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeResolver gives tests complete control over the resolved volume, so
// reject cases never need a fixture on another drive.
type fakeResolver struct {
	resolveFn func(string) (Resolution, error)
	mkdirFn   func(string) error
	probeFn   func(string) error
	created   []string
	probed    []string
}

func (f *fakeResolver) Resolve(p string) (Resolution, error) {
	if f.resolveFn != nil {
		return f.resolveFn(p)
	}
	return Resolution{}, errors.New("fake resolver: no resolve function")
}

func (f *fakeResolver) MkdirAll(p string) error {
	f.created = append(f.created, p)
	if f.mkdirFn != nil {
		return f.mkdirFn(p)
	}
	return nil
}

func (f *fakeResolver) ProbeWritable(p string) error {
	f.probed = append(f.probed, p)
	if f.probeFn != nil {
		return f.probeFn(p)
	}
	return nil
}

func dResolution(p string) Resolution {
	return Resolution{Requested: p, Existing: p, Resolved: p, Volume: "D:"}
}

// requireDTemp returns a temporary directory that really lives on D:.
// The rebuild rule forbids writing fixtures to other drives.
func requireDTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	vol := strings.ToUpper(filepath.VolumeName(dir))
	if vol != "D:" {
		t.Fatalf("test temp dir %q is on volume %q, want D: (set TEMP/TMP/TMPDIR to a D: path)", dir, vol)
	}
	return dir
}

func requireWindowsVolumeRule(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("the D:-only write rule is enforced on Windows")
	}
}

func createJunction(t *testing.T, link, target string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("junctions are a Windows feature")
	}
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("mklink /J %q %q: %v (%s)", link, target, err, out)
	}
}

func TestCheckPathAcceptsDDrive(t *testing.T) {
	const target = `D:\project\anvil-wt\anvil-rebuild\.tmp\test`
	f := &fakeResolver{resolveFn: func(string) (Resolution, error) {
		return dResolution(target), nil
	}}
	res, err := CheckPath(target, f)
	if err != nil {
		t.Fatalf("CheckPath(%q) returned error: %v", target, err)
	}
	if res.Volume != "D:" {
		t.Fatalf("resolved volume = %q, want D:", res.Volume)
	}
	if res.Resolved != target {
		t.Fatalf("resolved path = %q, want %q", res.Resolved, target)
	}
}

func TestCheckPathRejectsOtherDrive(t *testing.T) {
	requireWindowsVolumeRule(t)
	f := &fakeResolver{resolveFn: func(p string) (Resolution, error) {
		return Resolution{Requested: p, Existing: p, Resolved: p, Volume: "C:"}, nil
	}}
	_, err := CheckPath(`C:\Users\Administrator\AppData`, f)
	if !errors.Is(err, ErrOutsideD) {
		t.Fatalf("CheckPath on C: returned %v, want ErrOutsideD", err)
	}
}

// A path whose string starts with D: but whose reparse target lands on C: must
// be rejected: the check uses the resolved volume, not the text prefix.
func TestCheckPathUsesResolvedVolumeNotPrefix(t *testing.T) {
	requireWindowsVolumeRule(t)
	f := &fakeResolver{resolveFn: func(string) (Resolution, error) {
		return Resolution{
			Requested: `D:\link-to-c`,
			Existing:  `C:\Windows`,
			Resolved:  `C:\Windows`,
			Volume:    "C:",
		}, nil
	}}
	_, err := CheckPath(`D:\link-to-c`, f)
	if !errors.Is(err, ErrOutsideD) {
		t.Fatalf("CheckPath on reparse-to-C: returned %v, want ErrOutsideD", err)
	}
}

func TestCheckPathRejectsUNC(t *testing.T) {
	requireWindowsVolumeRule(t)
	f := &fakeResolver{resolveFn: func(p string) (Resolution, error) {
		return Resolution{Requested: p, Existing: p, Resolved: p, Volume: `\\server\share`}, nil
	}}
	_, err := CheckPath(`\\server\share\anvil`, f)
	if !errors.Is(err, ErrOutsideD) {
		t.Fatalf("CheckPath on UNC returned %v, want ErrOutsideD", err)
	}
}

func TestCheckPathRejectsEmpty(t *testing.T) {
	requireWindowsVolumeRule(t)
	f := &fakeResolver{resolveFn: func(p string) (Resolution, error) {
		return dResolution(p), nil
	}}
	_, err := CheckPath("   ", f)
	if !errors.Is(err, ErrOutsideD) {
		t.Fatalf("CheckPath on empty path returned %v, want ErrOutsideD", err)
	}
}

func TestCheckPathPropagatesResolveError(t *testing.T) {
	boom := errors.New("resolve boom")
	f := &fakeResolver{resolveFn: func(string) (Resolution, error) { return Resolution{}, boom }}
	_, err := CheckPath(`D:\project`, f)
	if !errors.Is(err, boom) {
		t.Fatalf("CheckPath returned %v, want wrapped %v", err, boom)
	}
}

func TestEnsureDirCreatesMissingDirsAndProbes(t *testing.T) {
	base := requireDTemp(t)
	target := filepath.Join(base, "a", "b", "c")
	res, err := EnsureDir(target, OSSystem{})
	if err != nil {
		t.Fatalf("EnsureDir(%q) returned error: %v", target, err)
	}
	if !strings.EqualFold(res.Volume, "D:") {
		t.Fatalf("resolved volume = %q, want D:", res.Volume)
	}
	info, statErr := os.Stat(target)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("target directory was not created: info=%v err=%v", info, statErr)
	}
}

func TestEnsureDirRejectsOtherDriveOnRealFilesystem(t *testing.T) {
	requireWindowsVolumeRule(t)
	_, err := EnsureDir(`C:\Users\Administrator\AppData\Local\anvil-should-not-exist`, OSSystem{})
	if !errors.Is(err, ErrOutsideD) {
		t.Fatalf("EnsureDir on C: returned %v, want ErrOutsideD", err)
	}
	if _, statErr := os.Stat(`C:\Users\Administrator\AppData\Local\anvil-should-not-exist`); !os.IsNotExist(statErr) {
		t.Fatalf("EnsureDir must not create directories on C: (stat error: %v)", statErr)
	}
}

// A junction inside D: that points to another volume must be rejected, and no
// directory may be created at the junction target.
func TestEnsureDirRejectsJunctionToOtherVolume(t *testing.T) {
	base := requireDTemp(t)
	link := filepath.Join(base, "junction-to-windows")
	createJunction(t, link, `C:\Windows`)
	target := filepath.Join(link, "anvil-r0-00-must-not-exist")

	_, err := EnsureDir(target, OSSystem{})
	if !errors.Is(err, ErrOutsideD) {
		t.Fatalf("EnsureDir(%q) returned %v, want ErrOutsideD", target, err)
	}
	if _, statErr := os.Stat(`C:\Windows\anvil-r0-00-must-not-exist`); !os.IsNotExist(statErr) {
		t.Fatalf("preflight created a directory on C: (stat error: %v)", statErr)
	}
}

// A junction inside D: that points inside D: is legal.
func TestEnsureDirAcceptsJunctionInsideD(t *testing.T) {
	base := requireDTemp(t)
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", real, err)
	}
	link := filepath.Join(base, "junction-inside-d")
	createJunction(t, link, real)

	res, err := EnsureDir(link, OSSystem{})
	if err != nil {
		t.Fatalf("EnsureDir(%q) returned error: %v", link, err)
	}
	if !strings.EqualFold(res.Volume, "D:") {
		t.Fatalf("resolved volume = %q, want D:", res.Volume)
	}
}

// When D: is not writable, EnsureDir must fail and must not try another path.
func TestEnsureDirRejectsUnwritableWithoutFallback(t *testing.T) {
	requireWindowsVolumeRule(t)
	const target = `D:\project\anvil-wt\anvil-rebuild\.tmp\test\unwritable`
	f := &fakeResolver{
		resolveFn: func(p string) (Resolution, error) { return dResolution(p), nil },
		probeFn:   func(string) error { return os.ErrPermission },
	}
	res, err := EnsureDir(target, f)
	if !errors.Is(err, ErrNotWritable) {
		t.Fatalf("EnsureDir on unwritable D: returned %v, want ErrNotWritable", err)
	}
	if res.Resolved != "" {
		t.Fatalf("EnsureDir returned a fallback path %q; want no fallback", res.Resolved)
	}
	if len(f.created) != 1 || !strings.EqualFold(f.created[0], target) {
		t.Fatalf("created paths = %v, want exactly [%q]", f.created, target)
	}
	if len(f.probed) != 1 || !strings.EqualFold(f.probed[0], target) {
		t.Fatalf("probed paths = %v, want exactly [%q]", f.probed, target)
	}
}

// If re-resolving after creation lands on another volume, reject before any
// write probe happens.
func TestEnsureDirRejectsVolumeChangeAfterCreate(t *testing.T) {
	requireWindowsVolumeRule(t)
	const target = `D:\project\anvil-wt\anvil-rebuild\.tmp\test\moved`
	calls := 0
	f := &fakeResolver{
		resolveFn: func(p string) (Resolution, error) {
			calls++
			if calls == 1 {
				return dResolution(p), nil
			}
			return Resolution{Requested: p, Existing: p, Resolved: p, Volume: "C:"}, nil
		},
	}
	_, err := EnsureDir(target, f)
	if !errors.Is(err, ErrOutsideD) {
		t.Fatalf("EnsureDir returned %v, want ErrOutsideD after volume change", err)
	}
	if len(f.probed) != 0 {
		t.Fatalf("must not probe a path that moved off D:, probed=%v", f.probed)
	}
}

func TestEnsureDirNoFallbackWhenMkdirFails(t *testing.T) {
	requireWindowsVolumeRule(t)
	const target = `D:\project\anvil-wt\anvil-rebuild\.tmp\test\mkdir-fails`
	f := &fakeResolver{
		resolveFn: func(p string) (Resolution, error) { return dResolution(p), nil },
		mkdirFn:   func(string) error { return os.ErrPermission },
	}
	_, err := EnsureDir(target, f)
	if !errors.Is(err, ErrNotWritable) {
		t.Fatalf("EnsureDir returned %v, want ErrNotWritable", err)
	}
	if len(f.probed) != 0 {
		t.Fatalf("must not probe after a failed create, probed=%v", f.probed)
	}
}
