package transport

import (
	"testing"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

func TestScriptedFrameIODelaysAndReleasesFrame(t *testing.T) {
	frames := NewScriptedFrameIO()
	clientConn, serverConn := frames.OpenConnPair("conn-1")

	body, err := protocol.MarshalConfigAck(protocol.ConfigAck{
		ConfigVersion: 9,
		Status:        protocol.StatusOK,
	})
	if err != nil {
		t.Fatalf("marshal config ack: %v", err)
	}
	frameBytes, err := protocol.Frame{
		Type:      protocol.TypeConfigAck,
		RequestID: 7,
		Body:      body,
	}.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}

	frames.AddRule(ScriptedRule{
		ConnID:    "conn-1",
		Direction: testsupport.FrameDirectionClientToServer,
		FrameType: protocol.TypeConfigAck,
		Action:    testsupport.FrameActionDelay,
	})

	if err := frames.WriteFrame(clientConn, frameBytes, time.Second, FrameContext{
		Side:      FrameSideClient,
		ConnID:    "conn-1",
		SessionID: 12,
	}); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	done := make(chan []byte, 1)
	go func() {
		payload, err := frames.ReadFrame(serverConn, 50*time.Millisecond, FrameContext{Side: FrameSideServer, ConnID: "conn-1", SessionID: 12})
		if err == nil {
			done <- payload
		}
	}()

	select {
	case <-done:
		t.Fatal("frame should not be delivered before release")
	case <-time.After(100 * time.Millisecond):
	}

	released := frames.ReleaseDelayed(ReleaseFilter{ConnID: "conn-1"})
	if released != 1 {
		t.Fatalf("expected 1 released frame, got %d", released)
	}

	payload, err := frames.ReadFrame(serverConn, time.Second, FrameContext{Side: FrameSideServer, ConnID: "conn-1", SessionID: 12})
	if err != nil {
		t.Fatalf("read released frame: %v", err)
	}
	parsed, err := protocol.ParseFrame(payload)
	if err != nil {
		t.Fatalf("parse released frame: %v", err)
	}
	if parsed.Type != protocol.TypeConfigAck || parsed.RequestID != 7 {
		t.Fatalf("unexpected released frame: %#v", parsed)
	}

	observed := frames.ObserveState()
	if len(observed.Delayed) != 0 {
		t.Fatalf("expected delayed queue to be empty after release, got %d", len(observed.Delayed))
	}
	if len(observed.Delivered) != 1 {
		t.Fatalf("expected 1 delivered frame, got %d", len(observed.Delivered))
	}
}
