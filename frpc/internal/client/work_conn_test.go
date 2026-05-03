package client

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestClientLoginStoresTCPWorkConfig(t *testing.T) {
	credentials := testCredentials(t)

	var workSecret [32]byte
	copy(workSecret[:], []byte("0123456789abcdef0123456789abcdef"))

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	client := New(testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test-client")

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
		expected := protocol.ChallengeResponse(secretHash, challenge.Nonce)
		if finish.Response != expected {
			t.Error("unexpected auth response")
			return
		}

		helloBody, err := protocol.MarshalServerHello(protocol.ServerHello{
			HeartbeatIntervalMs: 50,
			SessionID:           11,
			ServerVersion:       "test-server",
			TCPWorkPoolSize:     8,
			TCPWorkSecret:       workSecret,
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
			ConfigVersion: 99,
			GeneratedAtMs: 1234,
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

		frame = readFrame(t, serverConn)
		if frame.Type != protocol.TypeConfigAck {
			t.Errorf("expected config.ack, got %s", frame.Type.String())
		}
	}()

	conn, state, err := client.login(clientConn, credentials)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer conn.Close()

	sessionID, poolTarget, gotSecret := state.tcpWorkConfig()
	if sessionID != 11 {
		t.Fatalf("unexpected session id: got %d want 11", sessionID)
	}
	if poolTarget != 8 {
		t.Fatalf("unexpected tcp work pool target: got %d want 8", poolTarget)
	}
	if gotSecret != workSecret {
		t.Fatal("unexpected tcp work secret")
	}

	<-serverDone
}

func TestClientOpenTCPWorkConn(t *testing.T) {
	var workSecret [32]byte
	copy(workSecret[:], []byte("0123456789abcdef0123456789abcdef"))

	client := New(testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test-client")

	serverDone := make(chan struct{}, 1)
	client.dialContext = func(_ context.Context, _, _ string) (net.Conn, error) {
		clientConn, serverConn := net.Pipe()
		go func() {
			defer func() {
				serverDone <- struct{}{}
			}()
			defer serverConn.Close()

			frame := readFrame(t, serverConn)
			if frame.Type != protocol.TypeTCPWorkHello || frame.RequestID != tcpWorkHelloRequestID {
				t.Errorf("unexpected tcp.work.hello frame: %#v", frame)
				return
			}
			hello, err := protocol.UnmarshalTCPWorkHello(frame.Body)
			if err != nil {
				t.Errorf("unmarshal tcp.work.hello: %v", err)
				return
			}
			if hello.SessionID != 11 {
				t.Errorf("unexpected tcp work session id: %d", hello.SessionID)
				return
			}

			serverHelloBody, err := protocol.MarshalTCPWorkServerHello(protocol.TCPWorkServerHello{
				SelectedSecurityMode: protocol.TransportSecurityModePlain,
			})
			if err != nil {
				t.Errorf("marshal tcp.work.server_hello: %v", err)
				return
			}
			writeFrame(t, serverConn, protocol.Frame{
				Type:      protocol.TypeTCPWorkServerHello,
				RequestID: frame.RequestID,
				Body:      serverHelloBody,
			})

			frame = readFrame(t, serverConn)
			if frame.Type != protocol.TypeTCPWorkRegister || frame.RequestID != tcpWorkRegisterRequestID {
				t.Errorf("unexpected tcp.work.register frame: %#v", frame)
				return
			}
			register, err := protocol.UnmarshalTCPWorkRegister(frame.Body)
			if err != nil {
				t.Errorf("unmarshal tcp.work.register: %v", err)
				return
			}
			if register.WorkSecret != workSecret {
				t.Error("unexpected tcp work secret")
				return
			}

			readyBody, err := protocol.MarshalTCPWorkReady(protocol.TCPWorkReady{})
			if err != nil {
				t.Errorf("marshal tcp.work.ready: %v", err)
				return
			}
			writeFrame(t, serverConn, protocol.Frame{
				Type:      protocol.TypeTCPWorkReady,
				RequestID: frame.RequestID,
				Body:      readyBody,
			})

			buffer := make([]byte, 1)
			_, _ = serverConn.Read(buffer)
		}()
		return clientConn, nil
	}

	state := newSessionState(1000)
	state.setIdentity(11, "conn-1")
	state.setTCPWorkConfig(8, workSecret)

	conn, err := client.openTCPWorkConn(context.Background(), state)
	if err != nil {
		t.Fatalf("open tcp work conn: %v", err)
	}
	_ = conn.Close()

	<-serverDone
}
