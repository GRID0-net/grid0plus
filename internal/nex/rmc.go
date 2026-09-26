package nex

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// RMC is the call layer inside PRUDP DATA: one message per reassembled
// payload. Reference: https://github.com/kinnay/NintendoClients/wiki/RMC-Protocol
//
//	request:  u32 size | u8 protocol|0x80 [u16 protocol] | u32 call id | u32 method | body
//	success:  u32 size | u8 protocol      [u16 protocol] | u8 1 | u32 call id | u32 method|0x8000 | body
//	error:    u32 size | u8 protocol      [u16 protocol] | u8 0 | u32 result | u32 call id
//
// A protocol id of 0x7F or above is written as 0x7F followed by the real id
// as a u16.

// Request is an RMC call.
type Request struct {
	Protocol uint16
	Method   uint32
	CallID   uint32
	Body     []byte
}

// Response is an RMC reply. Result is 0 for success, otherwise an error code
// with its high bit set.
type Response struct {
	Protocol uint16
	Method   uint32
	CallID   uint32
	Result   uint32
	Body     []byte
}

// Success answers r with body.
func (r *Request) Success(body []byte) *Response {
	return &Response{Protocol: r.Protocol, Method: r.Method, CallID: r.CallID, Body: body}
}

// Error answers r with a result code. The error bit is added if missing, so
// callers can pass the constants below either way.
func (r *Request) Error(code uint32) *Response {
	return &Response{Protocol: r.Protocol, Method: r.Method, CallID: r.CallID, Result: code | ErrorMask}
}

// OK reports whether the response is a success.
func (r *Response) OK() bool { return r.Result&ErrorMask == 0 }

func appendProtocol(b []byte, protocol uint16, request bool) []byte {
	var flag uint8
	if request {
		flag = 0x80
	}
	if protocol < 0x7F {
		return append(b, uint8(protocol)|flag)
	}
	b = append(b, 0x7F|flag)
	return binary.LittleEndian.AppendUint16(b, protocol)
}

// Encode serialises a request.
func (r *Request) Encode() []byte {
	b := make([]byte, 4, 16+len(r.Body))
	b = appendProtocol(b, r.Protocol, true)
	b = binary.LittleEndian.AppendUint32(b, r.CallID)
	b = binary.LittleEndian.AppendUint32(b, r.Method)
	b = append(b, r.Body...)
	binary.LittleEndian.PutUint32(b, uint32(len(b)-4))
	return b
}

// Encode serialises a response.
func (r *Response) Encode() []byte {
	b := make([]byte, 4, 16+len(r.Body))
	b = appendProtocol(b, r.Protocol, false)
	if r.OK() {
		b = append(b, 1)
		b = binary.LittleEndian.AppendUint32(b, r.CallID)
		b = binary.LittleEndian.AppendUint32(b, r.Method|0x8000)
		b = append(b, r.Body...)
	} else {
		b = append(b, 0)
		b = binary.LittleEndian.AppendUint32(b, r.Result)
		b = binary.LittleEndian.AppendUint32(b, r.CallID)
	}
	binary.LittleEndian.PutUint32(b, uint32(len(b)-4))
	return b
}

// DecodeRMC parses one RMC message: exactly one of the returned request and
// response is non-nil.
func DecodeRMC(data []byte) (*Request, *Response, error) {
	if len(data) < 5 {
		return nil, nil, errors.New("rmc: message too short")
	}
	if size := binary.LittleEndian.Uint32(data); int(size) != len(data)-4 {
		return nil, nil, fmt.Errorf("rmc: size field %d, message has %d", size, len(data)-4)
	}
	r := NewReader(data[4:])
	first := r.U8()
	protocol := uint16(first &^ 0x80)
	if protocol == 0x7F {
		protocol = r.U16()
	}
	if first&0x80 != 0 {
		req := &Request{Protocol: protocol, CallID: r.U32(), Method: r.U32()}
		req.Body = append([]byte(nil), r.Rest()...)
		return req, nil, r.Err()
	}
	resp := &Response{Protocol: protocol}
	if r.Bool() {
		resp.CallID = r.U32()
		resp.Method = r.U32() &^ 0x8000
		resp.Body = append([]byte(nil), r.Rest()...)
	} else {
		resp.Result = r.U32()
		resp.CallID = r.U32()
	}
	return nil, resp, r.Err()
}

// ErrorMask marks a result code as an error.
const ErrorMask uint32 = 0x80000000

// Result codes this server returns. Names and values from NintendoClients'
// nintendo/nex/errors.py. Only codes something here actually produces are
// listed -- the console switches on them, so an unused one is a claim about
// behaviour that does not exist.
const (
	ResultSuccess uint32 = 0x00010001 // Core::Unknown without the error bit: NEX's "OK"

	ResultNotImplemented  uint32 = 0x00010002 // Core::NotImplemented
	ResultAccessDenied    uint32 = 0x00010006 // Core::AccessDenied
	ResultInvalidArgument uint32 = 0x0001000A // Core::InvalidArgument

	ResultNotAuthenticated uint32 = 0x00030002 // RendezVous::NotAuthenticated
	ResultInvalidUsername  uint32 = 0x00030064 // RendezVous::InvalidUsername
	ResultAccountDisabled  uint32 = 0x00030067 // RendezVous::AccountDisabled
	ResultInvalidPID       uint32 = 0x0003006B // RendezVous::InvalidPID
	ResultSessionVoid      uint32 = 0x00030073 // RendezVous::SessionVoid
	ResultSessionFull      uint32 = 0x000300C8 // RendezVous::SessionFull
	ResultSessionClosed    uint32 = 0x000300DB // RendezVous::SessionClosed
	ResultNotParticipated  uint32 = 0x000300D4 // RendezVous::NotParticipatedGathering
	ResultPermissionDenied uint32 = 0x000300D9 // RendezVous::PermissionDenied

	ResultTokenParseError      uint32 = 0x00680002 // Authentication::TokenParseError
	ResultTokenExpired         uint32 = 0x00680006 // Authentication::TokenExpired
	ResultValidationFailed     uint32 = 0x00680007 // Authentication::ValidationFailed
	ResultPrincipalIDUnmatched uint32 = 0x00680009 // Authentication::PrincipalIdUnmatched

	ResultRankingNotFound   uint32 = 0x00670005 // Ranking::NotFound
	ResultDataStoreNotFound uint32 = 0x00690004 // DataStore::NotFound
)
