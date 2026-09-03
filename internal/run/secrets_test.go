package run_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"filippo.io/age/armor"
	"github.com/oter/autonomous-agents/internal/run"
)

// newIdentity writes a fresh age identity to a 0600 file, as age-keygen
// does, and returns it with a function that encrypts to it: the armored
// ciphertext a `|` block in an Agent YAML holds.
func newIdentity(t *testing.T) (run.MasterIdentity, func(plaintext string) string) {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "age-master.key")
	if err := os.WriteFile(path, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return run.MasterIdentity(path), func(plaintext string) string {
		var buf bytes.Buffer
		a := armor.NewWriter(&buf)
		w, err := age.Encrypt(a, id.Recipient())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(plaintext)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.String() + "\n"
	}
}

// ADR-0003: the control plane decrypts with the master identity, on demand.
// A value comes back byte-exact, including a trailing newline.
func TestMasterIdentityDecryptsOnDemand(t *testing.T) {
	identity, encrypt := newIdentity(t)
	if err := identity.Check(); err != nil {
		t.Fatal(err)
	}
	got, err := identity.Decrypt(encrypt("lin_api_x=y z\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "lin_api_x=y z\n" {
		t.Errorf("plaintext = %q", got)
	}
	_, other := newIdentity(t)
	if _, err := identity.Decrypt(other("x")); err == nil {
		t.Error("a value encrypted to another identity decrypted")
	}
	if _, err := identity.Decrypt("not armor"); err == nil {
		t.Error("garbage decrypted")
	}
}

// SPEC §3: the identity file is 0600. One readable by anyone else is
// refused, as is a missing or malformed one, at startup and at every use.
func TestMasterIdentityRefusesLooseOrBadFile(t *testing.T) {
	identity, _ := newIdentity(t)
	if err := os.Chmod(string(identity), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := identity.Check(); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Errorf("group-readable identity: err = %v, want a refusal naming 0600", err)
	}
	for name, body := range map[string]string{"empty": "", "garbage": "AGE-SECRET-KEY-NOPE\n"} {
		path := filepath.Join(t.TempDir(), "k")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := run.MasterIdentity(path).Check(); err == nil {
			t.Errorf("%s identity file passed Check", name)
		}
	}
	if err := run.MasterIdentity(filepath.Join(t.TempDir(), "missing")).Check(); err == nil {
		t.Error("missing identity file passed Check")
	}
}
