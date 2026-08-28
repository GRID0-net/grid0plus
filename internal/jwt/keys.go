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

	mu     sync.Mutex
	keys   map[string]Key
	ecKeys map[string]ECKey
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
		ecKeys:            map[string]ECKey{},
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
	k := Key{ID: keyIDFor(&priv.PublicKey), Private: priv}
	r.keys[name] = k
	return k, nil
}

// The five issuers SwitchNet signs for. A key must never be shared between
// two of them, even when two of them publish at the same JWK set URL (as
// IssuerBAASID and IssuerNAID do): the console links a token's 'kid' to its
// 'iss' and rejects the whole Nintendo Account import if it ever sees one
// kid claiming two different issuers. See IssuerNAID.
const (
	// IssuerDevice signs device tokens. Published at the dcert /keys endpoint.
	IssuerDevice = "device"
	// IssuerApp signs application tokens. Published at the acert /keys endpoint.
	IssuerApp = "app"
	// IssuerBAASAccess signs BAAS access and user tokens. Published at
	// /1.0.0/internal_certificates.
	IssuerBAASAccess = "baas_access"
	// IssuerBAASID signs BAAS id tokens (iss = the BAAS host), the ones game
	// servers verify. Published at /1.0.0/certificates.
	IssuerBAASID = "baas_id"
	// IssuerNAID signs the Nintendo Account link flow's id tokens (iss =
	// "https://accounts.nintendo.com", internal_baas/nalink.go) -- a
	// deliberately separate key from IssuerBAASID even though both are
	// published in the same /1.0.0/certificates JWK set. Confirmed against a
	// real, working private server's own operational notes (Nextendo
	// Network's baas-jwks, 2026-08-26): reusing one kid across the accounts.
	// and BAAS issuers makes the console reject the import silently, with no
	// further network call, which matches SwitchNet's own error 2124-0292
	// exactly. Before this key existed, nalink.go signed with IssuerBAASID's
	// key by mistake.
	IssuerNAID = "na_id"
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
	key, err := parsePEM(path)
	if err != nil {
		return nil, err
	}
	priv, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an RSA key", path)
	}
	return priv, nil
}

// parsePEM reads a PEM private key of any type this package supports.
//
// The type is deliberately not inferred from the filename: a key directory
// where npln.key.pem holds an RSA key is a misconfiguration that must fail
// loudly here, not produce ES256-shaped tokens signed with the wrong algorithm.
func parsePEM(path string) (any, error) {
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
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(block.Bytes)
	case "PRIVATE KEY":
		return x509.ParsePKCS8PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("%s: unexpected PEM block %q", path, block.Type)
	}
}

// generate creates an RSA signing key and writes it to path, exactly once.
// See writeKeyOnce for why "exactly once" is the load-bearing part.
func generate(path string) (*rsa.PrivateKey, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}
	existing, err := writeKeyOnce(path, priv)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return priv, nil
	}
	other, ok := existing.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an RSA key", path)
	}
	return other, nil
}

// writeKeyOnce writes priv to path exactly once, and returns the key that is
// actually on disk afterwards: nil if ours won the race, or the one another
// process wrote first.
//
// The exclusive create is what makes this safe when several services start at
// the same moment — which is every `docker compose up`, because they all share
// one key directory and all find it empty. Without it, each process generates
// its own key, the last writer wins on disk, and every process keeps the one it
// made in memory. The result is dauth signing with one key while aauth verifies
// with another, and the error the console eventually sees is "device token
// expired", which points nowhere near the cause.
//
// "Exclusive" has to mean the CONTENT appears atomically, not just the name —
// see the comment inside for the bug an O_EXCL create onto the final path has.
func writeKeyOnce(path string, priv any) (any, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create key dir: %w", err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	buf := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	// Written to a temporary file first, then LINKED into place.
	//
	// An O_EXCL create straight onto path looks like it does this and does not.
	// It publishes the NAME before the CONTENT: the loser of the race sees
	// os.ErrExist, reads the file, and finds it empty or half-written. The
	// symptom is "no PEM block" from a directory that visibly contains a key,
	// and it happens exactly when several services start at once -- which is
	// every `docker compose up`, and is the one case this function exists for.
	//
	// os.Link is atomic and fails with EEXIST if the target exists, so it keeps
	// the create-or-fail semantics while making the file appear complete.
	tmp, err := os.CreateTemp(dir, ".key-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create temporary key file: %w", err)
	}
	tmpName := tmp.Name()
	// Removed whichever way this goes: on success the link is what persists, and
	// on failure a stray 0600 private key must not be left in the key
	// directory for a later run to trip over.
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("chmod temporary key file: %w", err)
	}
	if _, err := tmp.Write(buf); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write signing key: %w", err)
	}
	// Flushed before the link, so a crash cannot leave a named file with no
	// contents -- the durability half of the same problem.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("sync signing key: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close temporary key file: %w", err)
	}

	if err := os.Link(tmpName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			// Somebody else got there first, and because they linked too, their
			// file is complete. Use their key, not ours.
			return parsePEM(path)
		}
		return nil, fmt.Errorf("create signing key %s: %w", path, err)
	}
	return nil, nil
}
