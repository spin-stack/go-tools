// Package fetch is how the build reads a URL: a GET that answers the body only when the server
// answered 200, and says which URL answered what otherwise. What is fetched is checked against its
// pin by whoever asked for it.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// quiet is how long a server may send nothing - no headers, or no more of the body - before the
// request is given up. A deadline for the whole of it would give up on a large download over a
// slow link; none, and a caller whose context has no deadline waits for a stalled server forever.
const quiet = 30 * time.Second

// Get is url's body, which the caller closes.
func Get(ctx context.Context, url string) (io.ReadCloser, error) {
	return get(ctx, url, quiet)
}

func get(ctx context.Context, url string, quiet time.Duration) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	stalled := time.AfterFunc(quiet, func() { cancel(fmt.Errorf("%s sent nothing for %s", url, quiet)) })
	fail := func(err error) (io.ReadCloser, error) {
		stalled.Stop()
		cancel(nil)
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fail(err)
	}
	// GitHub answers sixty unauthenticated requests an hour; a token raises it.
	if token := os.Getenv("GITHUB_TOKEN"); token != "" && strings.HasPrefix(url, "https://api.github.com/") {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fail(cause(ctx, err))
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return fail(fmt.Errorf("%s answered %s", url, resp.Status))
	}
	stalled.Reset(quiet)
	return &watched{ctx: ctx, body: resp.Body, stalled: stalled, quiet: quiet, cancel: cancel}, nil
}

// watched is a body whose every byte puts off giving up on it.
type watched struct {
	ctx     context.Context //nolint:containedctx // the request's, which Read reports the cause of
	body    io.ReadCloser
	stalled *time.Timer
	quiet   time.Duration
	cancel  context.CancelCauseFunc
}

func (w *watched) Read(p []byte) (int, error) {
	n, err := w.body.Read(p)
	if n > 0 {
		w.stalled.Reset(w.quiet)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		err = cause(w.ctx, err)
	}
	return n, err
}

func (w *watched) Close() error {
	w.stalled.Stop()
	w.cancel(nil)
	return w.body.Close()
}

// cause is why ctx ended, when it did: a stalled server's URL, rather than "context canceled".
func cause(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return err
}

// Bytes is url's body, refused past limit bytes rather than cut there.
func Bytes(ctx context.Context, url string, limit int64) ([]byte, error) {
	body, err := Get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }() // read-only
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", url, err)
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%s is more than %d bytes", url, limit)
	}
	return raw, nil
}
