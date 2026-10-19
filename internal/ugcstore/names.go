package ugcstore

import (
	"strings"
)

// A document name is "tenants/<tenant>/documents/<rest>". The console's own
// Ugcstore client aborts on anything else -- 2162-0001 in nn.npln.Worker -- and a
// malformed name that reaches storage keeps crashing the game after a restart
// because it is read back every time. So a name that does not parse is refused
// before it is stored.
const documentsSegment = "/documents/"

// canonicalName validates name and returns the key it is stored under.
//
// The console writes its own tenant as "current" in some names and in full in
// others ("t-dce9377b-lp1"), and the configured tenant may be in either the
// full or the short form; all of them name the same documents, so the tenant
// segment is normalised to the configured tenant. The rest is kept verbatim.
func (s *Service) canonicalName(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, "tenants/")
	if !ok {
		return "", false
	}
	tenant, path, ok := strings.Cut(rest, "/")
	if !ok || tenant == "" {
		return "", false
	}
	path = "/" + path
	docPath, ok := strings.CutPrefix(path, documentsSegment)
	if !ok || docPath == "" || strings.HasSuffix(docPath, "/") || strings.Contains(docPath, "//") {
		return "", false
	}
	if tenant != "current" && shortTenant(tenant) != shortTenant(s.tenant) {
		return "", false
	}
	return "tenants/" + s.tenant + documentsSegment + docPath, true
}

// pathOwner returns the user a document path is scoped to -- the segment after
// the first "users" -- if it has one.
//
// A path like services/GameRecord/users/<uid>/VsUserAttribute/Data belongs to
// that player: only they may write it, whatever the document's recorded owner.
// Paths without a users segment (locker posts, pictures) are owned by whoever
// created them.
func pathOwner(canonical string) (string, bool) {
	_, docPath, _ := strings.Cut(canonical, documentsSegment)
	parts := strings.Split(docPath, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "users" {
			return parts[i+1], true
		}
	}
	return "", false
}

// shortTenant reduces "t-<id>-<env>" to "<id>". The same rule as npln's own
// tenant matching; duplicated rather than imported, because npln registers
// this package and cannot be imported by it.
func shortTenant(t string) string {
	if rest, ok := strings.CutPrefix(t, "t-"); ok {
		if i := strings.LastIndexByte(rest, '-'); i > 0 {
			return rest[:i]
		}
	}
	return t
}
