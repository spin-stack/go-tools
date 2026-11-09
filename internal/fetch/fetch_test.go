package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waiting is the standard policy with its waits written down instead of waited, spread by
// nothing, and what it says kept: the policy as shipped, minus the time.
func waiting() (retry, *[]time.Duration, *strings.Builder) {
	var waits []time.Duration
	said := &strings.Builder{}
	p := standard
	p.jitter = func(d time.Duration) time.Duration { return d }
	p.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	p.log = said
	return p, &waits, said
}

// answers is a server that answers each request with the next of each, the last one again once
// they run out, and counts what it was asked.
func answers(t *testing.T, each ...http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(asked.Add(1))
		each[min(n, len(each))-1](w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

func status(code int, header ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		w.WriteHeader(code)
	}
}

func body(s string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(s)) }
}

// cut promises ten bytes, sends four, and hangs up.
func cut(w http.ResponseWriter, _ *http.Request) {
	conn, buf, err := http.NewResponseController(w).Hijack()
	if err != nil {
		panic(err)
	}
	_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\nhalf")
	_ = buf.Flush()
	_ = conn.Close()
}

// What a server's bad moment is, and what an answer that will not change is: each is asked as
// many times as it takes, or once, and each wait is said.
func TestADownloadIsTriedAgainOnlyWhenAnotherAttemptCouldMendIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []http.HandlerFunc
		want    string
		wantErr string
		asked   int32
		waits   []time.Duration
		said    string
	}{
		{name: "a gateway timeout, then the file", answers: []http.HandlerFunc{status(504), body("0123456789")},
			want: "0123456789", asked: 2, waits: []time.Duration{4 * time.Second},
			said: "answered 504 Gateway Timeout, trying again in 4s (attempt 2/5)\n"},
		{name: "unavailable throughout", answers: []http.HandlerFunc{status(503)},
			wantErr: "answered 503 Service Unavailable (tried 5 times)", asked: 5,
			waits: []time.Duration{4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second}},
		{name: "a body cut short, then the whole of it", answers: []http.HandlerFunc{cut, body("0123456789")},
			want: "0123456789", asked: 2, waits: []time.Duration{4 * time.Second}, said: "unexpected EOF, trying again in 4s"},
		{name: "a server error, a bad gateway, a request timeout, then the file",
			answers: []http.HandlerFunc{status(500), status(502), status(408), body("ok")},
			want:    "ok", asked: 4, waits: []time.Duration{4 * time.Second, 8 * time.Second, 16 * time.Second}},
		{name: "rate limited for longer than the backoff", answers: []http.HandlerFunc{status(429, "Retry-After", "20"), body("ok")},
			want: "ok", asked: 2, waits: []time.Duration{20 * time.Second}},
		{name: "rate limited for less than the backoff", answers: []http.HandlerFunc{status(429, "Retry-After", "1"), body("ok")},
			want: "ok", asked: 2, waits: []time.Duration{4 * time.Second}},
		// A minute at most each, and two minutes of waiting in all: the third would be past it.
		{name: "rate limited for longer than is waited", answers: []http.HandlerFunc{status(429, "Retry-After", "90")},
			wantErr: "answered 429 Too Many Requests (tried 3 times)", asked: 3, waits: []time.Duration{time.Minute, time.Minute}},
		{name: "not there", answers: []http.HandlerFunc{http.NotFound}, wantErr: "answered 404 Not Found", asked: 1},
		{name: "forbidden", answers: []http.HandlerFunc{status(403)}, wantErr: "answered 403 Forbidden", asked: 1},
		{name: "not implemented", answers: []http.HandlerFunc{status(501)}, wantErr: "answered 501 Not Implemented", asked: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, asked := answers(t, tc.answers...)
			p, waits, said := waiting()
			got, err := p.bytes(t.Context(), srv.URL, 64)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, string(got))
			}
			assert.Equal(t, tc.asked, asked.Load(), "requests")
			assert.Equal(t, tc.waits, *waits, "waits")
			assert.Contains(t, said.String(), tc.said)
			assert.Equal(t, len(tc.waits), strings.Count(said.String(), "\n"), "a wait went unsaid")
		})
	}
}

// A Retry-After may be a date: the wait is until then.
func TestARetryAfterDateIsWaitedFor(t *testing.T) {
	then := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	srv, _ := answers(t, status(503, "Retry-After", then), body("ok"))
	p, waits, _ := waiting()
	_, err := p.bytes(t.Context(), srv.URL, 64)
	require.NoError(t, err)
	require.Len(t, *waits, 1)
	assert.InDelta(t, 29.5, (*waits)[0].Seconds(), 1.5)
}

// A body is answered only when the server said 200, and only whole.
func TestABodyIsWholeAndOnlyAnOKOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing":
			http.NotFound(w, r)
		case "/moved":
			w.WriteHeader(http.StatusNoContent)
		default:
			_, _ = w.Write([]byte("0123456789"))
		}
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
		{path: "/moved", limit: 10, wantErr: "answered 204 No Content"},
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

// What read itself refuses is the answer, not the network's doing: a file whose sum is wrong is
// the same file the next time.
func TestWhatReadRefusesIsNotTriedAgain(t *testing.T) {
	srv, asked := answers(t, body("the package"))
	p, waits, _ := waiting()
	refused := errors.New("not what is pinned")
	err := p.read(t.Context(), srv.URL, func(r io.Reader) error {
		_, _ = io.ReadAll(r)
		return refused
	})
	require.ErrorIs(t, err, refused)
	assert.Equal(t, int32(1), asked.Load())
	assert.Empty(t, *waits)
}

// A file holds the attempt that succeeded and nothing of the one cut short before it.
func TestAFileIsOnlyTheAttemptThatSucceeded(t *testing.T) {
	srv, asked := answers(t, body("a longer first body"), cut, body("0123456789"))
	p, _, _ := waiting()
	f, err := os.Create(filepath.Join(t.TempDir(), "file"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	// The first leaves the file holding more than the download; the second is cut short once.
	require.NoError(t, p.file(t.Context(), srv.URL, f))
	require.NoError(t, p.file(t.Context(), srv.URL, f))
	assert.Equal(t, int32(3), asked.Load())
	raw, err := os.ReadFile(f.Name())
	require.NoError(t, err)
	assert.Equal(t, "0123456789", string(raw))
}

// A HEAD is tried again as a GET is, and a 404 is still an answer.
func TestExistsIsTriedAgainAndANotFoundIsAnAnswer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []http.HandlerFunc
		want    bool
		wantErr string
		asked   int32
	}{
		{name: "there, after a 502", answers: []http.HandlerFunc{status(502), status(200)}, want: true, asked: 2},
		{name: "not there", answers: []http.HandlerFunc{status(404)}, asked: 1},
		{name: "forbidden", answers: []http.HandlerFunc{status(403)}, wantErr: "answered 403 Forbidden", asked: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, asked := answers(t, tc.answers...)
			p, _, _ := waiting()
			got, err := p.exists(t.Context(), srv.URL)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.asked, asked.Load())
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)
	ok, err := Exists(t.Context(), srv.URL)
	require.NoError(t, err)
	assert.True(t, ok)
}

// Nothing listening is the network's doing, tried the whole budget and said as refused.
func TestARefusedConnectionIsTriedAgain(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	p, waits, _ := waiting()
	_, err = p.bytes(t.Context(), "http://"+addr+"/", 64)
	require.ErrorIs(t, err, syscall.ECONNREFUSED)
	assert.ErrorContains(t, err, "(tried 5 times)")
	assert.Len(t, *waits, 4)
}

// A caller that gives up is not waited past, nor asked of again.
func TestACallerThatGivesUpIsNotKeptWaiting(t *testing.T) {
	srv, asked := answers(t, status(503))
	ctx, cancel := context.WithCancel(t.Context())
	p := standard
	p.log = io.Discard
	p.sleep = func(ctx context.Context, d time.Duration) error {
		cancel()
		return sleep(ctx, d)
	}
	start := time.Now()
	_, err := p.bytes(ctx, srv.URL, 64)
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), time.Second)
	assert.Equal(t, int32(1), asked.Load())

	// One that gave up before the answer came is not answered with another attempt.
	ctx, cancel = context.WithCancel(t.Context())
	p, waits, _ := waiting()
	slow, _ := answers(t, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err = p.bytes(ctx, slow.URL, 64)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, *waits)

	// A wait that runs out is waited.
	require.NoError(t, sleep(t.Context(), time.Millisecond))
}

// A server that goes quiet, before its headers or halfway through its body, is given up on, named,
// and tried again; one that keeps sending, however slowly in all, is not.
func TestAServerThatGoesQuietIsGivenUpOn(t *testing.T) {
	const quiet = 100 * time.Millisecond
	// Until the client hangs up, which is what giving up on a server is.
	quietFor := func(path string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if path == "/body" {
				_, _ = w.Write([]byte("half"))
				_ = http.NewResponseController(w).Flush()
			}
			<-r.Context().Done()
		}
	}
	slow := func(w http.ResponseWriter, _ *http.Request) {
		for range 4 {
			_, _ = w.Write([]byte("."))
			_ = http.NewResponseController(w).Flush()
			time.Sleep(quiet / 2)
		}
	}
	for _, tc := range []struct {
		name    string
		answers []http.HandlerFunc
		want    string
		wantErr string
		asked   int32
	}{
		{name: "quiet before its headers", answers: []http.HandlerFunc{quietFor("/headers")}, wantErr: "sent nothing for 100ms (tried 2 times)", asked: 2},
		{name: "quiet halfway", answers: []http.HandlerFunc{quietFor("/body")}, wantErr: "sent nothing for 100ms (tried 2 times)", asked: 2},
		{name: "quiet, then answering", answers: []http.HandlerFunc{quietFor("/headers"), body("ok")}, want: "ok", asked: 2},
		{name: "slow", answers: []http.HandlerFunc{slow}, want: "....", asked: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, asked := answers(t, tc.answers...)
			p, _, _ := waiting()
			p.quiet, p.attempts = quiet, 2
			got, err := p.bytes(t.Context(), srv.URL, 64)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, string(got))
			}
			assert.Equal(t, tc.asked, asked.Load())
		})
	}
}

// What sending a request can fail with, sorted into the network's doing and the request's.
func TestTransientIsTheNetworkNotTheRequest(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&stall{url: "u", quiet: time.Second}, true},
		{fmt.Errorf("dial: %w", syscall.ECONNREFUSED), true},
		{fmt.Errorf("read: %w", syscall.ECONNRESET), true},
		{io.EOF, true},
		{io.ErrUnexpectedEOF, true},
		{&net.DNSError{Err: "timeout", IsTimeout: true}, true},
		{&net.DNSError{Err: "server misbehaving", IsTemporary: true}, true},
		{&net.DNSError{Err: "no such host", IsNotFound: true}, false},
		{errors.New("x509: certificate signed by unknown authority"), false},
		{errors.New(`unsupported protocol scheme ""`), false},
	} {
		assert.Equal(t, tc.want, transient(tc.err), "%v", tc.err)
	}
}

// A jittered wait is anywhere in the upper half of the backoff, both ends included, and nowhere
// else: a backoff of four nanoseconds is small enough to see every wait it can be.
func TestJitterIsTheUpperHalf(t *testing.T) {
	seen := map[time.Duration]bool{}
	for range 1000 {
		seen[jitter(4)] = true
	}
	assert.Equal(t, map[time.Duration]bool{2: true, 3: true, 4: true}, seen)
}

const (
	releaseURL = "https://github.com/o/r/releases/download/v1.2/tool"
	cdn        = "release-assets.githubusercontent.com"
)

// github is GitHub as these tests ask it, host by host, through http.DefaultClient's transport -
// so the client's own redirect handling, which is what drops a token, is the real one - with
// every request written down as "host/path authorization|accept". A release's file is served as
// GitHub serves it: github.com and the API redirect to the CDN that holds the bytes.
func github(t *testing.T, fromCDN http.HandlerFunc) *[]string {
	var asked []string
	hosts := map[string]http.HandlerFunc{
		"github.com": func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/o/r/releases/download/v1.2/tool":
				http.Redirect(w, r, "https://"+cdn+"/tool", http.StatusFound)
			case "/o/r/archive/v1.2.tar.gz":
				_, _ = io.WriteString(w, "an archive")
			default:
				http.NotFound(w, r)
			}
		},
		"api.github.com": func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/repos/o/r/releases/tags/v1.2":
				_, _ = io.WriteString(w, `{"assets":[{"id":8,"name":"other"},{"id":7,"name":"tool"}]}`)
			case "/repos/o/r/releases/assets/7":
				http.Redirect(w, r, "https://"+cdn+"/tool", http.StatusFound)
			default:
				http.NotFound(w, r)
			}
		},
		cdn:           fromCDN,
		"example.com": func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "elsewhere") },
	}
	before := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		asked = append(asked, r.URL.Host+r.URL.Path+" "+r.Header.Get("Authorization")+"|"+r.Header.Get("Accept"))
		rec := httptest.NewRecorder()
		hosts[r.URL.Host](rec, r)
		resp := rec.Result()
		resp.Request = r
		return resp, nil
	})
	t.Cleanup(func() { http.DefaultClient.Transport = before })
	return &asked
}

// A token goes to GitHub's API and nowhere else. A release's file is asked for through the API,
// where a token counts, and the token stops there: the CDN the API redirects to, github.com and
// any other host never see it.
func TestATokenGoesOnlyToGitHubsAPI(t *testing.T) {
	viaAPI := func(tok string) []string {
		return []string{
			"api.github.com/repos/o/r/releases/tags/v1.2 Bearer " + tok + "|",
			"api.github.com/repos/o/r/releases/assets/7 Bearer " + tok + "|application/octet-stream",
			cdn + "/tool |application/octet-stream",
		}
	}
	for _, tc := range []struct {
		name, githubToken, ghToken, url, want string
		asked                                 []string
	}{
		{name: "a release's file, with GITHUB_TOKEN", githubToken: "tok", url: releaseURL, want: "the tool", asked: viaAPI("tok")},
		{name: "a release's file, with GH_TOKEN", ghToken: "gh", url: releaseURL, want: "the tool", asked: viaAPI("gh")},
		{name: "a release's file, with both", githubToken: "tok", ghToken: "gh", url: releaseURL, want: "the tool", asked: viaAPI("tok")},
		{name: "a release's file, without a token", url: releaseURL, want: "the tool",
			asked: []string{"github.com/o/r/releases/download/v1.2/tool |", cdn + "/tool |"}},
		{name: "github.com, not a release's file", githubToken: "tok", url: "https://github.com/o/r/archive/v1.2.tar.gz",
			want: "an archive", asked: []string{"github.com/o/r/archive/v1.2.tar.gz |"}},
		{name: "another host", githubToken: "tok", url: "https://example.com/f", want: "elsewhere", asked: []string{"example.com/f |"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GITHUB_TOKEN", tc.githubToken)
			t.Setenv("GH_TOKEN", tc.ghToken)
			asked := github(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "the tool") })
			got, err := Bytes(t.Context(), tc.url, 64)
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got))
			assert.Equal(t, tc.asked, *asked)
		})
	}
}

// With a token, a release's file that is not there - no such release, or none of that name - is
// said so, and Exists answers no rather than failing.
func TestAReleaseFileThatIsNotThereIsSaidSo(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("GH_TOKEN", "")
	github(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "the tool") })
	for _, tc := range []struct {
		url, wantErr string
		exists       bool
	}{
		{url: releaseURL, exists: true},
		{url: "https://github.com/o/r/releases/download/v1.2/missing", wantErr: "release v1.2 of o/r has no asset missing"},
		{url: "https://github.com/o/r/releases/download/v9/tool", wantErr: "release v9 of o/r has no asset tool"},
	} {
		_, err := Bytes(t.Context(), tc.url, 64)
		if tc.wantErr != "" {
			require.ErrorContains(t, err, tc.url+": "+tc.wantErr)
		} else {
			require.NoError(t, err)
		}
		ok, err := Exists(t.Context(), tc.url)
		require.NoError(t, err)
		assert.Equal(t, tc.exists, ok, tc.url)
	}
	// And without one, it is asked of github.com as it always was.
	t.Setenv("GITHUB_TOKEN", "")
	asked := github(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	ok, err := Exists(t.Context(), releaseURL)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []string{"github.com/o/r/releases/download/v1.2/tool |", cdn + "/tool |"}, *asked)
}

// A failure names the host that answered: a 504 from GitHub's CDN, behind a redirect, is not
// github.com's.
func TestAFailureNamesTheHostThatAnswered(t *testing.T) {
	for _, tc := range []struct{ token, want string }{
		{"", releaseURL + ": " + cdn + " answered 504 Gateway Timeout (tried 5 times)"},
		{"tok", releaseURL + ": https://api.github.com/repos/o/r/releases/assets/7: " + cdn + " answered 504 Gateway Timeout (tried 5 times)"},
	} {
		t.Setenv("GITHUB_TOKEN", tc.token)
		t.Setenv("GH_TOKEN", "")
		github(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusGatewayTimeout) })
		p, _, said := waiting()
		_, err := p.bytes(t.Context(), releaseURL, 64)
		require.EqualError(t, err, tc.want)
		assert.Contains(t, said.String(), cdn+" answered 504 Gateway Timeout, trying again")
	}
	// And a host that answered itself is named once.
	github(t, nil)
	_, err := Bytes(t.Context(), "https://github.com/o/r/archive/missing.tar.gz", 64)
	require.EqualError(t, err, "https://github.com/o/r/archive/missing.tar.gz answered 404 Not Found")
}

// What a release download is, and what is only like one.
func TestAReleaseFileIsGitHubsReleaseDownload(t *testing.T) {
	for _, tc := range []struct {
		url              string
		owner, repo, tag string
		name             string
		ok               bool
	}{
		{url: releaseURL, owner: "o", repo: "r", tag: "v1.2", name: "tool", ok: true},
		{url: "https://github.com/o/r/releases/download/v1%2B2/a%20b", owner: "o", repo: "r", tag: "v1+2", name: "a b", ok: true},
		{url: "http://github.com/o/r/releases/download/v1.2/tool"},
		{url: "https://github.com/o/r/releases/download/v1.2/tool?x=1"},
		{url: "https://api.github.com/o/r/releases/download/v1.2/tool"},
		{url: "https://github.com/o/r/releases/latest/download/tool"},
		{url: "https://github.com/o/r/archive/download/v1.2/tool"},
		{url: "https://github.com/o/r/releases/download/release/v1/tool"},
		{url: "://"},
	} {
		owner, repo, tag, name, ok := releaseFile(tc.url)
		assert.Equal(t, []any{tc.owner, tc.repo, tc.tag, tc.name, tc.ok}, []any{owner, repo, tag, name, ok}, tc.url)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
