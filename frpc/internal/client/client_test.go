package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/transport"
)

func TestClientRunSession(t *testing.T) {
	credentials := testCredentials(t)

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	client := New(testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test-client")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
		begin, err := protocol.UnmarshalAuthBegin(frame.Body)
		if err != nil {
			t.Errorf("unmarshal auth.begin: %v", err)
			return
		}

		challenge := protocol.AuthChallenge{
			ChallengeID: 7,
			ExpiresInMs: 5000,
		}
		copy(challenge.Nonce[:], []byte("nonce-1234567890"))
		challengeBodyValue, err := protocol.MarshalAuthChallenge(challenge)
		if err != nil {
			t.Errorf("marshal auth.challenge: %v", err)
			return
		}
		writeFrame(t, serverConn, protocol.Frame{
			Type:      protocol.TypeAuthChallenge,
			RequestID: frame.RequestID,
			Body:      challengeBodyValue,
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
		if finish.ChallengeID != challenge.ChallengeID {
			t.Errorf("unexpected challenge id: %d", finish.ChallengeID)
			return
		}
		secretHash := sha256.Sum256(credentials.ClientSecret[:])
		expected := protocol.ChallengeResponse(secretHash, challenge.Nonce)
		if finish.Response != expected {
			t.Errorf("unexpected auth response")
			return
		}

		helloBodyValue, err := protocol.MarshalServerHello(protocol.ServerHello{
			HeartbeatIntervalMs: 50,
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
			Body:      helloBodyValue,
		})

		configPushBodyValue, err := protocol.MarshalConfigPush(protocol.ConfigPush{
			ConfigVersion: 99,
			GeneratedAtMs: 1234,
			Tunnels: []protocol.TunnelEntry{
				{
					TunnelID:    1,
					Protocol:    protocol.ProtocolTCP,
					TunnelFlags: protocol.TunnelFlagEnabled,
					RemoteStart: 20000,
					RemoteEnd:   20000,
					LocalHost:   mustHost(t, "127.0.0.1"),
					LocalStart:  22,
					LocalEnd:    22,
				},
			},
		})
		if err != nil {
			t.Errorf("marshal config.push: %v", err)
			return
		}
		writeFrame(t, serverConn, protocol.Frame{
			Type:      protocol.TypeConfigPush,
			RequestID: 2147483648,
			Body:      configPushBodyValue,
		})

		frame = readFrame(t, serverConn)
		if frame.Type != protocol.TypeConfigAck {
			t.Errorf("expected config.ack, got %s", frame.Type.String())
			return
		}

		frame = readFrame(t, serverConn)
		if frame.Type != protocol.TypeHeartbeatPing {
			t.Errorf("expected heartbeat.ping, got %s", frame.Type.String())
			return
		}
		ping, err := protocol.UnmarshalHeartbeatPing(frame.Body)
		if err != nil {
			t.Errorf("unmarshal heartbeat.ping: %v", err)
			return
		}
		if ping.LastAckedConfigVersion != 99 {
			t.Errorf("unexpected last acked config version: %d", ping.LastAckedConfigVersion)
			return
		}

		pongBodyValue, err := protocol.MarshalHeartbeatPong(protocol.HeartbeatPong{
			ClientUnixMs: ping.ClientUnixMs,
			ServerUnixMs: uint64(time.Now().UTC().UnixMilli()),
		})
		if err != nil {
			t.Errorf("marshal heartbeat.pong: %v", err)
			return
		}
		writeFrame(t, serverConn, protocol.Frame{
			Type:      protocol.TypeHeartbeatPong,
			RequestID: frame.RequestID,
			Body:      pongBodyValue,
		})

		if begin.ClientID != credentials.ClientID {
			t.Errorf("unexpected client id")
			return
		}

		cancel()
		<-ctx.Done()
	}()

	if err := client.runSession(ctx, clientConn, credentials); err != nil {
		t.Fatalf("run session: %v", err)
	}

	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("mock server did not exit")
	}
}

func TestClientRunExitsOnTerminalRemoteErrorWithoutReconnect(t *testing.T) {
	credentials := testCredentials(t)
	logBuffer := &bytes.Buffer{}
	client := New(
		testConfig(),
		slog.New(slog.NewTextHandler(logBuffer, &slog.HandlerOptions{Level: slog.LevelDebug})),
		"test-client",
	)

	dialCount := 0
	serverDone := make(chan struct{}, 2)
	client.dialContext = func(_ context.Context, _, _ string) (net.Conn, error) {
		dialCount++
		clientConn, serverConn := net.Pipe()
		go func() {
			defer func() {
				serverDone <- struct{}{}
			}()
			defer serverConn.Close()

			performPlainTransportHello(t, serverConn, credentials.ClientID)

			frame := readFrame(t, serverConn)
			if frame.Type != protocol.TypeAuthBegin {
				t.Errorf("expected auth.begin, got %s", frame.Type.String())
				return
			}

			challenge := protocol.AuthChallenge{
				ChallengeID: 9,
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

			errorBody, err := protocol.MarshalErrorBody(protocol.ErrorBody{
				ErrorCode: protocol.ErrorCodeAuthClientLimitReached,
				Retryable: false,
				Message:   "other frpc already online ip=203.0.113.10:7000",
			})
			if err != nil {
				t.Errorf("marshal error body: %v", err)
				return
			}
			writeFrame(t, serverConn, protocol.Frame{
				Type:      protocol.TypeError,
				RequestID: frame.RequestID,
				Body:      errorBody,
			})
		}()
		return clientConn, nil
	}

	err := client.Run(context.Background())
	var remote *remoteError
	if !errors.As(err, &remote) {
		t.Fatalf("expected remoteError, got %v", err)
	}
	if remote.Code != protocol.ErrorCodeAuthClientLimitReached {
		t.Fatalf("unexpected remote error code: %d", remote.Code)
	}
	if dialCount != 1 {
		t.Fatalf("expected one dial attempt, got %d", dialCount)
	}
	if strings.Contains(logBuffer.String(), "重连 frps 中...") {
		t.Fatalf("did not expect reconnect log, got %q", logBuffer.String())
	}

	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("mock server did not exit")
	}
}

func TestClientHandlesStreamOpenAndData(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo server: %v", err)
	}
	defer listener.Close()

	echoDone := make(chan struct{})
	go func() {
		defer close(echoDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		buffer := make([]byte, 16)
		n, err := conn.Read(buffer)
		if err != nil {
			return
		}
		_, _ = conn.Write(buffer[:n])
	}()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	client := New(testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test-client")

	state := newSessionState(1000)
	state.setSnapshot(protocol.ConfigPush{
		ConfigVersion: 1,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: 20000,
				RemoteEnd:   20000,
				LocalHost:   mustHost(t, "127.0.0.1"),
				LocalStart:  uint16(listener.Addr().(*net.TCPAddr).Port),
				LocalEnd:    uint16(listener.Addr().(*net.TCPAddr).Port),
			},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	readDone := make(chan error, 1)
	go func() {
		readDone <- client.readLoop(ctx, clientConn, state)
	}()

	openBody, err := protocol.MarshalStreamOpen(protocol.StreamOpen{
		TunnelID:   7,
		RemotePort: 20000,
		ClientAddr: protocol.SockAddr{
			IP:   net.ParseIP("203.0.113.10").To4(),
			Port: 54321,
		},
		OpenedAtMs: 1234,
	})
	if err != nil {
		t.Fatalf("marshal stream.open: %v", err)
	}
	writeFrame(t, serverConn, protocol.Frame{
		Type:      protocol.TypeStreamOpen,
		RequestID: 1,
		StreamID:  7,
		Body:      openBody,
	})

	openedFrame := readFrame(t, serverConn)
	if openedFrame.Type != protocol.TypeStreamOpened {
		t.Fatalf("expected stream.opened, got %s", openedFrame.Type.String())
	}
	opened, err := protocol.UnmarshalStreamOpened(openedFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal stream.opened: %v", err)
	}
	if opened.Status != protocol.StatusOK {
		t.Fatalf("unexpected stream.opened status: %#v", opened)
	}

	writeFrame(t, serverConn, protocol.Frame{
		Type:     protocol.TypeStreamData,
		StreamID: 7,
		Body:     []byte("hello"),
	})

	dataFrame := readFrame(t, serverConn)
	if dataFrame.Type != protocol.TypeStreamData {
		t.Fatalf("expected stream.data, got %s", dataFrame.Type.String())
	}
	if string(dataFrame.Body) != "hello" {
		t.Fatalf("unexpected echoed payload: %q", string(dataFrame.Body))
	}

	closeBody, err := protocol.MarshalStreamClose(protocol.StreamClose{
		ReasonCode: protocol.CloseReasonEOF,
		Initiator:  protocol.InitiatorFRPS,
		Message:    "done",
	})
	if err != nil {
		t.Fatalf("marshal stream.close: %v", err)
	}
	writeFrame(t, serverConn, protocol.Frame{
		Type:     protocol.TypeStreamClose,
		StreamID: 7,
		Body:     closeBody,
	})

	cancel()
	_ = clientConn.Close()

	select {
	case err := <-readDone:
		if err != nil && !isNetClosed(err) {
			t.Fatalf("read loop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read loop did not exit")
	}

	select {
	case <-echoDone:
	case <-time.After(2 * time.Second):
		t.Fatal("echo server did not exit")
	}
}

func TestClientRejectsStreamOpenForUnknownTunnel(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	client := New(testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test-client")

	state := newSessionState(1000)
	state.setSnapshot(protocol.ConfigPush{ConfigVersion: 1})

	openBody, err := protocol.MarshalStreamOpen(protocol.StreamOpen{
		TunnelID:   99,
		RemotePort: 20000,
		ClientAddr: protocol.SockAddr{
			IP:   net.ParseIP("203.0.113.10").To4(),
			Port: 54321,
		},
		OpenedAtMs: 1234,
	})
	if err != nil {
		t.Fatalf("marshal stream.open: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.handleStreamOpen(clientConn, state, protocol.Frame{
			Type:      protocol.TypeStreamOpen,
			RequestID: 1,
			StreamID:  7,
			Body:      openBody,
		})
	}()

	openedFrame := readFrame(t, serverConn)
	if openedFrame.Type != protocol.TypeStreamOpened {
		t.Fatalf("expected stream.opened, got %s", openedFrame.Type.String())
	}
	opened, err := protocol.UnmarshalStreamOpened(openedFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal stream.opened: %v", err)
	}
	if opened.Status != protocol.StatusError {
		t.Fatalf("unexpected status: %d", opened.Status)
	}
	if opened.ErrorCode != protocol.ErrorCodeStreamTunnelNotFound {
		t.Fatalf("unexpected error code: %d", opened.ErrorCode)
	}
	if opened.Message != "tunnel 99 not found" {
		t.Fatalf("unexpected error message: %q", opened.Message)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("handle stream.open: %v", err)
	}
	if state.activeStreams.Load() != 0 {
		t.Fatalf("unexpected active stream count: %d", state.activeStreams.Load())
	}
}

func TestSessionStateLocalUDPTargetSupportsSingleAndRange(t *testing.T) {
	state := newSessionState(1000)
	state.setSnapshot(protocol.ConfigPush{
		ConfigVersion: 1,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    9,
				Protocol:    protocol.ProtocolUDP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: 21000,
				RemoteEnd:   21000,
				LocalHost:   mustHost(t, "127.0.0.1"),
				LocalStart:  5300,
				LocalEnd:    5300,
			},
			{
				TunnelID:    10,
				Protocol:    protocol.ProtocolUDP,
				TunnelFlags: protocol.TunnelFlagEnabled | protocol.TunnelFlagRange,
				RemoteStart: 22000,
				RemoteEnd:   22001,
				LocalHost:   mustHost(t, "127.0.0.1"),
				LocalStart:  5400,
				LocalEnd:    5401,
			},
		},
	})

	tests := []struct {
		name string
		open protocol.UDPOpen
		want string
	}{
		{
			name: "single",
			open: protocol.UDPOpen{
				TunnelID:   9,
				RemotePort: 21000,
			},
			want: "127.0.0.1:5300",
		},
		{
			name: "range",
			open: protocol.UDPOpen{
				TunnelID:   10,
				RemotePort: 22001,
			},
			want: "127.0.0.1:5401",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := state.localUDPTarget(tt.open)
			if err != nil {
				t.Fatalf("local udp target: %v", err)
			}
			if got != tt.want {
				t.Fatalf("unexpected local udp target: got %q want %q", got, tt.want)
			}
		})
	}
}

func TestSessionStateLocalTargetSupportsSingleAndRange(t *testing.T) {
	state := newSessionState(1000)
	state.setSnapshot(protocol.ConfigPush{
		ConfigVersion: 1,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: 20000,
				RemoteEnd:   20000,
				LocalHost:   mustHost(t, "127.0.0.1"),
				LocalStart:  5100,
				LocalEnd:    5100,
			},
			{
				TunnelID:    8,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled | protocol.TunnelFlagRange,
				RemoteStart: 20100,
				RemoteEnd:   20101,
				LocalHost:   mustHost(t, "127.0.0.1"),
				LocalStart:  5200,
				LocalEnd:    5201,
			},
		},
	})

	tests := []struct {
		name string
		open protocol.StreamOpen
		want string
	}{
		{
			name: "single",
			open: protocol.StreamOpen{
				TunnelID:   7,
				RemotePort: 20000,
			},
			want: "127.0.0.1:5100",
		},
		{
			name: "range",
			open: protocol.StreamOpen{
				TunnelID:   8,
				RemotePort: 20101,
			},
			want: "127.0.0.1:5201",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := state.localTarget(tt.open)
			if err != nil {
				t.Fatalf("local target: %v", err)
			}
			if got != tt.want {
				t.Fatalf("unexpected local target: got %q want %q", got, tt.want)
			}
		})
	}
}

func TestClientReadLoopHandlesUDPOpenAndClose(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	client := New(testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test-client")

	state := newSessionState(1000)
	state.setSnapshot(protocol.ConfigPush{
		ConfigVersion: 1,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    9,
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	readDone := make(chan error, 1)
	go func() {
		readDone <- client.readLoop(ctx, clientConn, state)
	}()

	openBody, err := protocol.MarshalUDPOpen(protocol.UDPOpen{
		TunnelID:      9,
		RemotePort:    21000,
		ClientAddr:    protocol.SockAddr{IP: net.ParseIP("203.0.113.11").To4(), Port: 40001},
		IdleTimeoutMs: 30000,
	})
	if err != nil {
		t.Fatalf("marshal udp.open: %v", err)
	}
	writeFrame(t, serverConn, protocol.Frame{
		Type:      protocol.TypeUDPOpen,
		RequestID: 7,
		StreamID:  33,
		Body:      openBody,
	})

	deadline := time.Now().Add(time.Second)
	for state.activeUDPSessions.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("udp session was not registered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	closeBody, err := protocol.MarshalUDPClose(protocol.UDPClose{
		ReasonCode: protocol.CloseReasonIdleTimeout,
		Initiator:  protocol.InitiatorFRPS,
		Message:    "cleanup",
	})
	if err != nil {
		t.Fatalf("marshal udp.close: %v", err)
	}
	writeFrame(t, serverConn, protocol.Frame{
		Type:     protocol.TypeUDPClose,
		StreamID: 33,
		Body:     closeBody,
	})

	deadline = time.Now().Add(time.Second)
	for state.activeUDPSessions.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("udp session was not closed")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	_ = clientConn.Close()

	select {
	case err := <-readDone:
		if err != nil && !isNetClosed(err) {
			t.Fatalf("read loop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read loop did not exit")
	}
}

func TestClientReadLoopForwardsUDPDatagramsToMappedRangeLocalService(t *testing.T) {
	localRangeStart := freeUDPPortRange(t, 2)
	localServer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1").To4(), Port: localRangeStart + 1})
	if err != nil {
		t.Fatalf("listen udp range echo server: %v", err)
	}
	defer localServer.Close()

	serverDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, protocol.MaxDataBodyLen)
		_ = localServer.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, addr, err := localServer.ReadFromUDP(buffer)
		if err != nil {
			serverDone <- err
			return
		}
		if string(buffer[:n]) != "ping-range" {
			serverDone <- fmt.Errorf("unexpected udp range request payload: %q", string(buffer[:n]))
			return
		}
		if _, err := localServer.WriteToUDP([]byte("pong-range"), addr); err != nil {
			serverDone <- err
			return
		}
		serverDone <- nil
	}()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	client := New(testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test-client")

	state := newSessionState(1000)
	state.setSnapshot(protocol.ConfigPush{
		ConfigVersion: 1,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    10,
				Protocol:    protocol.ProtocolUDP,
				TunnelFlags: protocol.TunnelFlagEnabled | protocol.TunnelFlagRange,
				RemoteStart: 22000,
				RemoteEnd:   22001,
				LocalHost:   mustHost(t, "127.0.0.1"),
				LocalStart:  uint16(localRangeStart),
				LocalEnd:    uint16(localRangeStart + 1),
			},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	readDone := make(chan error, 1)
	go func() {
		readDone <- client.readLoop(ctx, clientConn, state)
	}()

	openBody, err := protocol.MarshalUDPOpen(protocol.UDPOpen{
		TunnelID:      10,
		RemotePort:    22001,
		ClientAddr:    protocol.SockAddr{IP: net.ParseIP("203.0.113.11").To4(), Port: 40001},
		IdleTimeoutMs: 30000,
	})
	if err != nil {
		t.Fatalf("marshal udp.open: %v", err)
	}
	writeFrame(t, serverConn, protocol.Frame{
		Type:      protocol.TypeUDPOpen,
		RequestID: 7,
		StreamID:  44,
		Body:      openBody,
	})
	writeFrame(t, serverConn, protocol.Frame{
		Type:     protocol.TypeUDPData,
		StreamID: 44,
		Body:     []byte("ping-range"),
	})

	responseFrame := readFrame(t, serverConn)
	if responseFrame.Type != protocol.TypeUDPData {
		t.Fatalf("expected udp.data, got %s", responseFrame.Type.String())
	}
	if responseFrame.StreamID != 44 {
		t.Fatalf("unexpected udp session id: %d", responseFrame.StreamID)
	}
	if string(responseFrame.Body) != "pong-range" {
		t.Fatalf("unexpected udp range response payload: %q", string(responseFrame.Body))
	}

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("udp range local server: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("udp range local server did not exit")
	}

	closeBody, err := protocol.MarshalUDPClose(protocol.UDPClose{
		ReasonCode: protocol.CloseReasonIdleTimeout,
		Initiator:  protocol.InitiatorFRPS,
		Message:    "cleanup",
	})
	if err != nil {
		t.Fatalf("marshal udp.close: %v", err)
	}
	writeFrame(t, serverConn, protocol.Frame{
		Type:     protocol.TypeUDPClose,
		StreamID: 44,
		Body:     closeBody,
	})

	deadline := time.Now().Add(time.Second)
	for state.activeUDPSessions.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("udp session was not closed")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	_ = clientConn.Close()

	select {
	case err := <-readDone:
		if err != nil && !isNetClosed(err) {
			t.Fatalf("read loop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read loop did not exit")
	}
}

func TestClientReadLoopForwardsUDPDatagramsToLocalService(t *testing.T) {
	localServer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1").To4(), Port: 0})
	if err != nil {
		t.Fatalf("listen udp echo server: %v", err)
	}
	defer localServer.Close()

	serverDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, protocol.MaxDataBodyLen)
		_ = localServer.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, addr, err := localServer.ReadFromUDP(buffer)
		if err != nil {
			serverDone <- err
			return
		}
		if string(buffer[:n]) != "ping" {
			serverDone <- fmt.Errorf("unexpected udp request payload: %q", string(buffer[:n]))
			return
		}
		if _, err := localServer.WriteToUDP([]byte("pong"), addr); err != nil {
			serverDone <- err
			return
		}
		serverDone <- nil
	}()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	client := New(testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test-client")

	localPort := uint16(localServer.LocalAddr().(*net.UDPAddr).Port)
	state := newSessionState(1000)
	state.setSnapshot(protocol.ConfigPush{
		ConfigVersion: 1,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    9,
				Protocol:    protocol.ProtocolUDP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: 21000,
				RemoteEnd:   21000,
				LocalHost:   mustHost(t, "127.0.0.1"),
				LocalStart:  localPort,
				LocalEnd:    localPort,
			},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	readDone := make(chan error, 1)
	go func() {
		readDone <- client.readLoop(ctx, clientConn, state)
	}()

	openBody, err := protocol.MarshalUDPOpen(protocol.UDPOpen{
		TunnelID:      9,
		RemotePort:    21000,
		ClientAddr:    protocol.SockAddr{IP: net.ParseIP("203.0.113.11").To4(), Port: 40001},
		IdleTimeoutMs: 30000,
	})
	if err != nil {
		t.Fatalf("marshal udp.open: %v", err)
	}
	writeFrame(t, serverConn, protocol.Frame{
		Type:      protocol.TypeUDPOpen,
		RequestID: 7,
		StreamID:  33,
		Body:      openBody,
	})
	writeFrame(t, serverConn, protocol.Frame{
		Type:     protocol.TypeUDPData,
		StreamID: 33,
		Body:     []byte("ping"),
	})

	responseFrame := readFrame(t, serverConn)
	if responseFrame.Type != protocol.TypeUDPData {
		t.Fatalf("expected udp.data, got %s", responseFrame.Type.String())
	}
	if responseFrame.StreamID != 33 {
		t.Fatalf("unexpected udp session id: %d", responseFrame.StreamID)
	}
	if string(responseFrame.Body) != "pong" {
		t.Fatalf("unexpected udp response payload: %q", string(responseFrame.Body))
	}

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("udp local server: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("udp local server did not exit")
	}

	closeBody, err := protocol.MarshalUDPClose(protocol.UDPClose{
		ReasonCode: protocol.CloseReasonIdleTimeout,
		Initiator:  protocol.InitiatorFRPS,
		Message:    "cleanup",
	})
	if err != nil {
		t.Fatalf("marshal udp.close: %v", err)
	}
	writeFrame(t, serverConn, protocol.Frame{
		Type:     protocol.TypeUDPClose,
		StreamID: 33,
		Body:     closeBody,
	})

	deadline := time.Now().Add(time.Second)
	for state.activeUDPSessions.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("udp session was not closed")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	_ = clientConn.Close()

	select {
	case err := <-readDone:
		if err != nil && !isNetClosed(err) {
			t.Fatalf("read loop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read loop did not exit")
	}
}

func TestClientRejectsUDPDataForUnknownSession(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	client := New(testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test-client")

	state := newSessionState(1000)

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.handleUDPData(clientConn, state, protocol.Frame{
			Type:     protocol.TypeUDPData,
			StreamID: 55,
			Body:     []byte("hello"),
		})
	}()

	closeFrame := readFrame(t, serverConn)
	if closeFrame.Type != protocol.TypeUDPClose {
		t.Fatalf("expected udp.close, got %s", closeFrame.Type.String())
	}
	closeMessage, err := protocol.UnmarshalUDPClose(closeFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal udp.close: %v", err)
	}
	if closeMessage.ReasonCode != protocol.CloseReasonProtocolError || closeMessage.Message != "udp session not found" {
		t.Fatalf("unexpected udp.close: %#v", closeMessage)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("handle udp.data: %v", err)
	}
}

func writeFrame(t *testing.T, conn net.Conn, frame protocol.Frame) {
	t.Helper()

	frameBytes, err := frame.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	if err := transport.WriteFrame(conn, frameBytes, time.Second); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func performPlainTransportHello(t *testing.T, conn net.Conn, clientID [16]byte) {
	t.Helper()

	frame := readFrame(t, conn)
	if frame.Type != protocol.TypeTransportClientHello {
		t.Fatalf("expected transport.client_hello, got %s", frame.Type.String())
	}

	hello, err := protocol.UnmarshalTransportClientHello(frame.Body)
	if err != nil {
		t.Fatalf("unmarshal transport.client_hello: %v", err)
	}
	if hello.ClientID != clientID {
		t.Fatal("unexpected client id in transport.client_hello")
	}
	if hello.SupportedSecurityModes&protocol.TransportSecurityModePlain == 0 {
		t.Fatal("expected client to support plain transport")
	}

	body, err := protocol.MarshalTransportServerHello(protocol.TransportServerHello{
		SelectedSecurityMode: protocol.TransportSecurityModePlain,
		CapabilityBits:       0,
	})
	if err != nil {
		t.Fatalf("marshal transport.server_hello: %v", err)
	}
	writeFrame(t, conn, protocol.Frame{
		Type:      protocol.TypeTransportServerHello,
		RequestID: frame.RequestID,
		Body:      body,
	})
}

func readFrame(t *testing.T, conn net.Conn) protocol.Frame {
	t.Helper()

	frameBytes, err := transport.ReadFrame(conn, time.Second)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	frame, err := protocol.ParseFrame(frameBytes)
	if err != nil {
		t.Fatalf("parse frame: %v", err)
	}
	return frame
}

func mustHost(t *testing.T, value string) protocol.Host {
	t.Helper()

	host, err := protocol.ParseHost(value)
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}
	return host
}

func isNetClosed(err error) bool {
	if err == nil {
		return false
	}
	return err.Error() == "io: read/write on closed pipe"
}

func freeUDPPortRange(t *testing.T, size int) int {
	t.Helper()

	if size <= 0 {
		t.Fatal("udp port range size must be positive")
	}

	tryRange := func(start int) bool {
		listeners := make([]*net.UDPConn, 0, size)
		for offset := 0; offset < size; offset++ {
			listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: start + offset})
			if err != nil {
				for _, started := range listeners {
					_ = started.Close()
				}
				return false
			}
			listeners = append(listeners, listener)
		}
		for _, listener := range listeners {
			_ = listener.Close()
		}
		return true
	}

	seedListener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen for free udp port: %v", err)
	}
	seed := seedListener.LocalAddr().(*net.UDPAddr).Port
	_ = seedListener.Close()

	maxStart := 65535 - size + 1
	for start := seed; start <= maxStart; start++ {
		if tryRange(start) {
			return start
		}
	}
	for start := 1024; start < seed; start++ {
		if tryRange(start) {
			return start
		}
	}

	t.Fatalf("failed to allocate contiguous udp port range of size %d", size)
	return 0
}
