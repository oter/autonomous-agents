package run

import (
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
	"filippo.io/age/armor"
)

// MasterIdentity is the path of the age identity file every Allowlist value
// is encrypted to (SPEC §3). The file is read when a value is needed and
// held no longer: nothing is decrypted at startup, and no key material of
// any kind reaches a container (ADR-0003).
type MasterIdentity string

// Check reads the identity once, so a deploy whose key file is missing,
// readable by others, or malformed fails at startup rather than at the
// first Run that needs a secret. Nothing is decrypted.
func (m MasterIdentity) Check() error {
	_, err := m.load()
	return err
}

// Decrypt returns the plaintext of one armored Allowlist value.
func (m MasterIdentity) Decrypt(armored string) (string, error) {
	ids, err := m.load()
	if err != nil {
		return "", err
	}
	r, err := age.Decrypt(armor.NewReader(strings.NewReader(armored)), ids...)
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (m MasterIdentity) load() ([]age.Identity, error) {
	fi, err := os.Stat(string(m))
	if err != nil {
		return nil, fmt.Errorf("master identity: %w", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("master identity %s: mode %04o, must be 0600", m, fi.Mode().Perm())
	}
	f, err := os.Open(string(m))
	if err != nil {
		return nil, fmt.Errorf("master identity: %w", err)
	}
	defer f.Close()
	ids, err := age.ParseIdentities(f)
	if err != nil {
		return nil, fmt.Errorf("master identity %s: %w", m, err)
	}
	return ids, nil
}
