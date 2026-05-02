package client

import (
	"testing"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestIsLoginConflictErrorRecognizesProtocolCode(t *testing.T) {
	if !isLoginConflictError(&remoteError{Code: protocol.ErrorCodeAuthClientLimitReached, Message: "occupied"}) {
		t.Fatal("expected login conflict code to be recognized")
	}
	if isLoginConflictError(&remoteError{Code: protocol.ErrorCodeConfigApplyFailed, Message: "other"}) {
		t.Fatal("did not expect non-conflict code to be recognized")
	}
}

func TestIsTerminalRemoteErrorFollowsRetryableFlag(t *testing.T) {
	if !isTerminalRemoteError(&remoteError{Code: protocol.ErrorCodeAuthClientLimitReached, Retryable: false}) {
		t.Fatal("expected non-retryable remote error to be terminal")
	}
	if isTerminalRemoteError(&remoteError{Code: protocol.ErrorCodeAuthClientLimitReached, Retryable: true}) {
		t.Fatal("did not expect retryable remote error to be terminal")
	}
}
