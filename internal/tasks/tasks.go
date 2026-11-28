// Package tasks is the tasks every spin-stack repository includes, tasks.yml beside this file, as
// the bytes a release ships: what go-tools' `task` (cmd/task) names in GO_TOOLS_TASKS once the
// release is unpacked, and what versions/gate reads to know which pins those tasks read. It is a
// package, and not a file beside cmd/task, because a test of another repository has to link it.
package tasks

import _ "embed" // File

// File is tasks.yml, at the version of go-tools that is compiled.
//
//go:embed tasks.yml
var File []byte
