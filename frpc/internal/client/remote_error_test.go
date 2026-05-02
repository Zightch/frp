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
