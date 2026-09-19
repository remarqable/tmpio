package models

import (
	"strings"
	"testing"
)

func TestPasswordHashing(t *testing.T) {
	const plain = "correct horse battery staple"
	hash, err := HashPassword(plain)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("expected an encoded argon2id hash, got %q", hash)
	}
	if strings.Contains(hash, plain) {
		t.Fatal("the hash contains the password")
	}
	if !VerifyPassword(hash, plain) {
		t.Fatal("the password does not verify against its own hash")
	}
	for _, wrong := range []string{"", plain + " ", "Correct horse battery staple", "x"} {
		if VerifyPassword(hash, wrong) {
			t.Fatalf("%q verified against another password's hash", wrong)
		}
	}

	// Two hashes of one password differ: the salt is per credential.
	other, err := HashPassword(plain)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}
	if other == hash {
		t.Fatal("two hashes of the same password are identical, so the salt is not random")
	}
	if !VerifyPassword(other, plain) {
		t.Fatal("the second hash does not verify")
	}
}

func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	for _, bad := range []string{
		"",
		"not a hash",
		"$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",            // another algorithm
		"$argon2id$v=18$m=65536,t=3,p=2$c2FsdA$aGFzaA",           // another version
		"$argon2id$v=19$m=65536,t=3,p=2$c2FsdA",                  // truncated
		"$argon2id$v=19$m=notanumber,t=3,p=2$c2FsdA$aGFzaA",      // unparseable parameters
		"$argon2id$v=19$m=65536,t=3,p=2$!!!not-base64!!!$aGFzaA", // unparseable salt
		"$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$!!!not-base64!!!", // unparseable digest
	} {
		if VerifyPassword(bad, "anything") {
			t.Fatalf("malformed hash %q was accepted", bad)
		}
	}
}
