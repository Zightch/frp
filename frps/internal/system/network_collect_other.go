//go:build !linux && !windows

package system

import "runtime"

func newPlatformCollector() collector {
	return netInterfaceCollector{platform: runtime.GOOS}
}
