package npln

import "strings"

// NPLN resource names.
//
// Every resource in the definitions is addressed by a path, and everything
// below a tenant hangs off "tenants/<tenant id>". These helpers are the only
// place that shape is written down, so a capture that contradicts it is one
// file to change rather than a search through handlers.
//
// TODO(capture-needed): the exact prefixes. The path shape follows the
// definitions' own naming, and the collection segments are the lowerCamelCase
// plurals AIP-122 (which NPLN's schema follows throughout) prescribes — but no
// capture confirms what Splatoon 3 receives, or whether it parses these at all
// rather than treating them as opaque handles. A capture of IssueToken's or
// CreateMatchmakingTicket's response would settle every one of them at once.
// Change: this file.

// tenantName is the prefix every tenant-scoped resource hangs off.
func (s *Server) tenantName() string { return "tenants/" + s.tenant }

// userResourceName renders a user id as a resource name.
func userResourceName(userID string) string { return "users/" + userID }

func (s *Server) ticketName(id string) string {
	return s.tenantName() + "/matchmakingTickets/" + id
}

func (s *Server) sessionName(id string) string {
	return s.tenantName() + "/gameSessions/" + id
}

func (s *Server) userSessionName(sessionID, userID string) string {
	return s.sessionName(sessionID) + "/userSessions/" + userID
}

func (s *Server) iceServerSetName() string {
	return s.tenantName() + "/iceServerSet"
}

// lastSegment returns the final element of a resource name.
//
// Used to pull an id out of a path without asserting the rest of it. The
// alternative — matching the whole path — turns an unconfirmed prefix into a
// hard requirement, so a capture that shows a different one would reject every
// call rather than merely look wrong in a log.
func lastSegment(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}
