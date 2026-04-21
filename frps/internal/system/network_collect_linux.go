//go:build linux

package system

func newPlatformCollector() collector {
	return netInterfaceCollector{platform: "linux"}
}
