package rules

import (
	"fmt"
	"testing"

	"code-review-agent/diff"
)

func TestDebugMix3(t *testing.T) {
	files, _ := diff.ReadFromFilePaths([]string{"/tmp/edge/mix.env"})
	f := files[0]
	rt := NewGeneralSecretRule()
	out, err := rt.Check(f)
	fmt.Printf("DBG3 out=%d err=%v lang=%v\n", len(out), err, DetectLanguage(f.NewPath))
	for _, l := range collectAddedLines(f) {
		m := unquotedCredRe.FindStringSubmatch(l.Content)
		fmt.Printf("DBG3 line=%q match=%v\n", l.Content, m != nil)
	}
}
