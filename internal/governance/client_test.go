package governance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientRejectsInsecureRemoteEndpoints(t *testing.T) {
	for _, endpoint := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com?token=x", "https://example.com/path"} {
		if _, err := NewClient(endpoint, "credential"); err == nil {
			t.Fatalf("accepted invalid endpoint %s", endpoint)
		}
	}
}

func TestClientDoesNotFollowCredentialRedirect(t *testing.T) {
	var reached atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "private-test-credential")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Verifier(nil, time.Now).Current(context.Background(), Admission{ID: "test"})
	if err == nil || reached.Load() {
		t.Fatalf("redirect followed: reached=%v err=%v", reached.Load(), err)
	}
}
