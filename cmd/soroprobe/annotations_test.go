package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/soroworks/soroprobe/internal/probe"
)

func TestEscapeAnnotationData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text is untouched", "instance_ttl expired", "instance_ttl expired"},
		{"percent is escaped first", "100% full", "100%25 full"},
		{"newline becomes %0A", "line one\nline two", "line one%0Aline two"},
		{"carriage return becomes %0D", "a\rb", "a%0Db"},
		{"colon and comma are data, not syntax", "a:b,c", "a:b,c"},
		{"a percent before an escape-looking sequence is just a percent", "%0A", "%250A"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, escapeAnnotationData(tt.in))
		})
	}
}

func TestEscapeAnnotationProperty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain title", "soroprobe token: deployed", "soroprobe token%3A deployed"},
		{"comma separates properties", "a,b", "a%2Cb"},
		{"newline is escaped too", "a\nb", "a%0Ab"},
		{"percent is escaped first", "50%", "50%25"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, escapeAnnotationProperty(tt.in))
		})
	}
}

func TestWriteAnnotationShape(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writeAnnotation(&buf, "error", "soroprobe CDLZ: code_ttl", "expired at ledger 42")
	assert.Equal(t, "::error title=soroprobe CDLZ%3A code_ttl::expired at ledger 42\n", buf.String())
}

func TestEmitCheckAnnotationsOnlyInActions(t *testing.T) {
	t.Parallel()

	result := &probe.CheckResult{
		ContractID: "CDLZ",
		OK:         false,
		Checks: []probe.Check{
			{Name: "deployed", Outcome: probe.OutcomePass, Detail: "found"},
			{Name: "code_ttl", Outcome: probe.OutcomeFail, Detail: "expired at ledger 42"},
		},
	}

	var off bytes.Buffer
	emitCheckAnnotations(&off, result, false)
	assert.Empty(t, off.String(), "nothing is emitted outside GitHub Actions")

	var on bytes.Buffer
	emitCheckAnnotations(&on, result, true)
	out := on.String()
	assert.Contains(t, out, "::error title=soroprobe CDLZ%3A code_ttl::expired at ledger 42")
	assert.NotContains(t, out, "deployed", "passing steps are not annotated")
}

func TestEmitCheckAnnotationsWarnsOnTtlWarning(t *testing.T) {
	t.Parallel()

	result := &probe.CheckResult{
		ContractID: "CDLZ",
		OK:         true,
		Checks: []probe.Check{
			{Name: "instance_ttl", Outcome: probe.OutcomeWarn, Detail: "expiring in 2 days"},
		},
	}

	var buf bytes.Buffer
	emitCheckAnnotations(&buf, result, true)
	assert.Contains(t, buf.String(), "::warning title=soroprobe CDLZ%3A instance_ttl::expiring in 2 days")
}

func TestEmitBatchAnnotationsOnePerBadEntry(t *testing.T) {
	t.Parallel()

	result := &probe.BatchResult{
		Items: []probe.BatchItem{
			{Name: "token", Result: &probe.CheckResult{OK: true}},
			{Name: "vault", Result: &probe.CheckResult{
				OK:     false,
				Checks: []probe.Check{{Name: "code_ttl", Outcome: probe.OutcomeFail, Detail: "expired"}},
			}},
			{Name: "oracle", Error: "check timed out after 5s"},
		},
	}

	var buf bytes.Buffer
	emitBatchAnnotations(&buf, result, true)
	out := buf.String()
	assert.Contains(t, out, "::error title=soroprobe vault%3A code_ttl::expired")
	assert.Contains(t, out, "::warning title=soroprobe oracle::check timed out after 5s")
	assert.NotContains(t, out, "token", "passing entries are not annotated")
}

func TestFirstFailingPrefersAFailureOverAWarning(t *testing.T) {
	t.Parallel()

	r := &probe.CheckResult{Checks: []probe.Check{
		{Name: "instance_ttl", Outcome: probe.OutcomeWarn, Detail: "expiring"},
		{Name: "code_ttl", Outcome: probe.OutcomeFail, Detail: "expired"},
	}}
	assert.Equal(t, "code_ttl", firstFailing(r).Name)

	warnOnly := &probe.CheckResult{Checks: []probe.Check{
		{Name: "instance_ttl", Outcome: probe.OutcomeWarn, Detail: "expiring"},
	}}
	assert.Equal(t, "instance_ttl", firstFailing(warnOnly).Name)

	// A not-OK result with no failing or warning step still yields one
	// annotation rather than silently emitting none.
	empty := &probe.CheckResult{OK: false}
	assert.Equal(t, "check", firstFailing(empty).Name)
}
