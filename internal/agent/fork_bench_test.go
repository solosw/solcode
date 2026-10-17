package agent

import (
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/solosw/solcode/internal/tokenest"
)

// This compares inherited request material, not paid-provider billing or cache
// hit rates. Real counters are available per run through Task status/history.
func BenchmarkForkContext(b *testing.B) {
	parent := Snapshot{WorkingSet: []byte("{\"Goal\":\"bounded task\"}")}
	for i := 0; i < 100; i++ {
		parent.Messages = append(parent.Messages, sdk.NewUserMessage(sdk.NewTextBlock("request")), sdk.NewAssistantMessage(sdk.NewTextBlock(strings.Repeat("analysis ", 100))))
	}
	for _, mode := range []string{"none", "last_n", "full", "working_set"} {
		b.Run(mode, func(b *testing.B) {
			cfg := AgentConfig{ForkContext: mode, ForkLastN: 5, ParentContext: parent}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := selectFork(cfg); err != nil {
					b.Fatal(err)
				}
			}
			inherited, err := selectFork(cfg)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(len(inherited.Messages)), "messages/op")
			b.ReportMetric(float64(len(inherited.WorkingSet)), "working_set_bytes/op")
			b.ReportMetric(float64(tokenest.Messages(inherited.Messages)), "inherited_message_tokens/op")
		})
	}
}
