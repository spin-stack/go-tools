package shell_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/spin-stack/go-tools/internal/shell"
)

// A word ends at a blank outside quotes, and loses its quotes; inside single quotes nothing is
// special, inside double quotes and outside a backslash keeps the next character as it is, and
// an empty pair of quotes is still a word.
func TestWordsAreAShellsWords(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{"task lint", []string{"task", "lint"}},
		{"  task\tlint  ", []string{"task", "lint"}},
		{`task cov TITLE="unit (hermetic)"`, []string{"task", "cov", "TITLE=unit (hermetic)"}},
		{`task cov TITLE='a "b" c' X=1`, []string{"task", "cov", `TITLE=a "b" c`, "X=1"}},
		{`echo "a \" b" c\ d`, []string{"echo", `a " b`, "c d"}},
		{`echo '\'`, []string{"echo", `\`}},
		{`echo "" x`, []string{"echo", "", "x"}},
		{`echo "$X shard"`, []string{"echo", "$X shard"}},
		{"", nil},
	} {
		t.Run(tc.line, func(t *testing.T) {
			assert.Equal(t, tc.want, shell.Words(tc.line))
		})
	}
}
