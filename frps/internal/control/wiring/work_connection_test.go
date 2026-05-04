package wiring

import (
	"crypto/sha256"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestServerAcceptsTCPWorkConnection(t *testing.T) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	server := NewServer(
		Options{
			Repository: stubRepository{
				group: GroupRuntime{
					ID:               1,
					Name:             "group-a",
					Enabled:          true,
					EffectiveIP:      system.AnyIPv4,
					ClientSecretHash: tokenHash,
					Snapshot: ConfigSnapshot{
						Version:       99,
						GeneratedAtMs: 1234,
						Tunnels: []protocol.TunnelEntry{
							{
								TunnelID:    7,
								Protocol:    protocol.ProtocolTCP,
								TunnelFlags: protocol.TunnelFlagEnabled,
								RemoteStart: 20000,
								RemoteEnd:   20000,
								LocalHost:   host,
								LocalStart:  22,
								LocalEnd:    22,
							},
						},
					},
				},
			},
			ReadTimeout:       time.Second,
			WriteTimeout:      time.Second,
			ChallengeTTL:      5 * time.Second,
			HeartbeatInterval: 2 * time.Second,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	controlConn, controlDone, hello, _ := loginControlSessionForTCPWorkTest(t, server, tokenID, tokenHash)
	defer func() {
		_ = controlConn.Close()
	}()

	clientConn, workDone := openTCPWorkConnForTest(t, server, hello)
	defer clientConn.Close()

	waitForTCPWorkCounts(t, server, hello.SessionID, 1, 0)

	workConn, ok := server.acquireTCPWorkConn(hello.SessionID)
	if !ok || workConn == nil {
		t.Fatal("expected tcp work connection to be acquired")
	}
	waitForTCPWorkCounts(t, server, hello.SessionID, 0, 1)

	if !server.releaseTCPWorkConn(hello.SessionID, workConn) {
		t.Fatal("expected tcp work connection to be released")
	}
	waitForTCPWorkCounts(t, server, hello.SessionID, 1, 0)

	workConn, ok = server.acquireTCPWorkConn(hello.SessionID)
	if !ok || workConn == nil {
		t.Fatal("expected tcp work connection to be re-acquired")
	}
	waitForTCPWorkCounts(t, server, hello.SessionID, 0, 1)

	if !server.retireTCPWorkConn(hello.SessionID, workConn) {
		t.Fatal("expected tcp work connection to be retired")
	}
	waitForTCPWorkCounts(t, server, hello.SessionID, 0, 0)

	select {
	case <-workDone:
	case <-time.After(2 * time.Second):
		t.Fatal("tcp work connection did not exit")
	}

	_ = controlConn.Close()
	select {
	case <-controlDone:
	case <-time.After(2 * time.Second):
		t.Fatal("control connection did not exit")
	}
}

func TestServerRejectsTCPWorkConnectionWhenSessionMissing(t *testing.T) {
	server := NewServer(
		Options{
			Repository:        stubRepository{},
			ReadTimeout:       time.Second,
			WriteTimeout:      time.Second,
			ChallengeTTL:      5 * time.Second,
			HeartbeatInterval: 2 * time.Second,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientRaw, serverRaw := net.Pipe()
	clientConn := &connWithRemoteAddr{
		Conn:   clientRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10003},
	}
	serverConn := &connWithRemoteAddr{
		Conn:   serverRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 20003},
	}
	defer clientConn.Close()

	done := make(chan struct{})
	server.registerConn(serverConn)
	server.connWG.Add(1)
	go func() {
		defer close(done)
		server.handleConnection(serverConn)
	}()

	workHelloBody, err := protocol.MarshalTCPWorkHello(protocol.TCPWorkHello{
		SessionID:              999,
		SupportedSecurityModes: protocol.TransportSecurityModePlain,
	})
	if err != nil {
		t.Fatalf("marshal tcp.work.hello: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeTCPWorkHello,
		RequestID: 1,
		Body:      workHelloBody,
	})

	errorFrame := readMessage(t, clientConn)
	if errorFrame.Type != protocol.TypeError {
		t.Fatalf("expected error frame, got %s", errorFrame.Type.String())
	}
	errorBody, err := protocol.UnmarshalErrorBody(errorFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if errorBody.ErrorCode != protocol.ErrorCodeAuthSessionNotFound {
		t.Fatalf("unexpected error code: got %d want %d", errorBody.ErrorCode, protocol.ErrorCodeAuthSessionNotFound)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerShutdownSessionClosesBusyTCPWorkConnection(t *testing.T) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	server := NewServer(
		Options{
			Repository: stubRepository{
				group: GroupRuntime{
					ID:               1,
					Name:             "group-a",
					Enabled:          true,
					EffectiveIP:      system.AnyIPv4,
					ClientSecretHash: tokenHash,
					Snapshot: ConfigSnapshot{
						Version:       99,
						GeneratedAtMs: 1234,
						Tunnels: []protocol.TunnelEntry{
							{
								TunnelID:    7,
								Protocol:    protocol.ProtocolTCP,
								TunnelFlags: protocol.TunnelFlagEnabled,
								RemoteStart: 20000,
								RemoteEnd:   20000,
								LocalHost:   host,
								LocalStart:  22,
								LocalEnd:    22,
							},
						},
					},
				},
			},
			ReadTimeout:       time.Second,
			WriteTimeout:      time.Second,
			ChallengeTTL:      5 * time.Second,
			HeartbeatInterval: 2 * time.Second,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	controlConn, controlDone, hello, _ := loginControlSessionForTCPWorkTest(t, server, tokenID, tokenHash)
	defer func() {
		_ = controlConn.Close()
	}()

	clientConn, workDone := openTCPWorkConnForTest(t, server, hello)
	defer clientConn.Close()

	waitForTCPWorkCounts(t, server, hello.SessionID, 1, 0)

	workConn, ok := server.acquireTCPWorkConn(hello.SessionID)
	if !ok || workConn == nil {
		t.Fatal("expected tcp work connection to be acquired")
	}
	waitForTCPWorkCounts(t, server, hello.SessionID, 0, 1)

	runtime := server.runtimeExecutor(hello.SessionID)
	if runtime == nil || runtime.session == nil {
		t.Fatal("expected runtime session")
	}
	server.shutdownSession(runtime.session)
	waitForTCPWorkCounts(t, server, hello.SessionID, 0, 0)

	select {
	case <-workDone:
	case <-time.After(2 * time.Second):
		t.Fatal("tcp work connection did not exit")
	}

	_ = controlConn.Close()
	select {
	case <-controlDone:
	case <-time.After(2 * time.Second):
		t.Fatal("control connection did not exit")
	}
}

func loginControlSessionForTCPWorkTest(t *testing.T, server *Server, tokenID [16]byte, tokenHash [32]byte) (*connWithRemoteAddr, chan struct{}, protocol.ServerHello, protocol.Frame) {
	t.Helper()

	clientRaw, serverRaw := net.Pipe()
	clientConn := &connWithRemoteAddr{
		Conn:   clientRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10001},
	}
	serverConn := &connWithRemoteAddr{
		Conn:   serverRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 20001},
	}

	done := make(chan struct{})
	server.registerConn(serverConn)
	server.connWG.Add(1)
	go func() {
		defer close(done)
		server.handleConnection(serverConn)
	}()

	performTransportHello(t, clientConn, tokenID)

	authBeginBody, err := protocol.MarshalAuthBegin(protocol.AuthBegin{
		ClientID:      tokenID,
		ClientVersion: "test-client",
		Hostname:      "node-1",
		OS:            protocol.OSLinux,
		Arch:          protocol.ArchAMD64,
	})
	if err != nil {
		t.Fatalf("marshal auth.begin: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeAuthBegin,
		RequestID: 2,
		Body:      authBeginBody,
	})

	challengeFrame := readMessage(t, clientConn)
	if challengeFrame.Type != protocol.TypeAuthChallenge {
		t.Fatalf("expected auth.challenge, got %s", challengeFrame.Type.String())
	}
	challenge, err := protocol.UnmarshalAuthChallenge(challengeFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal auth.challenge: %v", err)
	}

	authFinishBody, err := protocol.MarshalAuthFinish(protocol.AuthFinish{
		ChallengeID: challenge.ChallengeID,
		Response:    protocol.ChallengeResponse(tokenHash, challenge.Nonce),
	})
	if err != nil {
		t.Fatalf("marshal auth.finish: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeAuthFinish,
		RequestID: 3,
		Body:      authFinishBody,
	})

	helloFrame := readMessage(t, clientConn)
	if helloFrame.Type != protocol.TypeServerHello {
		t.Fatalf("expected server.hello, got %s", helloFrame.Type.String())
	}
	hello, err := protocol.UnmarshalServerHello(helloFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal server.hello: %v", err)
	}
	if hello.TCPWorkPoolSize == 0 {
		t.Fatal("expected non-zero tcp work pool size")
	}

	configFrame := readMessage(t, clientConn)
	if configFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected config.push, got %s", configFrame.Type.String())
	}

	return clientConn, done, hello, configFrame
}

func openTCPWorkConnForTest(t *testing.T, server *Server, hello protocol.ServerHello) (*connWithRemoteAddr, chan struct{}) {
	t.Helper()

	clientRaw, serverRaw := net.Pipe()
	clientConn := &connWithRemoteAddr{
		Conn:   clientRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10002},
	}
	serverConn := &connWithRemoteAddr{
		Conn:   serverRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 20002},
	}

	workDone := make(chan struct{})
	server.registerConn(serverConn)
	server.connWG.Add(1)
	go func() {
		defer close(workDone)
		server.handleConnection(serverConn)
	}()

	workHelloBody, err := protocol.MarshalTCPWorkHello(protocol.TCPWorkHello{
		SessionID:              hello.SessionID,
		SupportedSecurityModes: protocol.TransportSecurityModePlain | protocol.TransportSecurityModeTLS,
	})
	if err != nil {
		t.Fatalf("marshal tcp.work.hello: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeTCPWorkHello,
		RequestID: 1,
		Body:      workHelloBody,
	})

	serverHelloFrame := readMessage(t, clientConn)
	if serverHelloFrame.Type != protocol.TypeTCPWorkServerHello || serverHelloFrame.RequestID != 1 {
		t.Fatalf("unexpected tcp.work.server_hello frame: %#v", serverHelloFrame)
	}
	serverHello, err := protocol.UnmarshalTCPWorkServerHello(serverHelloFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal tcp.work.server_hello: %v", err)
	}
	if serverHello.SelectedSecurityMode != protocol.TransportSecurityModePlain {
		t.Fatalf("unexpected tcp work transport mode: %d", serverHello.SelectedSecurityMode)
	}

	registerBody, err := protocol.MarshalTCPWorkRegister(protocol.TCPWorkRegister{
		WorkSecret: hello.TCPWorkSecret,
	})
	if err != nil {
		t.Fatalf("marshal tcp.work.register: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeTCPWorkRegister,
		RequestID: 2,
		Body:      registerBody,
	})

	readyFrame := readMessage(t, clientConn)
	if readyFrame.Type != protocol.TypeTCPWorkReady || readyFrame.RequestID != 2 {
		t.Fatalf("unexpected tcp.work.ready frame: %#v", readyFrame)
	}
	if _, err := protocol.UnmarshalTCPWorkReady(readyFrame.Body); err != nil {
		t.Fatalf("unmarshal tcp.work.ready: %v", err)
	}

	return clientConn, workDone
}

func waitForTCPWorkCounts(t *testing.T, server *Server, sessionID uint64, wantIdle int, wantBusy int) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for {
		idle, busy, ok := server.tcpWorkConnCounts(sessionID)
		if ok && idle == wantIdle && busy == wantBusy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for tcp work counts idle=%d busy=%d, got idle=%d busy=%d ok=%v", wantIdle, wantBusy, idle, busy, ok)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
