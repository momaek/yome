//go:build !linux

package inject

import "errors"

// errNotLinux keeps the package building on a developer machine. Only the
// device-touching entry points fail; the geometry and envelope logic, which is
// what the tests cover, is platform independent.
var errNotLinux = errors.New("evdev input devices are only available on the reMarkable (linux/arm)")

// DeviceName is unavailable off-device.
func DeviceName(string) (string, error) { return "", errNotLinux }

// ListDevices is unavailable off-device.
func ListDevices() (map[string]string, error) { return nil, errNotLinux }

// FindDevice is unavailable off-device.
func FindDevice(string) (string, error) { return "", errNotLinux }
