package zip

// The peer surface — one app's ops as another app may invoke them.
//
// A typed op rides EVERY transport its app listens on. So an internal op
// registered on the edge-facing app is an internal op served on :8000, and the
// separation between "what the world may call" and "what a peer may call" has to
// be structural rather than a check somebody remembers to write.
//
// It is a second [App], listening only on [SocketPath], and it is reached with
// [Ask]. That is the whole of the internal call plane: no registry, no discovery,
// no address in a caller's configuration — the name IS the address.

// Peer is this app's peer sibling: the ops a peer may invoke, served ONLY on
// this app's canonical socket and never on the edge.
//
//	svc.ops(app)          // the world's surface
//	svc.plane(app.Peer()) // the fleet's surface
//	return app.Listen(zip.Addr(":9653"))
//
// [App.Listen] brings it up, so there is one serve verb and no second Listen to
// forget. It exists only when something registered on it: an app with no peer
// ops binds no peer socket, and one that has them binds exactly the path every
// caller's [Ask] resolves.
//
// Memoized, because two peer apps would be two answers to one op name.
func (a *App) Peer() *App {
	a.peerOnce.Do(func() {
		p := New(Config{
			AppName:               a.cfg.AppName,
			Logger:                a.logger,
			DisableStartupMessage: true,
			// A peer surface publishes no document and serves no agent door: it is
			// reached by name and op token from inside the fleet, and its schema is
			// the Go types both ends import.
			OpenAPI: OpenAPIConfig{Disabled: true},
			MCP:     MCPConfig{Disabled: true},
		})
		p.sibling = true
		a.peer = p
	})
	return a.peer
}

// servePeer binds the peer sibling to this app's canonical socket. Called from
// Listen on the PRIMARY app only, and only when peer ops were registered — a
// plugin that answers no peer op should not create a socket other processes can
// dial and get nothing from.
func (a *App) servePeer() {
	if a.sibling || a.peer == nil || len(a.peer.Registry()) == 0 {
		return
	}
	if a.cfg.AppName == "" {
		a.logger.Error("zip: peer ops registered on an app with no AppName — " +
			"the name IS the socket, so there is nowhere to serve them")
		return
	}
	sock := SocketPath(a.cfg.AppName)
	peer := a.peer
	go func() {
		if err := peer.Listen(sock); err != nil {
			a.logger.Error("zip peer listener stopped", "addr", sock, "err", err)
		}
	}()
}
