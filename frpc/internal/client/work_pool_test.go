package client

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	appconfig "github.com/zightch/frp/frpc/internal/config"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type workConnExpectation struct {
	SessionID       uint64
	WorkSecret      [32]byte
	CloseAfterReady bool
}

func TestClientRunSessionMaintainsTCPWorkPool(t *testing.T) {
	credentials := testCredentials(t)
	var workSecret [32]byte
	copy(workSecret[:], []byte("0123456789abcdef0123456789abcdef"))

	controlClientConn, controlServerConn := net.Pipe()
	defer controlClientConn.Close()

	client := New(testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test-client")

	readyCh := make(chan int, 4)
	var workWG sync.WaitGroup
	client.dialContext = workConnDialer(t, &workWG, readyCh, nil, []workConnExpectation{
		{SessionID: 11, WorkSecret: workSecret},
		{SessionID: 11, WorkSecret: workSecret},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	controlDone := make(chan struct{})
	go func() {
		defer close(controlDone)
		defer controlServerConn.Close()
		serveControlSession(t, controlServerConn, credentials, 11, workSecret, 2)
		<-ctx.Done()
	}()

	runDone := make(chan error, 1)
	go func() {
		runDone <- client.runSession(ctx, controlClientConn, credentials)
	}()

	waitForWorkReadySet(t, readyCh, 1, 2)
	waitForTCPWorkPoolCount(t, client, 2)

	cancel()

	if err := <-runDone; err != nil {
		t.Fatalf("run session: %v", err)
	}
	waitForWaitGroup(t, &workWG)
	select {
	case <-controlDone:
	case <-time.After(2 * time.Second):
		t.Fatal("control server did not exit")
	}
}

func TestClientRunSessionReplenishesClosedTCPWorkConn(t *testing.T) {
	credentials := testCredentials(t)
	var workSecret [32]byte
	copy(workSecret[:], []byte("0123456789abcdef0123456789abcdef"))

	controlClientConn, controlServerConn := net.Pipe()
	defer controlClientConn.Close()

	client := New(testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test-client")

	readyCh := make(chan int, 4)
	var workWG sync.WaitGroup
	client.dialContext = workConnDialer(t, &workWG, readyCh, nil, []workConnExpectation{
		{SessionID: 11, WorkSecret: workSecret, CloseAfterReady: true},
		{SessionID: 11, WorkSecret: workSecret},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	controlDone := make(chan struct{})
	go func() {
		defer close(controlDone)
		defer controlServerConn.Close()
		serveControlSession(t, controlServerConn, credentials, 11, workSecret, 1)
		<-ctx.Done()
	}()

	runDone := make(chan error, 1)
	go func() {
		runDone <- client.runSession(ctx, controlClientConn, credentials)
	}()

	waitForWorkReadySet(t, readyCh, 1, 2)
	waitForTCPWorkPoolCount(t, client, 1)

	cancel()

	if err := <-runDone; err != nil {
		t.Fatalf("run session: %v", err)
	}
	waitForWaitGroup(t, &workWG)
	select {
	case <-controlDone:
	case <-time.After(2 * time.Second):
		t.Fatal("control server did not exit")
	}
}

func TestClientRunSessionRebuildsTCPWorkPoolAcrossSessions(t *testing.T) {
	credentials := testCredentials(t)
	var firstSecret [32]byte
	copy(firstSecret[:], []byte("0123456789abcdef0123456789abcdea"))
	var secondSecret [32]byte
	copy(secondSecret[:], []byte("0123456789abcdef0123456789abcdef"))

	client := New(testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test-client")

	readyCh := make(chan int, 4)
	closedCh := make(chan int, 4)
	var workWG sync.WaitGroup
	client.dialContext = workConnDialer(t, &workWG, readyCh, closedCh, []workConnExpectation{
		{SessionID: 11, WorkSecret: firstSecret},
		{SessionID: 22, WorkSecret: secondSecret},
	})

	firstControlClient, firstControlServer := net.Pipe()
	firstCtx, firstCancel := context.WithCancel(context.Background())
	firstControlDone := make(chan struct{})
	go func() {
		defer close(firstControlDone)
		defer firstControlServer.Close()
		serveControlSession(t, firstControlServer, credentials, 11, firstSecret, 1)
		<-firstCtx.Done()
	}()

	firstRunDone := make(chan error, 1)
	go func() {
		firstRunDone <- client.runSession(firstCtx, firstControlClient, credentials)
	}()

	waitForWorkReady(t, readyCh, 1)
	waitForTCPWorkPoolCount(t, client, 1)
	firstCancel()

	if err := <-firstRunDone; err != nil {
		t.Fatalf("first run session: %v", err)
	}
	waitForWorkClosed(t, closedCh, 1)
	select {
	case <-firstControlDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first control server did not exit")
	}

	secondControlClient, secondControlServer := net.Pipe()
	secondCtx, secondCancel := context.WithCancel(context.Background())
	secondControlDone := make(chan struct{})
	go func() {
		defer close(secondControlDone)
		defer secondControlServer.Close()
		serveControlSession(t, secondControlServer, credentials, 22, secondSecret, 1)
		<-secondCtx.Done()
	}()

	secondRunDone := make(chan error, 1)
	go func() {
		secondRunDone <- client.runSession(secondCtx, secondControlClient, credentials)
	}()

	waitForWorkReady(t, readyCh, 2)
	waitForTCPWorkPoolCount(t, client, 1)
	secondCancel()

	if err := <-secondRunDone; err != nil {
		t.Fatalf("second run session: %v", err)
	}
	waitForWorkClosed(t, closedCh, 2)
	select {
	case <-secondControlDone:
	case <-time.After(2 * time.Second):
		t.Fatal("second control server did not exit")
	}

	waitForWaitGroup(t, &workWG)
}

func serveControlSession(t *testing.T, conn net.Conn, credentials appconfig.Credentials, sessionID uint64, workSecret [32]byte, workPoolTarget uint16) {
	t.Helper()

	performPlainTransportHello(t, conn, credentials.ClientID)

	frame := readFrame(t, conn)
	if frame.Type != protocol.TypeAuthBegin {
		t.Fatalf("expected auth.begin, got %s", frame.Type.String())
	}

	challenge := protocol.AuthChallenge{
		ChallengeID: 7,
		ExpiresInMs: 5000,
	}
	copy(challenge.Nonce[:], []byte("nonce-1234567890"))
	challengeBody, err := protocol.MarshalAuthChallenge(challenge)
	if err != nil {
		t.Fatalf("marshal auth.challenge: %v", err)
	}
	writeFrame(t, conn, protocol.Frame{
		Type:      protocol.TypeAuthChallenge,
		RequestID: frame.RequestID,
		Body:      challengeBody,
	})

	frame = readFrame(t, conn)
	if frame.Type != protocol.TypeAuthFinish {
		t.Fatalf("expected auth.finish, got %s", frame.Type.String())
	}
	finish, err := protocol.UnmarshalAuthFinish(frame.Body)
	if err != nil {
		t.Fatalf("unmarshal auth.finish: %v", err)
	}
	secretHash := sha256.Sum256(credentials.ClientSecret[:])
	expected := protocol.ChallengeResponse(secretHash, challenge.Nonce)
	if finish.Response != expected {
		t.Fatal("unexpected auth response")
	}

	helloBody, err := protocol.MarshalServerHello(protocol.ServerHello{
		HeartbeatIntervalMs: 5000,
		SessionID:           sessionID,
		ServerVersion:       "test-server",
		TCPWorkPoolSize:     workPoolTarget,
		TCPWorkSecret:       workSecret,
	})
	if err != nil {
		t.Fatalf("marshal server.hello: %v", err)
	}
	writeFrame(t, conn, protocol.Frame{
		Type:      protocol.TypeServerHello,
		RequestID: frame.RequestID,
		Body:      helloBody,
	})

	configPushBody, err := protocol.MarshalConfigPush(protocol.ConfigPush{
		ConfigVersion: 99,
		GeneratedAtMs: 1234,
	})
	if err != nil {
		t.Fatalf("marshal config.push: %v", err)
	}
	writeFrame(t, conn, protocol.Frame{
		Type:      protocol.TypeConfigPush,
		RequestID: 2147483648,
		Body:      configPushBody,
	})

	frame = readFrame(t, conn)
	if frame.Type != protocol.TypeConfigAck {
		t.Fatalf("expected config.ack, got %s", frame.Type.String())
	}
}

func workConnDialer(t *testing.T, wg *sync.WaitGroup, readyCh chan<- int, closedCh chan<- int, expectations []workConnExpectation) func(context.Context, string, string) (net.Conn, error) {
	t.Helper()

	var mu sync.Mutex
	index := 0

	return func(_ context.Context, _, _ string) (net.Conn, error) {
		mu.Lock()
		if index >= len(expectations) {
			mu.Unlock()
			return nil, fmt.Errorf("unexpected extra tcp work dial %d", index+1)
		}
		index++
		current := index
		expectation := expectations[current-1]
		mu.Unlock()

		clientConn, serverConn := net.Pipe()
		wg.Add(1)
		go func() {
			defer wg.Done()
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
			if hello.SessionID != expectation.SessionID {
				t.Errorf("unexpected tcp work session id: got %d want %d", hello.SessionID, expectation.SessionID)
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
			if register.WorkSecret != expectation.WorkSecret {
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

			readyCh <- current
			if expectation.CloseAfterReady {
				if closedCh != nil {
					closedCh <- current
				}
				return
			}

			buffer := make([]byte, 1)
			_, _ = serverConn.Read(buffer)
			if closedCh != nil {
				closedCh <- current
			}
		}()

		return clientConn, nil
	}
}

func waitForWorkReady(t *testing.T, readyCh <-chan int, want int) {
	t.Helper()

	select {
	case got := <-readyCh:
		if got != want {
			t.Fatalf("unexpected work ready index: got %d want %d", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for work ready %d", want)
	}
}

func waitForWorkReadySet(t *testing.T, readyCh <-chan int, wants ...int) {
	t.Helper()

	remaining := make(map[int]struct{}, len(wants))
	for _, want := range wants {
		remaining[want] = struct{}{}
	}

	deadline := time.After(2 * time.Second)
	for len(remaining) > 0 {
		select {
		case got := <-readyCh:
			if _, ok := remaining[got]; !ok {
				t.Fatalf("unexpected work ready index: got %d want one of %#v", got, wants)
			}
			delete(remaining, got)
		case <-deadline:
			t.Fatalf("timed out waiting for work ready set %#v", wants)
		}
	}
}

func waitForWorkClosed(t *testing.T, closedCh <-chan int, want int) {
	t.Helper()

	select {
	case got := <-closedCh:
		if got != want {
			t.Fatalf("unexpected work closed index: got %d want %d", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for work closed %d", want)
	}
}

func waitForTCPWorkPoolCount(t *testing.T, client *Client, want int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		state := client.currentState()
		if state != nil && state.tcpWorkConnCount() == want {
			return
		}
		if time.Now().After(deadline) {
			got := -1
			if state != nil {
				got = state.tcpWorkConnCount()
			}
			t.Fatalf("timed out waiting for tcp work pool count %d, got %d", want, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForWaitGroup(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for work connections to close")
	}
}
