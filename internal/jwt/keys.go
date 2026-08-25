// Package jwt issues and verifies the JSON Web Tokens the Switch's auth chain
// is built on, and serves the JWK sets the tokens' 'jku' headers point at.
//
// Reference: https://github.com/kinnay/NintendoClients/wiki/Switch-Tokens
//
// SwitchNet signs its own tokens with its own keys. It never reproduces
// Nintendo's signing keys and never validates a Nintendo-issued credential:
// tokens presented by a console are only checked against keys SwitchNet itself
// generated.
package jwt

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// KeyID is a stable identifier for a signing key. It goes in the 'kid' header
// of every token and in the matching JWK.
type KeyID = string

// Key is one RSA signing key plus the id it is published under.
type Key struct {
	ID      KeyID
	Private *rsa.PrivateKey
}

// Public returns the verification half of the key.
func (k Key) Public() *rsa.PublicKey { return &k.Private.PublicKey }

// Keyring holds the signing keys, one per token issuer, loaded from disk.
//
// Each issuer gets its own key so that compromising one does not let an
// attacker mint the others, and so that the JWK set served at each 'jku' URL
// contains only the key that URL is supposed to publish.
type Keyring struct {
	dir               string
	generateIfMissing bool

	mu   sync.Mutex
	keys map[string]Key
}

// NewKeyring returns a keyring backed by PEM files in dir. Keys are loaded
// lazily on first use. If generateIfMissing is set, a missing key is created
// and written to disk; otherwise a missing key is an error.
//
// Generating on demand is convenient for a first run, but it means a lost
// volume silently invalidates every token the consoles are holding. Once the
// keys are backed up, turn generation off so that a missing key fails loudly.
func NewKeyring(dir string, generateIfMissing bool) *Keyring {
	return &Keyring{
		dir:               dir,
		generateIfMissing: generateIfMissing,
		keys:              map[string]Key{},
	}
}

// Key returns the signing key for the named issuer, loading or generating it
// on first use. Names are used as filenames, so keep them to [a-z0-9_-].
func (r *Keyring) Key(name string) (Key, error) {
	if err := validKeyName(name); err != nil {
		return Key{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if k, ok := r.keys[name]; ok {
		return k, nil
	}

	path := filepath.Join(r.dir, name+".key.pem")
	priv, err := loadPEM(path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist) && r.generateIfMissing:
		priv, err = generate(path)
		if err != nil {
			return Key{}, err
		}
	default:
		return Key{}, fmt.Errorf("load signing key %s: %w", name, err)
	}
	k := Key{ID: keyIDFor(priv), Private: priv}
	r.keys[name] = k
	return k, nil
}

// The four issuers SwitchNet signs for. They map one-to-one onto the JWK set
// URLs a console fetches, so a key must never be shared between two of them.
const (
	// IssuerDevice signs device tokens. Published at the dcert /keys endpoint.
	IssuerDevice = "device"
	// IssuerApp signs application tokens. Published at the acert /keys endpoint.
	IssuerApp = "app"
	// IssuerBAASAccess signs BAAS access and user tokens. Published at
	// /1.0.0/internal_certificates.
	IssuerBAASAccess = "baas_access"
	// IssuerBAASID signs BAAS id tokens, the ones game servers verify.
	// Published at /1.0.0/certificates.
	IssuerBAASID = "baas_id"
)

func validKeyName(name string) error {
	if name == "" {
		return errors.New("jwt: empty key name")
	}
	for _, c := range name {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return fmt.Errorf("jwt: invalid key name %q", name)
		}
	}
	return nil
}

func loadPEM(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%s: no PEM block", path)
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		any, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		priv, ok := any.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%s: not an RSA key", path)
		}
		return priv, nil
	default:
		return nil, fmt.Errorf("%s: unexpected PEM block %q", path, block.Type)
	}
}

// generate creates a signing key and writes it to path, exactly once.
//
// The exclusive create is what makes this safe when several services start at
// the same moment — which is every `docker compose up`, because they all share
// one key directory and all find it empty. Without it, each process generates
// its own key, the last writer wins on disk, and every process keeps the one it
// made in memory. The result is dauth signing with one key while aauth verifies
// with another, and the error the console eventually sees is "device token
// expired", which points nowhere near the cause.
func generate(path string) (*rsa.PrivateKey, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create key dir: %w", err)
	}

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	buf := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	// O_EXCL: create or fail, never truncate. 0600 because it is a private key.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			// Somebody else got there first. Use their key, not ours.
			return loadPEM(path)
		}
		return nil, fmt.Errorf("create signing key %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Write(buf); err != nil {
		return nil, fmt.Errorf("write signing key: %w", err)
	}
	// Flush to disk before returning. Another process that sees the file must
	// see a complete key in it, not a partial write.
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("sync signing key: %w", err)
	}
	return priv, nil
}
