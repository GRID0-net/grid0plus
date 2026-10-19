package baas

import (
	"errors"
	"net/http"
	"strings"

	"github.com/n-popescu/grid0plus/internal/db"
)

// ---- PUT /1.0.0/push_channels/{uid}/{daid} -------------------------------

// pushChannelRequest is what a real console PUTs: which push service, and
// the token that service should deliver notifications to.
type pushChannelRequest struct {
	Service          string         `json:"service"`
	DeviceToken      string         `json:"deviceToken"`
	DeviceAttributes map[string]any `json:"deviceAttributes"`
}

// updatePushChannel answers PUT /1.0.0/push_channels/{uid}/{daid}.
//
// A bare 204 is not what a real server sends back: it answers 200 with a JSON body
// echoing every field the console just PUT plus createdAt/updatedAt/userId/deviceAccountId,
// and one key that was never in the request -- userAttributes: {}, always empty.
// Matching that shape rather than an empty-success guess follows the same "match what
// a real server actually does" reasoning as naUsersMe: an assumed-harmless placeholder
// is not the same as a verified one.
//
// This does not persist the push channel anywhere: nothing in GRID0+ sends push
// notifications through this route, so there is nothing yet to deliver to. The response
// is real-shaped so a client that reads it back does not choke on a shape it has never
// seen from a working server; it is not, on its own, evidence the console needs the
// value to be REMEMBERED across requests.
func (s *Server) updatePushChannel(w http.ResponseWriter, r *http.Request, c *caller) {
	uid := r.PathValue("uid")
	daid := r.PathValue("daid")
	if uid == "" || !strings.EqualFold(uid, c.UserID) || daid != c.DeviceAccountID {
		s.log.Warn("baas: refused push channel update for another account",
			"caller", c.UserID, "caller_device_account", c.DeviceAccountID,
			"target_user", uid, "target_device_account", daid)
		writeError(w, r, errInsufficientScope)
		return
	}

	var body pushChannelRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, r, errInvalidParams)
		return
	}
	if body.DeviceAttributes == nil {
		body.DeviceAttributes = map[string]any{}
	}
	// The token is npns's notification token for this console's local
	// account: remembering it is what lets a push to this user find the
	// console (internal/nplnrest's frontline). Always the caller's own pair,
	// checked above.
	if body.DeviceToken != "" {
		if err := s.store.SetPushChannel(r.Context(), db.PushChannel{
			UserID: c.UserID, DeviceAccountID: c.DeviceAccountID, DeviceToken: body.DeviceToken,
		}); err != nil {
			writeInternalError(s.log, w, r, "store push channel", err)
			return
		}
	}

	now := s.now().Unix()
	writeJSON(w, http.StatusOK, map[string]any{
		"service":          body.Service,
		"deviceToken":      body.DeviceToken,
		"deviceAttributes": body.DeviceAttributes,
		"userAttributes":   map[string]any{},
		"userId":           uid,
		"deviceAccountId":  daid,
		"createdAt":        now,
		"updatedAt":        now,
	})
}

// ---- POST /1.0.0/devices/snapshot ----------------------------------------

// devicesSnapshotRequest is a batch of user ids a console is asking about --
// its own local save data's list of "accounts that have used this console",
// per the real capture's own field name.
type devicesSnapshotRequest struct {
	UserIDs []string `json:"userIds"`
}

// devicesSnapshot answers POST /1.0.0/devices/snapshot.
//
// A real server answers 200 with {"deletedUserIds": [...], "persistentUserIds": [...]},
// splitting the requested ids by whether each still names a real, non-deleted user. That
// split is exactly what this store can already answer for itself (GetUser, and
// db.User.Deleted), so implementing it for real costs nothing extra and risks nothing
// invented.
//
// Called with the anonymous application-level token (bs:grt=1; see router.go's own
// note on this route), so there is no caller identity to scope this to; any set of ids
// can be asked about.
func (s *Server) devicesSnapshot(w http.ResponseWriter, r *http.Request, _ *caller) {
	var body devicesSnapshotRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, r, errInvalidParams)
		return
	}

	persistent := make([]string, 0, len(body.UserIDs))
	deleted := make([]string, 0)
	for _, id := range body.UserIDs {
		user, err := s.store.GetUser(r.Context(), id)
		switch {
		case errors.Is(err, db.ErrNotFound):
			deleted = append(deleted, id)
		case err != nil:
			writeInternalError(s.log, w, r, "load user for devices snapshot", err)
			return
		case user.Deleted:
			deleted = append(deleted, id)
		default:
			persistent = append(persistent, id)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"persistentUserIds": persistent,
		"deletedUserIds":    deleted,
	})
}
