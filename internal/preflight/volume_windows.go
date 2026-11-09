//go:build windows

package preflight

import (
	"strings"

	"golang.org/x/sys/windows"
)

// resolveExisting returns the final path of an existing file or directory with
// every reparse point (symlink or junction) followed. EvalSymlinks alone does
// not follow junctions on Windows, so the Win32 final-path query is the
// authoritative source.
func resolveExisting(path string) (string, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(
		ptr,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)

	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(handle, &buf[0], uint32(len(buf)), 0) // 0 = VOLUME_NAME_DOS
		if err != nil {
			return "", err
		}
		if n < uint32(len(buf)) {
			return strings.TrimPrefix(windows.UTF16ToString(buf[:n]), `\\?\`), nil
		}
		buf = make([]uint16, n+1)
	}
}

// volumeAllowed reports whether a resolved volume is the required D: volume.
func volumeAllowed(volume string) bool {
	return normalizeVolume(volume) == RequiredVolume
}
