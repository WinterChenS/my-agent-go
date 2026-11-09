// Package preflight enforces the Anvil rebuild rule that every writable
// location used by the product and its tooling lives on the D: volume.
//
// The rule is stated in docs/superpowers/specs/2026-11-09-anvil-rebuild-execution-standard.md:
// a path only counts as "on D:" after resolving relative components, symlinks
// and Windows junctions/reparse points to the volume the write actually lands
// on. Falling back to a default location when D: cannot be used is forbidden.
package preflight

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RequiredVolume is the only volume Anvil tools may write to on Windows.
const RequiredVolume = "D:"

var (
	// ErrOutsideD means the resolved path does not land on RequiredVolume.
	ErrOutsideD = errors.New("preflight: path is outside the required D: volume")
	// ErrNotWritable means the target directory could not be written to.
	ErrNotWritable = errors.New("preflight: path is not writable")
)

// Resolution describes how a requested path resolves on disk.
type Resolution struct {
	// Requested is the absolute, cleaned input path.
	Requested string
	// Existing is the nearest existing ancestor after reparse resolution.
	Existing string
	// Resolved is Existing plus the still-missing path suffix.
	Resolved string
	// Volume is the actual volume of Existing (for example "D:").
	Volume string
}

// Resolver resolves paths to their real volume and provides the filesystem
// operations preflight needs. Tests inject fakes; product code uses OSSystem.
type Resolver interface {
	Resolve(path string) (Resolution, error)
	MkdirAll(path string) error
	ProbeWritable(dir string) error
}

// OSSystem is the production Resolver backed by the operating system.
type OSSystem struct{}

// Resolve resolves path to its actual volume. Missing trailing components are
// carried along; the check runs against the nearest existing ancestor.
func (OSSystem) Resolve(path string) (Resolution, error) {
	if strings.TrimSpace(path) == "" {
		return Resolution{}, fmt.Errorf("%w: empty path", ErrOutsideD)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Resolution{}, fmt.Errorf("preflight: resolve %q: %w", path, err)
	}
	abs = filepath.Clean(abs)

	existing := abs
	var missing []string
	for {
		if _, statErr := os.Lstat(existing); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			return Resolution{}, fmt.Errorf("preflight: inspect %q: %w", existing, statErr)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return Resolution{}, fmt.Errorf("preflight: no existing ancestor for %q", path)
		}
		missing = append([]string{filepath.Base(existing)}, missing...)
		existing = parent
	}

	realExisting, err := resolveExisting(existing)
	if err != nil {
		return Resolution{}, fmt.Errorf("preflight: resolve actual path of %q: %w", existing, err)
	}

	resolved := realExisting
	if len(missing) > 0 {
		resolved = filepath.Join(append([]string{realExisting}, missing...)...)
	}
	return Resolution{
		Requested: abs,
		Existing:  realExisting,
		Resolved:  resolved,
		Volume:    filepath.VolumeName(realExisting),
	}, nil
}

// MkdirAll creates the directory and any missing parents.
func (OSSystem) MkdirAll(path string) error { return os.MkdirAll(path, 0o755) }

// ProbeWritable verifies a directory is writable by creating and removing a
// temporary file in it.
func (OSSystem) ProbeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".anvil-write-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	if closeErr := f.Close(); closeErr != nil {
		_ = os.Remove(name)
		return closeErr
	}
	return os.Remove(name)
}

// CheckPath resolves path and requires it to land on RequiredVolume. It does
// not create anything.
func CheckPath(path string, r Resolver) (Resolution, error) {
	if strings.TrimSpace(path) == "" {
		return Resolution{}, fmt.Errorf("%w: empty path", ErrOutsideD)
	}
	res, err := r.Resolve(path)
	if err != nil {
		return Resolution{}, err
	}
	if !volumeAllowed(res.Volume) {
		return res, fmt.Errorf("%w: %q resolves to volume %q", ErrOutsideD, path, res.Volume)
	}
	return res, nil
}

// EnsureDir resolves path, requires RequiredVolume, creates the directory and
// verifies it is writable. It never substitutes another location.
func EnsureDir(path string, r Resolver) (Resolution, error) {
	res, err := CheckPath(path, r)
	if err != nil {
		return Resolution{}, err
	}
	if err := r.MkdirAll(res.Resolved); err != nil {
		return Resolution{}, fmt.Errorf("%w: create %q: %v", ErrNotWritable, res.Resolved, err)
	}
	// Re-resolve after creation: a component may have been replaced by a
	// junction between the check and the write.
	resolved, err := CheckPath(res.Resolved, r)
	if err != nil {
		return Resolution{}, err
	}
	if err := r.ProbeWritable(resolved.Resolved); err != nil {
		return Resolution{}, fmt.Errorf("%w: probe %q: %v", ErrNotWritable, resolved.Resolved, err)
	}
	return resolved, nil
}

// normalizeVolume turns a raw volume name into a comparable form:
// `\\?\D:` -> `D:`, `\\?\UNC\host\share` -> `\\host\share`, trailing
// separators dropped, upper case for case-insensitive comparison.
func normalizeVolume(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, `\\?\UNC\`) {
		v = `\\` + v[len(`\\?\UNC\`):]
	}
	v = strings.TrimPrefix(v, `\\?\`)
	v = strings.TrimRight(v, `\/`)
	return strings.ToUpper(v)
}
