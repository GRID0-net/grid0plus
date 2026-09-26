// Package splatoon2 is Splatoon 2's NEX game server.
//
// Splatoon 2 is a NEX title: its online play goes through a NEX game server
// (matchmaking, Splatfest rankings, match results) and peer-to-peer Pia for
// the match itself. This package is that server, built on
// internal/rendezvous; see README.md for what it answers and why.
package splatoon2

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/n-popescu/switchnet/config"
	"github.com/n-popescu/switchnet/internal/db"
	"github.com/n-popescu/switchnet/internal/identity"
	"github.com/n-popescu/switchnet/internal/rendezvous"
)

// Splatoon 2's game server, from Kinnay's Game Server List: three servers
// exist (testfire, world premiere, main) and 2DF33D01 is the main one.
const (
	GameServerID = 0x2DF33D01
	AccessKey    = "4eb18d39"
)

// Host is the hostname a console resolves to reach it:
// g<game server id, 8 lowercase hex digits>-lp1.s.n.srv.nintendo.net.
const Host = "g2df33d01-lp1.s.n.srv.nintendo.net"

// Title ids, per region. The same game server serves all of them.
var TitleIDs = []string{
	"01003bc0000a0000", // Americas
	"0100f8f0000a2000", // Europe
	"01003c700009c000", // Japan
}

// Options configures the server.
type Options struct {
	Config   config.Splatoon2
	Store    db.Store
	IDTokens *identity.IDTokens
	Log      *slog.Logger
	Now      func() time.Time
	// IdleTimeout drops a silent console.
	IdleTimeout time.Duration
}

// Server is Splatoon 2's game server.
type Server struct {
	*rendezvous.Server
	store db.Store
	log   *slog.Logger
}

// New builds the server.
func New(opts Options) *Server {
	store := opts.Store
	rv := rendezvous.New(rendezvous.Options{
		Title: rendezvous.Title{
			Name:         "splatoon2",
			AccessKey:    AccessKey,
			GameServerID: GameServerID,
			ServerName:   "SwitchNet",

			// The authentication half offers PRUDP minor version 5; the
			// secure half 0. NextendoNetwork measured Splatoon 2's proven
			// server answering the secure SYN with 0 where theirs answered 5,
			// with the client held constant, and scoped the change to the
			// secure half because a title's authentication stopped accepting
			// logins when its version was forced to 0 (facts only).
			AuthMinorVersion:   5,
			SecureMinorVersion: 0,

			SecureAddress: opts.Config.SecureAddress,
			SecurePort:    opts.Config.SecurePort,

			// type=3 (behind NAT | public) and no Pa: what Splatoon 2's
			// proven server answers Register with, measured by
			// NextendoNetwork against the same client (facts only). Newer
			// Pia (5.19, Smash) wants type=11 and Pa; Splatoon 2's is older.
			PublicStationType: 3,
			PublicStationPa:   false,
		},
		IDTokens:    opts.IDTokens,
		Store:       store,
		Log:         opts.Log,
		Now:         opts.Now,
		IdleTimeout: opts.IdleTimeout,
	})
	s := &Server{Server: rv, store: store, log: rv.Log()}
	s.registerExtensions()
	s.registerRanking()
	s.registerDataStore()
	return s
}

// ServeHTTP serves the game server's hostname: the NEX WebSocket, and the
// DataStore object URLs this server hands out on the same name.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/datastore/") && r.Header.Get("Sec-WebSocket-Key") == "" {
		s.serveObjects(w, r)
		return
	}
	s.Server.ServeHTTP(w, r)
}
