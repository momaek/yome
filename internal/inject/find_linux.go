//go:build linux

package inject

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

// eviocgname is EVIOCGNAME(len): _IOC(_IOC_READ, 'E', 0x06, len).
func eviocgname(fd uintptr, buf []byte) error {
	req := uintptr(2)<<30 | uintptr(len(buf))<<16 | uintptr('E')<<8 | 0x06
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		return errno
	}
	return nil
}

// DeviceName reads the evdev name of an input device node.
func DeviceName(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	var buf [256]byte
	if err := eviocgname(f.Fd(), buf[:]); err != nil {
		return "", fmt.Errorf("EVIOCGNAME on %s: %w", path, err)
	}
	return strings.TrimRight(string(buf[:]), "\x00"), nil
}

// ListDevices returns every /dev/input/event* node with its evdev name.
// Nodes that cannot be opened or queried are skipped.
func ListDevices() (map[string]string, error) {
	paths, err := filepath.Glob("/dev/input/event*")
	if err != nil {
		return nil, fmt.Errorf("glob input devices: %w", err)
	}
	sort.Strings(paths)
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		if name, err := DeviceName(p); err == nil {
			out[p] = name
		}
	}
	return out, nil
}

// FindDevice locates an input device by its evdev name. Event node numbering is
// not stable across boots, so the daemon never hardcodes eventX.
func FindDevice(name string) (string, error) {
	devs, err := ListDevices()
	if err != nil {
		return "", err
	}
	for path, n := range devs {
		if n == name {
			return path, nil
		}
	}
	var found []string
	for path, n := range devs {
		found = append(found, fmt.Sprintf("%s=%q", path, n))
	}
	sort.Strings(found)
	return "", fmt.Errorf("no input device named %q; found: %s", name, strings.Join(found, " "))
}
