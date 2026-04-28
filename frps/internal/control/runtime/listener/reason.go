package listener

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"syscall"

	controldomainruntime "github.com/zightch/frp/frps/internal/control/domain/runtime"
)

func BuildTunnelListenerStartReason(protocolValue uint8, effectiveIP string, remotePort uint16, cause error) string {
	addr := net.JoinHostPort(effectiveIP, strconv.Itoa(int(remotePort)))
	if IsListenPortConflictError(cause) {
		return fmt.Sprintf("%s 监听 %s 端口冲突，无法启动", strings.ToUpper(controldomainruntime.ProtocolName(protocolValue)), addr)
	}
	return fmt.Sprintf("%s 监听 %s 启动失败: %v", strings.ToUpper(controldomainruntime.ProtocolName(protocolValue)), addr, cause)
}

func IsListenPortConflictError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "address already in use") ||
		strings.Contains(message, "only one usage of each socket address") ||
		strings.Contains(message, "10048")
}
