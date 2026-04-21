//go:build windows

package system

func newPlatformCollector() collector {
	return netInterfaceCollector{platform: "windows"}
}
