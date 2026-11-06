//go:build windows

package throttle

import "golang.org/x/sys/windows"

func lowerPriority() {
	if h, err := windows.GetCurrentProcess(); err == nil {
		_ = windows.SetPriorityClass(h, windows.BELOW_NORMAL_PRIORITY_CLASS)
	}
}
