package client

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	appconfig "github.com/zightch/frp/frpc/internal/config"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestClientRunSessionUsesLoginSnapshotForFirstStream(t *testing.T) {
	tokenValue := "00112233445566778899aabbccddeeff0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	token, err := appconfig.ParseToken(tokenValue)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}

	loginTarget, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen login target: %v", err)
	}
	defer loginTarget.Close()

	targetDone := make(chan error, 1)
	go func() {
		conn, err := loginTarget.Accept()
		if err != nil {
			targetDone <- err
			return
		}
		defer conn.Close()

		buffer := make([]byte, 16)
		n, err := conn.Read(buffer)
		if err != nil {
			targetDone <- err
			return
		}
		if string(buffer[:n]) != "ping" {
			targetDone <- io.ErrUnexpectedEOF
			return
		}
		if _, err := conn.Write([]byte("login-snapshot")); err != nil {
			targetDone <- err
			return
		}
		targetDone <- nil
	}()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	client := New(
		appconfig.Config{
			Server: "127.0.0.1:7000",
			Token:  tokenValue,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-client",
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		defer serverConn.Close()

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
		tokenHash := sha256.Sum256(token.Secret[:])
		expected := protocol.ChallengeResponse(tokenHash, challenge.Nonce)
		if finish.Response != expected {
			t.Errorf("unexpected auth response")
			return
		}

		helloBody, err := protocol.MarshalServerHello(protocol.ServerHello{
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
			Body:      helloBody,
		})

		configPushBody, err := protocol.MarshalConfigPush(protocol.ConfigPush{
			ConfigVersion: 2,
			GeneratedAtMs: 5678,
			Tunnels: []protocol.TunnelEntry{
				{
					TunnelID:    7,
					Protocol:    protocol.ProtocolTCP,
					TunnelFlags: protocol.TunnelFlagEnabled,
					RemoteStart: 20000,
					RemoteEnd:   20000,
					LocalHost:   mustHost(t, "127.0.0.1"),
					LocalStart:  uint16(loginTarget.Addr().(*net.TCPAddr).Port),
					LocalEnd:    uint16(loginTarget.Addr().(*net.TCPAddr).Port),
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
			Body:      configPushBody,
		})

		ackFrame := readFrame(t, serverConn)
		if ackFrame.Type != protocol.TypeConfigAck {
			t.Errorf("expected config.ack, got %s", ackFrame.Type.String())
			return
		}
		ack, err := protocol.UnmarshalConfigAck(ackFrame.Body)
		if err != nil {
			t.Errorf("unmarshal config.ack: %v", err)
			return
		}
		if ack.ConfigVersion != 2 || ack.Status != protocol.StatusOK {
			t.Errorf("unexpected config.ack: %#v", ack)
			return
		}

		openBody, err := protocol.MarshalStreamOpen(protocol.StreamOpen{
			TunnelID:   7,
			RemotePort: 20000,
			ClientAddr: protocol.SockAddr{
				IP:   net.ParseIP("203.0.113.10").To4(),
				Port: 45678,
			},
			OpenedAtMs: 9999,
		})
		if err != nil {
			t.Errorf("marshal stream.open: %v", err)
			return
		}
		writeFrame(t, serverConn, protocol.Frame{
			Type:      protocol.TypeStreamOpen,
			RequestID: 4,
			StreamID:  70,
			Body:      openBody,
		})

		openedFrame := readFrame(t, serverConn)
		if openedFrame.Type != protocol.TypeStreamOpened {
			t.Errorf("expected stream.opened, got %s", openedFrame.Type.String())
			return
		}
		opened, err := protocol.UnmarshalStreamOpened(openedFrame.Body)
		if err != nil {
			t.Errorf("unmarshal stream.opened: %v", err)
			return
		}
		if opened.Status != protocol.StatusOK {
			t.Errorf("unexpected stream.opened: %#v", opened)
			return
		}

		writeFrame(t, serverConn, protocol.Frame{
			Type:     protocol.TypeStreamData,
			StreamID: 70,
			Body:     []byte("ping"),
		})

		dataFrame := readFrame(t, serverConn)
		if dataFrame.Type != protocol.TypeStreamData {
			t.Errorf("expected stream.data, got %s", dataFrame.Type.String())
			return
		}
		if string(dataFrame.Body) != "login-snapshot" {
			t.Errorf("unexpected payload from login snapshot target: %q", string(dataFrame.Body))
			return
		}

		closeFrame := readFrame(t, serverConn)
		if closeFrame.Type != protocol.TypeStreamClose {
			t.Errorf("expected stream.close, got %s", closeFrame.Type.String())
			return
		}

		cancel()
		<-ctx.Done()
	}()

	if err := client.runSession(ctx, clientConn, token); err != nil {
		t.Fatalf("run session: %v", err)
	}

	select {
	case err := <-targetDone:
		if err != nil {
			t.Fatalf("login snapshot target: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("login snapshot target did not finish")
	}

	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("mock server did not exit")
	}
}
