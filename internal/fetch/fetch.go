// Package fetch is how the build reads a URL: a GET whose body is used only when the server
// answered 200, tried again when what failed was the network or a server's bad moment, and that
// says which URL answered what otherwise. What is fetched is checked against its pin by whoever
// asked for it, on the bytes of the attempt that succeeded.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// quiet is how long a server may send nothing - no headers, or no more of the body - before the
// attempt is given up. A deadline for the whole of it would give up on a large download over a
// slow link; none, and a caller whose context has no deadline waits for a stalled server forever.
const quiet = 30 * time.Second

// standard is how every tool here is patient with a server. GitHub Releases answers 504 for tens
// of seconds at a time (2026-11-09 failed CI in three repositories on one): five attempts, waits
// of about 4, 8, 16 and 32 seconds between them, ride that out in under a minute of waiting, and
// an outage that is not a blip still fails the job in about two rather than at its timeout.
var standard = retry{
	attempts: 5,
	first:    4 * time.Second,
	most:     time.Minute,
	budget:   2 * time.Minute,
	jitter:   jitter,
	sleep:    sleep,
	log:      os.Stderr,
	quiet:    quiet,
}

// retry is how many times, and how long apart, a request is made before its failure is the
// caller's.
type retry struct {
	attempts int
	first    time.Duration // the wait after the first attempt, doubled after each one since
	most     time.Duration // the longest one wait, a Retry-After's included
	budget   time.Duration // every wait together
	jitter   func(time.Duration) time.Duration
	sleep    func(context.Context, time.Duration) error
	log      io.Writer
	quiet    time.Duration
}

// Read hands url's body to read. read is called once for each attempt with that attempt's body
// from its first byte: whatever it kept from an attempt that failed is its to throw away. An error
// read returns is the answer, unless it is the body's own - the connection cut halfway - which is
// tried again.
func Read(ctx context.Context, url string, read func(io.Reader) error) error {
	return standard.read(ctx, url, read)
}

// Bytes is url's body, refused past limit bytes rather than cut there.
func Bytes(ctx context.Context, url string, limit int64) ([]byte, error) {
	return standard.bytes(ctx, url, limit)
}

// File writes url's body to f, which then holds the bytes of the attempt that succeeded and
// nothing of one cut short.
func File(ctx context.Context, url string, f *os.File) error {
	return standard.file(ctx, url, f)
}

// Exists is whether url is there to be downloaded: a 200 to a HEAD, redirects followed, and a 404
// is an answer rather than an error. Any other status is one, since it says nothing about the file.
func Exists(ctx context.Context, url string) (bool, error) {
	return standard.exists(ctx, url)
}

func (p retry) read(ctx context.Context, url string, read func(io.Reader) error) error {
	return p.try(ctx, http.MethodGet, url, func(resp *http.Response, body io.Reader) error {
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s answered %s", url, resp.Status)
		}
		return read(body)
	})
}

func (p retry) bytes(ctx context.Context, url string, limit int64) ([]byte, error) {
	var raw []byte
	err := p.read(ctx, url, func(body io.Reader) error {
		var err error
		raw, err = io.ReadAll(io.LimitReader(body, limit+1)) // mutate-exempt: one byte past the limit is all the refusal needs, and more is the same refusal
		return err
	})
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%s is more than %d bytes", url, limit)
	}
	return raw, nil
}

func (p retry) file(ctx context.Context, url string, f *os.File) error {
	return p.read(ctx, url, func(body io.Reader) error {
		if _, err := f.Seek(0, io.SeekStart); err != nil { // mutate-exempt: seeking a file this process opened fails only on a file that is not one
			return err
		}
		if err := f.Truncate(0); err != nil { // mutate-exempt: as above, for a file opened to write
			return err
		}
		_, err := io.Copy(f, body)
		return err
	})
}

func (p retry) exists(ctx context.Context, url string) (bool, error) {
	var ok bool
	err := p.try(ctx, http.MethodHead, url, func(resp *http.Response, _ io.Reader) error {
		switch resp.StatusCode {
		case http.StatusOK:
			ok = true
			return nil
		case http.StatusNotFound:
			return nil
		}
		return fmt.Errorf("%s answered %s", url, resp.Status)
	})
	return ok, err
}

// try makes the request until use answers, an attempt fails in a way another would not mend, or
// the attempts or the time for them run out. Each wait is said on stderr: a job that sits for a
// minute says why.
func (p retry) try(ctx context.Context, method, url string, use func(*http.Response, io.Reader) error) error {
	var spent time.Duration
	for attempt := 1; ; attempt++ {
		err := p.once(ctx, method, url, use)
		var a *again
		if !errors.As(err, &a) {
			return err
		}
		if ctx.Err() != nil {
			return a.err
		}
		wait := p.wait(attempt, a.after)
		if attempt == p.attempts || spent+wait > p.budget {
			return fmt.Errorf("%w (tried %d times)", a.err, attempt)
		}
		_, _ = fmt.Fprintf(p.log, "%v, trying again in %s (attempt %d/%d)\n", a.err, wait, attempt+1, p.attempts)
		if err := p.sleep(ctx, wait); err != nil {
			return err
		}
		spent += wait
	}
}

// wait is how long to wait after attempt failed: the backoff, spread so that every job a blip
// failed does not come back at once, or longer when the server said so, and never past most.
// Whole seconds, so what is said is what is waited.
func (p retry) wait(attempt int, after time.Duration) time.Duration {
	d := max(p.jitter(p.first<<(attempt-1)), after)
	return min(d, p.most).Round(time.Second)
}

// once is one attempt. A failure another attempt could mend is an *again.
func (p retry) once(ctx context.Context, method, url string, use func(*http.Response, io.Reader) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stalled := time.AfterFunc(p.quiet, func() { cancel(&stall{url: url, quiet: p.quiet}) })
	defer stalled.Stop()
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}
	// GitHub answers sixty unauthenticated requests an hour; a token raises it.
	if token := os.Getenv("GITHUB_TOKEN"); token != "" && strings.HasPrefix(url, "https://api.github.com/") {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		err = cause(ctx, err)
		if transient(err) {
			return &again{err: err}
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }() // read-only
	if passing(resp.StatusCode) {
		return &again{err: fmt.Errorf("%s answered %s", url, resp.Status), after: retryAfter(resp.Header.Get("Retry-After"))}
	}
	stalled.Reset(p.quiet)
	err = use(resp, &watched{ctx: ctx, url: url, body: resp.Body, stalled: stalled, quiet: p.quiet})
	// A body that broke is the network's doing, whatever the server answered before it did.
	if b := (*broken)(nil); errors.As(err, &b) {
		return &again{err: err}
	}
	return err
}

// again is a failure another attempt could mend, and how long the server asked to be left alone.
type again struct {
	err   error
	after time.Duration
}

func (a *again) Error() string { return a.err.Error() }

// passing is whether status says the server is having a moment rather than that the request is
// wrong: a 4xx other than a timeout or a rate limit is the same answer every time.
func passing(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// transient is whether err, from sending a request, is the network rather than the request: a
// connection refused or reset, one closed before it answered, a timeout, a server gone quiet, or
// a name server that could not answer just then. A bad certificate or a malformed URL is neither.
func transient(err error) bool {
	var s *stall
	var ne net.Error
	var dns *net.DNSError
	return errors.As(err, &s) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		(errors.As(err, &ne) && ne.Timeout()) ||
		(errors.As(err, &dns) && dns.IsTemporary)
}

// retryAfter is a Retry-After header's wait, in seconds or until a date; none when it says
// neither.
func retryAfter(v string) (wait time.Duration) {
	if s, err := strconv.Atoi(v); err == nil {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		wait = time.Until(t)
	}
	return wait
}

// jitter is a wait somewhere in the upper half of d.
func jitter(d time.Duration) time.Duration {
	return d/2 + rand.N(d/2+1) //nolint:gosec // spreads retries; nothing to guess
}

// sleep waits d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// stall is a server that sent nothing for quiet.
type stall struct {
	url   string
	quiet time.Duration
}

func (s *stall) Error() string { return fmt.Sprintf("%s sent nothing for %s", s.url, s.quiet) }

// broken is a body that could not be read to its end.
type broken struct {
	url string
	err error
}

func (b *broken) Error() string { return fmt.Sprintf("reading %s: %v", b.url, b.err) }
func (b *broken) Unwrap() error { return b.err }

// watched is a body whose every byte puts off giving up on it, and whose failure is a *broken.
type watched struct {
	ctx     context.Context //nolint:containedctx // the attempt's, which Read reports the cause of
	url     string
	body    io.Reader
	stalled *time.Timer
	quiet   time.Duration
}

func (w *watched) Read(p []byte) (int, error) {
	n, err := w.body.Read(p)
	if n > 0 {
		w.stalled.Reset(w.quiet)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		err = &broken{url: w.url, err: cause(w.ctx, err)}
	}
	return n, err
}

// cause is why ctx ended, when it did: a stalled server's URL, rather than "context canceled".
func cause(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return err
}
