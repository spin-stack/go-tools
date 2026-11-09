// Command worker runs the shop's nightly job.
package main

import (
	"os"
	"os/exec"
)

func main() {
	cmd := exec.Command("sh", "-c", "echo nightly")
	cmd.Stdout = os.Stdout
	_ = cmd.Run()
}
