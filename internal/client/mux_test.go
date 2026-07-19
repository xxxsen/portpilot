package client

import (
	"net"
	"testing"
	"time"

	"github.com/portpilot/portpilot/internal/protocol"
	"github.com/xtaci/smux"
)

func TestMuxPoolSelectsLeastLoadedHealthySession(t *testing.T) {
	t.Parallel()
	first := testClientMux(t, "first")
	second := testClientMux(t, "second")
	third := testClientMux(t, "third")
	first.active.Store(4)
	second.active.Store(1)
	third.active.Store(0)
	third.draining.Store(true)
	pool := &MuxPool{
		params: protocol.DataPlane{MaxStreamsPerSession: 8},
		sessions: map[string]*clientMux{
			first.id: first, second.id: second, third.id: third,
		},
	}
	if selected := pool.Select(); selected != second {
		t.Fatalf("unexpected mux selected: %#v", selected)
	}
	second.active.Store(8)
	if selected := pool.Select(); selected != first {
		t.Fatalf("stream-saturated mux was selected: %#v", selected)
	}
	first.draining.Store(true)
	if selected := pool.Select(); selected != nil {
		t.Fatalf("unhealthy mux pool returned a session: %#v", selected)
	}
}

func TestMuxDrainClosesIdleSession(t *testing.T) {
	t.Parallel()
	mux := testClientMux(t, "draining")
	pool := &MuxPool{
		params: protocol.DataPlane{
			MaxStreamsPerSession: 8,
			SMux:                 protocol.SMuxParameters{DrainTimeout: "100ms"},
		},
		sessions: map[string]*clientMux{mux.id: mux},
	}
	pool.Drain(mux.id)
	select {
	case <-mux.session.CloseChan():
	case <-time.After(time.Second):
		t.Fatal("idle draining mux did not close")
	}
}

func testClientMux(t *testing.T, id string) *clientMux {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	serverResult := make(chan *smux.Session, 1)
	go func() {
		session, err := smux.Server(serverConn, smux.DefaultConfig())
		if err == nil {
			serverResult <- session
		}
	}()
	clientSession, err := smux.Client(clientConn, smux.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	var serverSession *smux.Session
	select {
	case serverSession = <-serverResult:
	case <-time.After(time.Second):
		t.Fatal("smux server session did not start")
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
	})
	return &clientMux{id: id, session: clientSession}
}
