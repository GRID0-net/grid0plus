package npln

// userResourceName renders a user id as this tenant's user resource name.
//
// "tenants/<tenant>/users/<id>", not the bare "users/<id>". The console's NPLN
// client SDK aborts (2162-0001 in nn.npln.Worker) on any resource name that is not
// "tenants/<t>/<rest>", and there is one resource-name parser in the SDK, not one
// per service. A User or Token handed back in the bare shape is rejected on the very
// first RPC a client makes: Splatoon 3 crashed immediately after
// IssuePrearrangedUserToken with no further call ever reaching this server.
func (s *Server) userResourceName(userID string) string {
	return "tenants/" + s.tenant + "/users/" + userID
}
