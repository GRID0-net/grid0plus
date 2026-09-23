package npln

// userResourceName renders a user id as this tenant's user resource name.
//
// "tenants/<tenant>/users/<id>", not the bare "users/<id>" this returned
// until 2026-09-23. internal/ugcstore's own README and
// docs/CAPTURES/npln-gamerecord-attributes-2026-09-23.md document, from the
// v11.3.0 decompile, that the console's NPLN client SDK aborts (2162-0001 in
// nn.npln.Worker, per NextendoNetwork/splatoon-3's measurement) on any
// resource name that is not "tenants/<t>/<rest>" -- confirmed there for
// Ugcstore document names, and there is one resource-name parser in the SDK,
// not one per service. Every User/Token this server had been handing back
// was exactly the shape that parser rejects, on the very first RPC a client
// makes: a defensible reading of why Splatoon 3 crashed immediately after
// IssuePrearrangedUserToken with no further call ever reaching this server.
func (s *Server) userResourceName(userID string) string {
	return "tenants/" + s.tenant + "/users/" + userID
}
