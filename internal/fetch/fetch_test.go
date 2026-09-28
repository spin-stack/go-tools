package fetch

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A body is answered only when the server said 200, and only whole.
func TestABodyIsWholeAndOnlyAnOKOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("0123456789"))
	}))
	t.Cleanup(srv.Close)
	for _, tc := range []struct {
		path    string
		limit   int64
		want    string
		wantErr string
	}{
		{path: "/", limit: 10, want: "0123456789"},
		{path: "/", limit: 9, wantErr: "more than 9 bytes"},
		{path: "/missing", limit: 10, wantErr: "404"},
	} {
		got, err := Bytes(t.Context(), srv.URL+tc.path, tc.limit)
		switch {
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s, %d: err = %v, want %q", tc.path, tc.limit, err, tc.wantErr)
		case tc.wantErr == "" && (err != nil || string(got) != tc.want):
			t.Errorf("%s, %d: %q, %v", tc.path, tc.limit, got, err)
		}
	}
}

// A server that goes quiet, before its headers or halfway through its body, is given up on and
// named; one that keeps sending, however slowly in all, is not.
func TestAServerThatGoesQuietIsGivenUpOn(t *testing.T) {
	const quiet = 100 * time.Millisecond
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/headers":
			<-release
		case "/body":
			_, _ = w.Write([]byte("half"))
			_ = http.NewResponseController(w).Flush()
			<-release
		case "/slow":
			for range 4 {
				_, _ = w.Write([]byte("."))
				_ = http.NewResponseController(w).Flush()
				time.Sleep(quiet / 2)
			}
		}
	}))
	// Cleanups run last first: the handlers let go before the server waits for them.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	for _, tc := range []struct {
		path, wantErr string
	}{
		{"/headers", "sent nothing"},
		{"/body", "sent nothing"},
		{"/slow", ""},
	} {
		body, err := get(t.Context(), srv.URL+tc.path, quiet)
		if err == nil {
			_, err = io.ReadAll(body)
			_ = body.Close()
		}
		switch {
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: err = %v, want %q", tc.path, err, tc.wantErr)
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: %v", tc.path, err)
		}
	}
}
