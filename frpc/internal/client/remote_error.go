package client

import (
	"errors"
	"fmt"
	"strings"

	"github.com/zightch/frp/frps/pkg/protocol"
)

type remoteError struct {
	Code      uint16
	Retryable bool
	Message   string
}

func (e *remoteError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("frps error %d: %s", e.Code, e.Message)
}

func isLoginConflictError(err error) bool {
	var remote *remoteError
	if !errors.As(err, &remote) {
		return false
	}
	if remote.Code == protocol.ErrorCodeAuthClientLimitReached {
		return true
	}

	message := strings.ToLower(strings.TrimSpace(remote.Message))
	return strings.Contains(message, "already online") ||
		strings.Contains(message, "other frpc online") ||
		strings.Contains(message, "登录冲突")
}

func isTerminalRemoteError(err error) bool {
	var remote *remoteError
	if !errors.As(err, &remote) {
		return false
	}
	return !remote.Retryable
}
