package baas

import (
	"net/http"

	sjwt "github.com/n-popescu/grid0plus/internal/jwt"
)

// The two JWK set endpoints.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/BAAS-Server#get-100certificates
//
// These are the seam between the account layer and the game servers, and they
// are the only BAAS endpoints that need no access token — a game server
// verifying a player's id token has no BAAS token of its own.
//
//	/1.0.0/certificates           the key for ID TOKENS. A game server fetches
//	                              this to verify the token a console handed it,
//	                              which is how it learns which player is
//	                              connecting without trusting anything the game
//	                              says.
//
//	/1.0.0/internal_certificates  the key for ACCESS and USER TOKENS, the ones
//	                              this API authenticates with.
//
// The two publish DIFFERENT keys, and that is the point. If they shared one, an
// id token would verify as an access token — so a game server's copy of a
// player's token could act as that player against their own account. There is a
// test asserting the two key ids differ.
//
// /1.0.0/certificates actually publishes two keys of its own: idIssuer's (for
// the id token embedded in a login/federation response) and naIDIssuer's (for
// the Nintendo Account link flow's id token, nalink.go). Both are "developer"
// usage and both live at this one URL, but they must be different keys and
// therefore different 'kid's — see sjwt.IssuerNAID's doc comment for the
// console-side failure mode ("2124-0292") that reusing one kid across two
// issuers actually causes, confirmed against a real private server's own
// operational notes.
//
// Nintendo marks the sets with a 'usage' field: "developer" for the id token key
// and "internal" for the access token key.
//
// # Key rotation
//
// Nintendo regenerates most of its JWKs every 24 hours and keeps the two
// previous keys in the set so tokens issued before a rotation still verify. The
// exception is exactly this pair, which never changes.
//
// GRID0+ does not rotate either. On a private network rotation would mean
// handing consoles tokens they cannot use until they re-authenticate, and buys
// nothing: the keys are not exposed to anybody who is not already trusted with
// the server.
func (s *Server) registerCertificateRoutes(mux *http.ServeMux) {
	mux.Handle("GET /1.0.0/certificates",
		sjwt.JWKSHandler(sjwt.JWKSetFor("developer", s.idIssuer.Key(), s.naIDIssuer.Key())))
	mux.Handle("GET /1.0.0/internal_certificates",
		sjwt.JWKSHandler(sjwt.JWKSetFor("internal", s.accessIssuer.Key())))
}
