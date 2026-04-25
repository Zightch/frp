package client

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/transport"
)

func TestClientApplyConfigPushRejectsInvalidHeaders(t *testing.T) {
	cases := []struct {
		name        string
		frame       protocol.Frame
		wantMessage string
	}{
		{
			name:        "request id zero",
			frame:       protocol.Frame{Type: protocol.TypeConfigPush, RequestID: 0},
			wantMessage: "config.push requestId must be non-zero",
		},
		{
			name:        "stream id non-zero",
			frame:       protocol.Frame{Type: protocol.TypeConfigPush, RequestID: 7, StreamID: 1},
			wantMessage: "config.push streamId must be zero",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			defer clientConn.Close()
			defer serverConn.Close()

			client := newTestClient()
			state := newSessionState(1000)

			pushBody, err := protocol.MarshalConfigPush(validConfigPush(t))
			if err != nil {
				t.Fatalf("marshal config.push: %v", err)
			}
			tc.frame.Body = pushBody

			err = client.applyConfigPush(clientConn, state, tc.frame)
			if err == nil || !strings.Contains(err.Error(), tc.wantMessage) {
				t.Fatalf("unexpected header validation error: %v", err)
			}
			assertNoFrameWritten(t, serverConn)
		})
	}
}

func TestClientApplyConfigPushRejectsInvalidSnapshotsWithoutAckOrRuntimeTeardown(t *testing.T) {
	cases := []struct {
		name        string
		push        protocol.ConfigPush
		wantMessage string
	}{
		{
			name: "config version zero",
			push: protocol.ConfigPush{
				ConfigVersion: 0,
				GeneratedAtMs: 200,
				Tunnels:       validConfigPush(t).Tunnels,
			},
			wantMessage: "config.push configVersion must be non-zero",
		},
		{
			name: "duplicate tunnel ids",
			push: protocol.ConfigPush{
				ConfigVersion: 2,
				GeneratedAtMs: 200,
				Tunnels: []protocol.TunnelEntry{
					validConfigPush(t).Tunnels[0],
					validConfigPush(t).Tunnels[0],
				},
			},
			wantMessage: "duplicate tunnelId",
		},
		{
			name: "unsupported protocol",
			push: protocol.ConfigPush{
				ConfigVersion: 2,
				GeneratedAtMs: 200,
				Tunnels: []protocol.TunnelEntry{
					{
						TunnelID:    7,
						Protocol:    99,
						TunnelFlags: protocol.TunnelFlagEnabled,
						RemoteStart: 20000,
						RemoteEnd:   20000,
						LocalHost:   mustHost(t, "127.0.0.1"),
						LocalStart:  5000,
						LocalEnd:    5000,
					},
				},
			},
			wantMessage: "unsupported tunnel protocol",
		},
		{
			name: "unsupported flags",
			push: protocol.ConfigPush{
				ConfigVersion: 2,
				GeneratedAtMs: 200,
				Tunnels: []protocol.TunnelEntry{
					{
						TunnelID:    7,
						Protocol:    protocol.ProtocolTCP,
						TunnelFlags: 0x80,
						RemoteStart: 20000,
						RemoteEnd:   20000,
						LocalHost:   mustHost(t, "127.0.0.1"),
						LocalStart:  5000,
						LocalEnd:    5000,
					},
				},
			},
			wantMessage: "unsupported tunnel flags",
		},
		{
			name: "zero ports",
			push: protocol.ConfigPush{
				ConfigVersion: 2,
				GeneratedAtMs: 200,
				Tunnels: []protocol.TunnelEntry{
					{
						TunnelID:    7,
						Protocol:    protocol.ProtocolTCP,
						TunnelFlags: protocol.TunnelFlagEnabled,
						RemoteStart: 0,
						RemoteEnd:   0,
						LocalHost:   mustHost(t, "127.0.0.1"),
						LocalStart:  5000,
						LocalEnd:    5000,
					},
				},
			},
			wantMessage: "tunnel ports must be between 1 and 65535",
		},
		{
			name: "single tunnel mismatched range",
			push: protocol.ConfigPush{
				ConfigVersion: 2,
				GeneratedAtMs: 200,
				Tunnels: []protocol.TunnelEntry{
					{
						TunnelID:    7,
						Protocol:    protocol.ProtocolTCP,
						TunnelFlags: protocol.TunnelFlagEnabled,
						RemoteStart: 20000,
						RemoteEnd:   20001,
						LocalHost:   mustHost(t, "127.0.0.1"),
						LocalStart:  5000,
						LocalEnd:    5001,
					},
				},
			},
			wantMessage: "single tunnel must use identical start and end ports",
		},
		{
			name: "range mapping misaligned",
			push: protocol.ConfigPush{
				ConfigVersion: 2,
				GeneratedAtMs: 200,
				Tunnels: []protocol.TunnelEntry{
					{
						TunnelID:    7,
						Protocol:    protocol.ProtocolTCP,
						TunnelFlags: protocol.TunnelFlagEnabled | protocol.TunnelFlagRange,
						RemoteStart: 20000,
						RemoteEnd:   20002,
						LocalHost:   mustHost(t, "127.0.0.1"),
						LocalStart:  5000,
						LocalEnd:    5001,
					},
				},
			},
			wantMessage: "remote and local port ranges must be aligned",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			defer clientConn.Close()
			defer serverConn.Close()

			client := newTestClient()
			state, cleanup := newStateWithActiveRuntime(t)
			defer cleanup()

			body, err := protocol.MarshalConfigPush(tc.push)
			if err != nil {
				t.Fatalf("marshal invalid config.push: %v", err)
			}

			err = client.applyConfigPush(clientConn, state, protocol.Frame{
				Type:      protocol.TypeConfigPush,
				RequestID: 7,
				Body:      body,
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantMessage) {
				t.Fatalf("unexpected invalid snapshot error: %v", err)
			}

			assertNoFrameWritten(t, serverConn)

			snapshot := state.snapshotValue()
			if snapshot.ConfigVersion != 1 {
				t.Fatalf("expected snapshot to remain unchanged, got %#v", snapshot)
			}
			if state.activeStreams.Load() != 1 {
				t.Fatalf("expected active stream to remain open, got %d", state.activeStreams.Load())
			}
			if state.activeUDPSessions.Load() != 1 {
				t.Fatalf("expected active udp session to remain open, got %d", state.activeUDPSessions.Load())
			}
			if state.lastAckedConfigVersion.Load() != 1 {
				t.Fatalf("expected last acked config version to remain unchanged, got %d", state.lastAckedConfigVersion.Load())
			}
		})
	}
}

func TestClientRunSessionSurfacesContactAdministratorMessage(t *testing.T) {
	credentials := testCredentials(t)

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	client := newTestClient()
	ctx := context.Background()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		defer serverConn.Close()

		performPlainTransportHello(t, serverConn, credentials.ClientID)

		frame := readFrame(t, serverConn)
		if frame.Type != protocol.TypeAuthBegin {
			t.Errorf("expected auth.begin, got %s", frame.Type.String())
			return
		}

		challenge := protocol.AuthChallenge{
			ChallengeID: 7,
			ExpiresInMs: 5000,
		}
		copy(challenge.Nonce[:], []byte("nonce-1234567890"))
		challengeBody, err := protocol.MarshalAuthChallenge(challenge)
		if err != nil {
			t.Errorf("marshal auth.challenge: %v", err)
			return
		}
		writeFrame(t, serverConn, protocol.Frame{
			Type:      protocol.TypeAuthChallenge,
			RequestID: frame.RequestID,
			Body:      challengeBody,
		})

		frame = readFrame(t, serverConn)
		if frame.Type != protocol.TypeAuthFinish {
			t.Errorf("expected auth.finish, got %s", frame.Type.String())
			return
		}
		finish, err := protocol.UnmarshalAuthFinish(frame.Body)
		if err != nil {
			t.Errorf("unmarshal auth.finish: %v", err)
			return
		}
		secretHash := sha256.Sum256(credentials.ClientSecret[:])
		if want := protocol.ChallengeResponse(secretHash, challenge.Nonce); finish.Response != want {
			t.Errorf("unexpected auth response")
			return
		}

		helloBody, err := protocol.MarshalServerHello(protocol.ServerHello{
			HeartbeatIntervalMs: 60000,
			SessionID:           11,
			ServerVersion:       "test-server",
		})
		if err != nil {
			t.Errorf("marshal server.hello: %v", err)
			return
		}
		writeFrame(t, serverConn, protocol.Frame{
			Type:      protocol.TypeServerHello,
			RequestID: frame.RequestID,
			Body:      helloBody,
		})

		pushBody, err := protocol.MarshalConfigPush(validConfigPush(t))
		if err != nil {
			t.Errorf("marshal config.push: %v", err)
			return
		}
		writeFrame(t, serverConn, protocol.Frame{
			Type:      protocol.TypeConfigPush,
			RequestID: 2147483648,
			Body:      pushBody,
		})

		ackFrame := readFrame(t, serverConn)
		if ackFrame.Type != protocol.TypeConfigAck {
			t.Errorf("expected config.ack, got %s", ackFrame.Type.String())
			return
		}

		errorBody, err := protocol.MarshalErrorBody(protocol.ErrorBody{
			ErrorCode: protocol.ErrorCodeConfigApplyFailed,
			Message:   `生效 IP "127.0.0.2" 当前不存在于本机，请联系管理员解决`,
		})
		if err != nil {
			t.Errorf("marshal error body: %v", err)
			return
		}
		writeFrame(t, serverConn, protocol.Frame{
			Type:      protocol.TypeError,
			RequestID: ackFrame.RequestID,
			Body:      errorBody,
		})
	}()

	err := client.runSession(ctx, clientConn, credentials)
	if err == nil {
		t.Fatal("expected runSession to surface startup rejection")
	}
	if !strings.Contains(err.Error(), "请联系管理员解决") {
		t.Fatalf("expected user-visible error to contain contact-administrator hint, got %v", err)
	}
	if !strings.Contains(err.Error(), "frps error 1201") {
		t.Fatalf("expected user-visible error to preserve remote error code, got %v", err)
	}

	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("mock server did not exit")
	}
}

func newTestClient() *Client {
	return New(testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test-client")
}

func validConfigPush(t *testing.T) protocol.ConfigPush {
	t.Helper()

	return protocol.ConfigPush{
		ConfigVersion: 2,
		GeneratedAtMs: 200,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: 20000,
				RemoteEnd:   20000,
				LocalHost:   mustHost(t, "127.0.0.1"),
				LocalStart:  5000,
				LocalEnd:    5000,
			},
		},
	}
}

func newStateWithActiveRuntime(t *testing.T) (*sessionState, func()) {
	t.Helper()

	state := newSessionState(1000)
	state.setSnapshot(protocol.ConfigPush{
		ConfigVersion: 1,
		GeneratedAtMs: 100,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: 20000,
				RemoteEnd:   20000,
				LocalHost:   mustHost(t, "127.0.0.1"),
				LocalStart:  5000,
				LocalEnd:    5000,
			},
			{
				TunnelID:    8,
				Protocol:    protocol.ProtocolUDP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: 21000,
				RemoteEnd:   21000,
				LocalHost:   mustHost(t, "127.0.0.1"),
				LocalStart:  5300,
				LocalEnd:    5300,
			},
		},
	})
	state.lastAckedConfigVersion.Store(1)

	streamConn, streamPeer := net.Pipe()
	if !state.addStream(41, &localStream{conn: streamConn}) {
		t.Fatal("failed to register test stream")
	}

	udpServer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1").To4(), Port: 0})
	if err != nil {
		t.Fatalf("listen udp server: %v", err)
	}
	udpConn, err := net.DialUDP("udp", nil, udpServer.LocalAddr().(*net.UDPAddr))
	if err != nil {
		_ = udpServer.Close()
		t.Fatalf("dial udp server: %v", err)
	}
	if !state.addUDPSession(52, &localUDPSession{
		conn:   udpConn,
		target: udpServer.LocalAddr().String(),
		open: protocol.UDPOpen{
			TunnelID:   8,
			RemotePort: 21000,
		},
	}) {
		_ = udpConn.Close()
		_ = udpServer.Close()
		t.Fatal("failed to register test udp session")
	}

	return state, func() {
		state.closeAllStreams()
		state.closeAllUDPSessions()
		_ = streamPeer.Close()
		_ = udpServer.Close()
	}
}

func assertNoFrameWritten(t *testing.T, conn net.Conn) {
	t.Helper()

	_, err := transport.ReadFrame(conn, 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected no frame to be written")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected timeout while waiting for frame, got %v", err)
	}
}
