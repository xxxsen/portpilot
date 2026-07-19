package server

import (
	"context"
	"testing"

	"github.com/portpilot/portpilot/internal/config"
)

func TestOldEpochCannotReserveOrAttachMux(t *testing.T) {
	t.Parallel()
	app := &App{Config: config.DefaultServerConfig()}
	app.Config.Limits.MaxMuxSessionsTotal = 2
	app.Config.DataPlane.SMux.MaxSessions = 2
	app.Sessions = NewSessionManager(app)
	old := testSession(app, "client", "old", 1)
	current := testSession(app, "client", "current", 2)
	app.Sessions.active[current.ClientID] = current

	if old.ReserveMux("old-mux") {
		t.Fatal("old epoch reserved a mux slot")
	}
	if old.AddMux(&MuxSession{ID: "old-mux", owner: old}) {
		t.Fatal("old epoch attached a mux session")
	}
	if !current.ReserveMux("current-mux") {
		t.Fatal("current epoch could not reserve a mux slot")
	}
	if current.ReserveMux("current-mux") {
		t.Fatal("duplicate mux reservation succeeded")
	}
	if app.Sessions.muxTotal.Load() != 1 {
		t.Fatalf("mux reservations leaked quota: %d", app.Sessions.muxTotal.Load())
	}
	current.ReleaseMux("current-mux")
	if app.Sessions.muxTotal.Load() != 0 {
		t.Fatal("mux reservation quota was not released")
	}
}

func TestGlobalMuxQuotaIsSharedAcrossClients(t *testing.T) {
	t.Parallel()
	app := &App{Config: config.DefaultServerConfig()}
	app.Config.Limits.MaxMuxSessionsTotal = 1
	app.Sessions = NewSessionManager(app)
	first := testSession(app, "first", "session-a", 1)
	second := testSession(app, "second", "session-b", 1)
	app.Sessions.active[first.ClientID] = first
	app.Sessions.active[second.ClientID] = second
	if !first.ReserveMux("mux-a") {
		t.Fatal("first mux reservation failed")
	}
	if second.ReserveMux("mux-b") {
		t.Fatal("global mux quota admitted a second reservation")
	}
	first.ReleaseMux("mux-a")
	if !second.ReserveMux("mux-b") {
		t.Fatal("released global mux quota was not reusable")
	}
	second.ReleaseMux("mux-b")
}

func testSession(app *App, clientID, sessionID string, epoch uint64) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{
		ClientID: clientID, ID: sessionID, Epoch: epoch,
		app: app, ctx: ctx, cancel: cancel,
		send:  make(chan outboundControl, 1),
		muxes: make(map[string]*MuxSession), pendingMux: make(map[string]struct{}),
	}
}
