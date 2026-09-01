package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// The UI listener is private plus basic auth (SPEC §3); run-now sits behind it.
func TestBasicAuth(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	try := func(h http.Handler, user, pass string) int {
		req := httptest.NewRequest("POST", "/agents/a/run", nil)
		if user != "" {
			req.SetBasicAuth(user, pass)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}

	h := basicAuth(ok, "oter", string(hash))
	if c := try(h, "", ""); c != 401 {
		t.Errorf("no creds: %d", c)
	}
	if c := try(h, "oter", "wrong"); c != 401 {
		t.Errorf("wrong password: %d", c)
	}
	if c := try(h, "someone", "hunter2"); c != 401 {
		t.Errorf("wrong user: %d", c)
	}
	if c := try(h, "oter", "hunter2"); c != 200 {
		t.Errorf("right creds: %d", c)
	}
	if c := try(basicAuth(ok, "oter", ""), "oter", ""); c != 401 {
		t.Errorf("empty hash must never authenticate: %d", c)
	}
}
