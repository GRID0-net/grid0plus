package rendezvous

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/n-popescu/switchnet/internal/nex"
)

// TicketGranting (protocol 10): the only protocol on the authentication
// port. https://github.com/kinnay/NintendoClients/wiki/Authentication-Protocol
const ProtocolTicketGranting = 0x0A

const (
	methodValidateAndRequestTicket                     = 1 // Login
	methodValidateAndRequestTicketWithCustomData       = 2 // LoginEx
	methodRequestTicket                                = 3
	methodGetPID                                       = 4
	methodGetName                                      = 5
	methodValidateAndRequestTicketWithParam            = 6
	authTokenTypeSwitch                          uint8 = 2
)

// loginKey carries a successful login on the authentication connection, for
// RequestTicket.
type loginKey struct{}

type loginState struct {
	pid       uint64
	sourceKey []byte
}

func (s *Server) serveAuth(c *nex.Conn, req *nex.Request) *nex.Response {
	if req.Protocol != ProtocolTicketGranting {
		s.log.Warn("nex: non-TicketGranting call on the authentication port", "protocol", req.Protocol, "method", req.Method)
		return req.Error(nex.ResultNotImplemented)
	}
	switch req.Method {
	case methodValidateAndRequestTicketWithCustomData:
		return s.loginEx(c, req)
	case methodValidateAndRequestTicketWithParam:
		return s.loginWithParam(c, req)
	case methodValidateAndRequestTicket:
		// Takes a username and nothing else. On a Switch server a normal
		// account cannot log in this way -- there is nowhere to put the id
		// token -- and the special accounts (guest) that could are deleted
		// (Kinnay, Kerberos Authentication, "Token Authentication").
		r := nex.NewReader(req.Body)
		s.log.Warn("nex: refused a login with no token", "username", r.String())
		return req.Success(s.loginResult(nex.ResultInvalidUsername|nex.ErrorMask, 0, nil, nil))
	case methodRequestTicket:
		return s.requestTicket(c, req)
	case methodGetPID:
		return s.getPID(req)
	case methodGetName:
		return s.getName(req)
	}
	s.log.Warn("nex: TicketGranting method not implemented", "method", req.Method)
	return req.Error(nex.ResultNotImplemented)
}

// authenticate turns a login's username and AuthenticationInfo into a PID,
// or refuses. Fail-closed at every step, and the username only ever
// NARROWS what the token says: it has to agree with the token's subject, it
// never chooses an identity by itself.
func (s *Server) authenticate(username string, info *nex.Reader) (uint64, uint32) {
	token, ok := readAuthenticationInfo(info)
	if !ok {
		s.log.Warn("nex: login without a readable AuthenticationInfo", "username", username)
		return 0, nex.ResultTokenParseError
	}
	claimed, err := strconv.ParseUint(username, 10, 64)
	if err != nil {
		s.log.Warn("nex: login username is not a user id", "username", username)
		return 0, nex.ResultInvalidUsername
	}
	if s.idTokens == nil {
		return 0, nex.ResultValidationFailed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	caller, err := s.idTokens.Resolve(ctx, token)
	if err != nil {
		s.log.Warn("nex: login refused, id token did not resolve", "username", username)
		return 0, nex.ResultValidationFailed
	}
	pid, err := PIDForUserID(caller.UserID)
	if err != nil {
		return 0, nex.ResultValidationFailed
	}
	if pid != claimed {
		s.log.Warn("nex: login refused, username is not the token's user",
			"username", username, "token_user", caller.UserID)
		return 0, nex.ResultPrincipalIDUnmatched
	}
	return pid, 0
}

// readAuthenticationInfo reads the AnyDataHolder<AuthenticationInfo> a
// Switch sends: the Data parent level (empty), then
// {String token, u32 ngsVersion, u8 tokenType, u32 serverVersion}.
func readAuthenticationInfo(r *nex.Reader) (string, bool) {
	name, obj := r.AnyData()
	if r.Err() != nil || name != "AuthenticationInfo" {
		return "", false
	}
	var token string
	var tokenType uint8
	obj.Struct(func(*nex.Reader, uint8) {}) // nn::nex::Data
	obj.Struct(func(sub *nex.Reader, _ uint8) {
		token = sub.String()
		_ = sub.U32() // ngsVersion: 4 on Switch
		tokenType = sub.U8()
		_ = sub.U32() // serverVersion
	})
	if obj.Err() != nil || token == "" {
		return "", false
	}
	if tokenType != authTokenTypeSwitch {
		// 1 is a Wii U NEX token, 0 a 3DS one: neither is anything SwitchNet
		// issued. Refused rather than tried.
		return "", false
	}
	return token, true
}

// ticketFor issues a ticket to the secure server and remembers the login on
// the authentication connection for RequestTicket.
func (s *Server) ticketFor(c *nex.Conn, pid uint64) ([]byte, []byte) {
	// The source key encrypts the ticket and goes back to the console as
	// pSourceKey, which a Switch uses instead of deriving one from a
	// password it does not have. Random per login: it protects the ticket
	// only in transit, inside TLS, and nothing ever needs to reproduce it.
	sourceKey := make([]byte, nex.SessionKeySize)
	_, _ = rand.Read(sourceKey)
	ticket := nex.IssueTicket(pid, SecurePID, sourceKey, s.secureKey, s.now())
	c.SetValue(loginKey{}, loginState{pid: pid, sourceKey: sourceKey})
	return ticket, sourceKey
}

// loginEx is ValidateAndRequestTicketWithCustomData, Splatoon 2's login
// (NEX 4.0; NextendoNetwork's server records S2 using method 2, facts only).
//
//	request:  String username | AnyDataHolder<AuthenticationInfo>
//	response: Result | PID | Buffer ticket | RVConnectionData | String build name | String pSourceKey
//
// A refusal is a SUCCESSFUL RMC response whose Result field carries the
// error, with the other fields blank -- the documented shape, and the one
// NintendoClients' client parses.
func (s *Server) loginEx(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	username := r.String()
	pid, code := s.authenticate(username, r)
	if code != 0 {
		return req.Success(s.loginResult(code|nex.ErrorMask, 0, nil, nil))
	}
	ticket, sourceKey := s.ticketFor(c, pid)
	s.log.Info("nex: login", "pid", pid, "remote", c.RemoteIP)
	return req.Success(s.loginResult(nex.ResultSuccess, pid, ticket, sourceKey))
}

func (s *Server) loginResult(result uint32, pid uint64, ticket, sourceKey []byte) []byte {
	w := nex.NewWriter()
	w.Result(result)
	w.PID(pid)
	w.Buffer(ticket)
	s.writeConnectionData(w, result&nex.ErrorMask == 0)
	w.String(s.title.ServerName)
	w.String(hex.EncodeToString(sourceKey))
	return w.Bytes()
}

// writeConnectionData writes RVConnectionData (version 1, NEX >= 3.5).
func (s *Server) writeConnectionData(w *nex.Writer, ok bool) {
	w.Struct(1, func(w *nex.Writer) {
		if ok {
			w.StationURL(s.SecureStation())
		} else {
			w.StationURL(nex.NewStationURL("prudp"))
		}
		nex.WriteList(w, []uint8(nil), (*nex.Writer).U8) // no special protocols
		w.StationURL(nex.NewStationURL("prudp"))
		w.DateTime(nex.NewDateTime(s.now()))
	})
}

// SecureStation is where the console should connect to reach the secure
// server.
//
// By default, "the same place you are already talking to": address 0.0.0.1,
// port 1 is how a Switch server says so (Kinnay, Authentication Protocol,
// pConnectionData), and it keeps the whole game server behind one hostname,
// one certificate name and one nginx block. sid=2 is the secure server's
// virtual port.
//
// The scheme is "prudps", and must be: it is how the console decides the
// target is a secure server and therefore puts its ticket in the CONNECT.
// With "prudp" it connects with an empty CONNECT and only fails once Pia
// needs the session (NextendoNetwork's measurement, facts only).
func (s *Server) SecureStation() *nex.StationURL {
	u := nex.NewStationURL("prudps")
	addr, port := "0.0.0.1", 1
	if s.title.SecureAddress != "" {
		addr = s.title.SecureAddress
		port = s.title.SecurePort
	}
	u.Set("address", addr)
	u.SetInt("port", int64(port))
	u.SetInt("CID", 1)
	u.SetInt("PID", SecurePID)
	u.SetInt("sid", SecurePort)
	u.SetInt("stream", nex.StreamRVSecure)
	u.SetInt("type", 2)
	return u
}

// loginWithParam is ValidateAndRequestTicketWithParam (NEX >= 4.4). Splatoon
// 2 does not use it; it is here so a title that does gets the same checks.
//
//	request:  ValidateAndRequestTicketParam{u32 platform, String username,
//	          AnyDataHolder extraData, bool skipVersionCheck, u32 nexVersion, u32 clientVersion}
//	response: ValidateAndRequestTicketResult{PID, Buffer ticket, StationURL,
//	          DateTime, String build name, String pSourceKey}
//
// Unlike LoginEx, a refusal here is an RMC error.
func (s *Server) loginWithParam(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	var username string
	var pid uint64
	var code uint32 = nex.ResultTokenParseError
	r.Struct(func(p *nex.Reader, _ uint8) {
		_ = p.U32()
		username = p.String()
		pid, code = s.authenticate(username, p)
	})
	if r.Err() != nil && code == 0 {
		code = nex.ResultInvalidArgument
	}
	if code != 0 {
		return req.Error(code)
	}
	ticket, sourceKey := s.ticketFor(c, pid)
	w := nex.NewWriter()
	w.Struct(1, func(w *nex.Writer) {
		w.PID(pid)
		w.Buffer(ticket)
		w.StationURL(s.SecureStation())
		w.DateTime(nex.NewDateTime(s.now()))
		w.String(s.title.ServerName)
		w.String(hex.EncodeToString(sourceKey))
	})
	s.log.Info("nex: login", "pid", pid, "remote", c.RemoteIP)
	return req.Success(w.Bytes())
}

// requestTicket issues another ticket to a console that already logged in
// on this connection. NEX calls it when the first ticket's target does not
// match the secure station's PID, which never happens here -- both are
// SecurePID -- but a console is entitled to ask.
//
//	request:  PID source | PID target
//	response: Result | Buffer ticket | String pSourceKey
func (s *Server) requestTicket(c *nex.Conn, req *nex.Request) *nex.Response {
	r := nex.NewReader(req.Body)
	source, target := r.PID(), r.PID()
	st, _ := c.Value(loginKey{}).(loginState)
	w := nex.NewWriter()
	if r.Err() != nil || st.pid == 0 || source != st.pid || target != SecurePID {
		// Only for yourself, only after logging in, only to the secure
		// server. Anything else would be a ticket for a principal this
		// connection never proved it was.
		w.Result(nex.ResultAccessDenied | nex.ErrorMask)
		w.Buffer(nil)
		w.String("")
		return req.Success(w.Bytes())
	}
	w.Result(nex.ResultSuccess)
	w.Buffer(nex.IssueTicket(st.pid, SecurePID, st.sourceKey, s.secureKey, s.now()))
	w.String(hex.EncodeToString(st.sourceKey))
	return req.Success(w.Bytes())
}

// getPID maps a username to a PID: a user's username is their PID in
// decimal. 0 when there is no such user.
func (s *Server) getPID(req *nex.Request) *nex.Response {
	name := nex.NewReader(req.Body).String()
	w := nex.NewWriter()
	pid, err := strconv.ParseUint(name, 10, 64)
	if err != nil || !s.exists(pid) {
		pid = 0
	}
	w.PID(pid)
	return req.Success(w.Bytes())
}

// getName is getPID's inverse. "" when there is no such user.
func (s *Server) getName(req *nex.Request) *nex.Response {
	pid := nex.NewReader(req.Body).PID()
	w := nex.NewWriter()
	if pid != 0 && s.exists(pid) {
		w.String(strconv.FormatUint(pid, 10))
	} else {
		w.String("")
	}
	return req.Success(w.Bytes())
}
