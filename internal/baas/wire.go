package baas

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
)

// maxFormBytes caps a form-encoded BAAS body. The largest real one is a login
// carrying an application token, around 2 KiB; 64 KiB is generous and bounds
// what a malformed client can make the server allocate.
const maxFormBytes = 64 << 10

// maxJSONBytes caps a JSON body other than an image upload.
const maxJSONBytes = 64 << 10

type form url.Values

func (f form) Get(key string) string { return url.Values(f).Get(key) }

// parseForm reads a form-encoded body.
func parseForm(r *http.Request) (form, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	return form(r.PostForm), nil
}

// decodeJSONBody decodes a JSON body of at most maxJSONBytes.
func decodeJSONBody(r *http.Request, dst any) error {
	return decodeJSONBodyLimit(r, dst, maxJSONBytes)
}

// decodeJSONBodyLimit decodes a JSON body of at most limit bytes.
//
// DisallowUnknownFields is deliberately NOT used. The console sends fields that
// are not documented and that a future firmware will add to; rejecting a request
// over one would break the endpoint for a change that does not concern us.
func decodeJSONBodyLimit(r *http.Request, dst any, limit int64) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, limit))
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

// readBody reads a request body of at most limit bytes.
//
// Used where the body has to be examined twice, because its shape is not known
// in advance and two decodings are tried against it.
func readBody(r *http.Request, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, limit))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errors.New("baas: empty body")
	}
	return raw, nil
}

// unmarshalJSON is json.Unmarshal, named so the two-attempt decoding in
// friends.go reads clearly.
func unmarshalJSON(raw []byte, dst any) error { return json.Unmarshal(raw, dst) }
