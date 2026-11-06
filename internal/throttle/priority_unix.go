//go:build !windows

package throttle

import "golang.org/x/sys/unix"

// lowerPriority raises niceness to Nice, never lowering an already higher one.
func lowerPriority() {
	cur, err := unix.Getpriority(unix.PRIO_PROCESS, 0)
	if err != nil {
		return
	}
	// Linux's raw getpriority returns 20-nice; x/sys reports the raw value
	// there and the nice value on BSD/Darwin.
	nice := cur
	if runtimeLinux {
		nice = 20 - cur
	}
	if nice >= Nice {
		return
	}
	_ = unix.Setpriority(unix.PRIO_PROCESS, 0, Nice)
}
