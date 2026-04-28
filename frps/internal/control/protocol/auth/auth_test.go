package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	controldomainruntime "github.com/zightch/frp/frps/internal/control/domain/runtime"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestChallengeServiceRejectsReplay(t *testing.T) {
	clock := &manualClock{now: time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)}
	var secretHash [32]byte
	copy(secretHash[:], []byte("0123456789abcdef0123456789abcdef"))

	service := NewChallengeService(ChallengeServiceOptions{
		Clock:  clock,
		TTL:    time.Second,
		Random: bytes.NewReader([]byte("abcdefghijklmnop")),
	})
	challenge, err := service.Issue(secretHash)
	if err != nil {
		t.Fatalf("issue challenge: %v", err)
	}
	response := protocol.ChallengeResponse(secretHash, challenge.Nonce)
	if err := service.Consume(challenge.ChallengeID, response); err != nil {
		t.Fatalf("consume challenge: %v", err)
	}

	err = service.Consume(challenge.ChallengeID, response)
	if code := protocolErrorCode(t, err); code != protocol.ErrorCodeAuthChallengeReplayed {
		t.Fatalf("unexpected error code: got %d want %d", code, protocol.ErrorCodeAuthChallengeReplayed)
	}
}

func TestChallengeServiceRejectsExpiredChallenge(t *testing.T) {
	clock := &manualClock{now: time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)}
	var secretHash [32]byte
	copy(secretHash[:], []byte("0123456789abcdef0123456789abcdef"))

	service := NewChallengeService(ChallengeServiceOptions{
		Clock:  clock,
		TTL:    time.Second,
		Random: bytes.NewReader([]byte("abcdefghijklmnop")),
	})
	challenge, err := service.Issue(secretHash)
	if err != nil {
		t.Fatalf("issue challenge: %v", err)
	}
	clock.now = clock.now.Add(2 * time.Second)

	response := protocol.ChallengeResponse(secretHash, challenge.Nonce)
	err = service.Consume(challenge.ChallengeID, response)
	if code := protocolErrorCode(t, err); code != protocol.ErrorCodeAuthChallengeExpired {
		t.Fatalf("unexpected error code: got %d want %d", code, protocol.ErrorCodeAuthChallengeExpired)
	}
}

func TestChallengeServiceRejectsMismatchAndMarksChallengeUsed(t *testing.T) {
	clock := &manualClock{now: time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)}
	var secretHash [32]byte
	copy(secretHash[:], []byte("0123456789abcdef0123456789abcdef"))

	service := NewChallengeService(ChallengeServiceOptions{
		Clock:  clock,
		TTL:    time.Second,
		Random: bytes.NewReader([]byte("abcdefghijklmnop")),
	})
	challenge, err := service.Issue(secretHash)
	if err != nil {
		t.Fatalf("issue challenge: %v", err)
	}

	err = service.Consume(challenge.ChallengeID, [32]byte{})
	if code := protocolErrorCode(t, err); code != protocol.ErrorCodeAuthInvalidClient {
		t.Fatalf("unexpected mismatch error code: got %d want %d", code, protocol.ErrorCodeAuthInvalidClient)
	}

	response := protocol.ChallengeResponse(secretHash, challenge.Nonce)
	err = service.Consume(challenge.ChallengeID, response)
	if code := protocolErrorCode(t, err); code != protocol.ErrorCodeAuthChallengeReplayed {
		t.Fatalf("unexpected replay error code: got %d want %d", code, protocol.ErrorCodeAuthChallengeReplayed)
	}
}

func TestAuthenticateSucceedsWithoutServer(t *testing.T) {
	clock := &manualClock{now: time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)}
	var clientID [16]byte
	copy(clientID[:], []byte("client-id-000001"))
	var secretHash [32]byte
	copy(secretHash[:], []byte("0123456789abcdef0123456789abcdef"))
	nonce := [16]byte{}
	copy(nonce[:], []byte("abcdefghijklmnop"))

	writer := &recordingFrameWriter{}
	result, err := Authenticate(AuthenticateOptions{
		Conn:             noopConn{},
		ExpectedClientID: clientID,
		Reader: &sequenceFrameReader{
			frames: []protocol.Frame{
				authBeginFrame(t, clientID, 11),
				authFinishFrame(t, protocol.ChallengeResponse(secretHash, nonce), 22),
			},
		},
		Writer: writer,
		Repository: staticRepository{group: controldomainruntime.GroupRuntime{
			ID:               7,
			Enabled:          true,
			ClientSecretHash: secretHash,
		}},
		Challenges: NewChallengeService(ChallengeServiceOptions{
			Clock:  clock,
			TTL:    5 * time.Second,
			Random: bytes.NewReader(nonce[:]),
		}),
		ReadTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if result.Group.ID != 7 || result.FinishRequestID != 22 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(writer.frames) != 1 {
		t.Fatalf("expected one auth.challenge frame, got %d", len(writer.frames))
	}
	frame := writer.frames[0]
	if frame.Type != protocol.TypeAuthChallenge || frame.RequestID != 11 {
		t.Fatalf("unexpected written frame: %#v", frame)
	}
	challenge, err := protocol.UnmarshalAuthChallenge(frame.Body)
	if err != nil {
		t.Fatalf("unmarshal auth.challenge: %v", err)
	}
	if challenge.ChallengeID != 1 || challenge.Nonce != nonce || challenge.ExpiresInMs != 5000 {
		t.Fatalf("unexpected auth challenge: %#v", challenge)
	}
}

func TestDecodeAuthBeginFrameRejectsClientMismatch(t *testing.T) {
	var expected [16]byte
	copy(expected[:], []byte("client-id-000001"))
	var actual [16]byte
	copy(actual[:], []byte("client-id-000002"))

	_, err := DecodeAuthBeginFrame(authBeginFrame(t, actual, 1), expected)
	if code := protocolErrorCode(t, err); code != protocol.ErrorCodeAuthInvalidClient {
		t.Fatalf("unexpected error code: got %d want %d", code, protocol.ErrorCodeAuthInvalidClient)
	}
}

func TestDecodeAuthFinishFrameRejectsStreamID(t *testing.T) {
	frame := authFinishFrame(t, [32]byte{}, 1)
	frame.StreamID = 99

	_, err := DecodeAuthFinishFrame(frame)
	if code := protocolErrorCode(t, err); code != protocol.ErrorCodeProtocolBadBody {
		t.Fatalf("unexpected error code: got %d want %d", code, protocol.ErrorCodeProtocolBadBody)
	}
}

type manualClock struct {
	now time.Time
}

func (c *manualClock) Now() time.Time {
	return c.now
}

type staticRepository struct {
	group controldomainruntime.GroupRuntime
	err   error
}

func (r staticRepository) LoadGroupRuntimeByClientID(context.Context, [16]byte) (controldomainruntime.GroupRuntime, error) {
	return r.group, r.err
}

type sequenceFrameReader struct {
	frames []protocol.Frame
	index  int
}

func (r *sequenceFrameReader) ReadFrame(net.Conn) (protocol.Frame, error) {
	if r.index >= len(r.frames) {
		return protocol.Frame{}, fmt.Errorf("frame reader exhausted")
	}
	frame := r.frames[r.index]
	r.index++
	return frame, nil
}

type recordingFrameWriter struct {
	frames []protocol.Frame
}

func (w *recordingFrameWriter) WriteFrame(frame protocol.Frame) error {
	w.frames = append(w.frames, frame)
	return nil
}

func protocolErrorCode(t *testing.T, err error) uint16 {
	t.Helper()

	var protocolErr *protocol.ProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("expected protocol error, got %T %[1]v", err)
	}
	return protocolErr.Code
}

func authBeginFrame(t *testing.T, clientID [16]byte, requestID uint32) protocol.Frame {
	t.Helper()

	body, err := protocol.MarshalAuthBegin(protocol.AuthBegin{
		ClientID:      clientID,
		ClientVersion: "test-client",
		Hostname:      "node-1",
		OS:            protocol.OSLinux,
		Arch:          protocol.ArchAMD64,
	})
	if err != nil {
		t.Fatalf("marshal auth.begin: %v", err)
	}
	return protocol.Frame{
		Type:      protocol.TypeAuthBegin,
		RequestID: requestID,
		Body:      body,
	}
}

func authFinishFrame(t *testing.T, response [32]byte, requestID uint32) protocol.Frame {
	t.Helper()

	body, err := protocol.MarshalAuthFinish(protocol.AuthFinish{
		ChallengeID: 1,
		Response:    response,
	})
	if err != nil {
		t.Fatalf("marshal auth.finish: %v", err)
	}
	return protocol.Frame{
		Type:      protocol.TypeAuthFinish,
		RequestID: requestID,
		Body:      body,
	}
}

type noopConn struct{}

func (noopConn) Read([]byte) (int, error)         { return 0, nil }
func (noopConn) Write([]byte) (int, error)        { return 0, nil }
func (noopConn) Close() error                     { return nil }
func (noopConn) LocalAddr() net.Addr              { return noopAddr("local") }
func (noopConn) RemoteAddr() net.Addr             { return noopAddr("remote") }
func (noopConn) SetDeadline(time.Time) error      { return nil }
func (noopConn) SetReadDeadline(time.Time) error  { return nil }
func (noopConn) SetWriteDeadline(time.Time) error { return nil }

type noopAddr string

func (a noopAddr) Network() string { return string(a) }
func (a noopAddr) String() string  { return string(a) }
