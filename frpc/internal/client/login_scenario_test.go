package client

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestClientHandleWorkStreamOpenUsesSnapshotForFirstStream(t *testing.T) {
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

	workClient, workServer := net.Pipe()
	defer workClient.Close()
	defer workServer.Close()

	client := New(testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), "test-client")
	state := newSessionState(1000)
	state.setSnapshot(protocol.ConfigPush{
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
		t.Fatalf("marshal stream.open: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.handleWorkStreamOpen(workClient, state, protocol.Frame{
			Type:      protocol.TypeStreamOpen,
			RequestID: 4,
			StreamID:  70,
			Body:      openBody,
		})
	}()

	openedFrame := readFrame(t, workServer)
	if openedFrame.Type != protocol.TypeStreamOpened {
		t.Fatalf("expected stream.opened, got %s", openedFrame.Type.String())
	}
	opened, err := protocol.UnmarshalStreamOpened(openedFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal stream.opened: %v", err)
	}
	if opened.Status != protocol.StatusOK {
		t.Fatalf("unexpected stream.opened: %#v", opened)
	}

	if _, err := workServer.Write([]byte("ping")); err != nil {
		t.Fatalf("write raw work payload: %v", err)
	}
	response := make([]byte, len("login-snapshot"))
	if _, err := io.ReadFull(workServer, response); err != nil {
		t.Fatalf("read relayed response: %v", err)
	}
	if string(response) != "login-snapshot" {
		t.Fatalf("unexpected payload from snapshot target: %q", string(response))
	}

	select {
	case err := <-errCh:
		if err != nil && !isNetClosed(err) {
			t.Fatalf("handle work stream open: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("work stream relay did not exit")
	}

	select {
	case err := <-targetDone:
		if err != nil {
			t.Fatalf("snapshot target: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot target did not finish")
	}
}
