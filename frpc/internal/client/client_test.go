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
	"github.com/zightch/frp/frps/pkg/transport"
)

func TestClientRunSession(t *testing.T) {
	tokenValue := "00112233445566778899aabbccddeeff0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	token, err := appconfig.ParseToken(tokenValue)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}

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
		tokenHash := sha256.Sum256(token.Secret[:])
		expected := authResponse(tokenHash, challenge.Nonce)
		if finish.Response != expected {
			t.Errorf("unexpected auth response")
			return
		}

		helloBodyValue, err := protocol.MarshalServerHello(protocol.ServerHello{
			HeartbeatIntervalMs: 50,
			SessionID:           11,
			ServerVersion:       "test-server",
			MinSupportedVersion: "test-client",
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

		if begin.TokenID != token.ID {
			t.Errorf("unexpected token id")
			return
		}

		cancel()
		<-ctx.Done()
	}()

	if err := client.runSession(ctx, clientConn, token); err != nil {
		t.Fatalf("run session: %v", err)
	}

	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("mock server did not exit")
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
