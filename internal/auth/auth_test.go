package auth

import "testing"

// TestCredentials checks hashing, password bounds, and non-reversible session storage.
func TestCredentials(t *testing.T) {
	if _, e := HashPassword("short"); e == nil {
		t.Fatal("short password accepted")
	}
	h, e := HashPassword("a sufficiently long password")
	if e != nil {
		t.Fatal(e)
	}
	if h == "a sufficiently long password" || !Verify(h, "a sufficiently long password") || Verify(h, "wrong") {
		t.Fatal("password verification")
	}
	a, e := Token()
	if e != nil {
		t.Fatal(e)
	}
	b, e := Token()
	if e != nil {
		t.Fatal(e)
	}
	if len(a) != 64 || a == b || Digest(a) == a {
		t.Fatal("session entropy/storage")
	}
}
