package client

import (
	"os"
	"runtime"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}

func detectOS() uint8 {
	switch runtime.GOOS {
	case "windows":
		return protocol.OSWindows
	case "linux":
		return protocol.OSLinux
	case "darwin":
		return protocol.OSDarwin
	default:
		return protocol.OSUnknown
	}
}

func detectArch() uint8 {
	switch runtime.GOARCH {
	case "amd64":
		return protocol.ArchAMD64
	case "arm64":
		return protocol.ArchARM64
	case "386":
		return protocol.Arch386
	case "arm":
		return protocol.ArchARM
	default:
		return protocol.ArchUnknown
	}
}
