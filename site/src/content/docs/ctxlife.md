---
title: ctxlife
description: Fails on a goroutine that uses its caller's context and outlives the call. Its work is cancelled the moment the function returns.
---

A function's `ctx` belongs to its caller, and it ends when the caller is done with the call. A
goroutine the function starts and doesn't wait for - a server, a watcher, a cache refresher -
outlives that. If it keeps using the caller's `ctx`, it stops working the moment the call returns,
usually with no error anyone sees.

This is not hypothetical. Every workspace on a Spinbox host once lost DNS this way: the resolver
was started under the context of the call that created it.

```go
func (h *Host) Start(ctx context.Context) error {
	go h.resolver.Serve(ctx) // ends when Start's caller cancels ctx
	return nil
}
```

```sh
go tool ctxlife        # the module, or name directories
```

## Three answers

ctxlife reports a goroutine that uses a `context.Context` parameter of the function that starts
it, unless it is one of these:

- **Waited for.** The function receives from a channel or calls `Wait` after starting it, so the
  goroutine finishes before the caller's context ends.
- **Detached.** The parameter is replaced first:
  ```go
  ctx = context.WithoutCancel(ctx) // keeps its values, drops its cancellation
  go h.resolver.Serve(ctx)
  ```
- **Bound on purpose.** A goroutine that *should* stop with the call, such as work for a request
  that was abandoned, says so on the `go` line or the line above it:
  ```go
  // ctx-lifetime: the upload is the request's, and stops when the client goes away
  go h.upload(ctx, body)
  ```

The finding names the file, the line and the parameter, and lists these three fixes. It reads
syntax, not types: a parameter counts when its type is written `context.Context`. Tests are left
out.
