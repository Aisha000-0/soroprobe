package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/soroworks/soroprobe/internal/probe"
)

// annotationEnv is the variable GitHub Actions sets in every step. Its
// presence — not its value — is what signals that workflow commands will be
// understood, so `GITHUB_ACTIONS=false` still counts as inside Actions.
const annotationEnv = "GITHUB_ACTIONS"

// escapeAnnotationData escapes a workflow-command message or property value.
// GitHub decodes these in order, so % must be escaped first. See
// https://docs.github.com/actions/reference/workflow-commands-for-github-actions
func escapeAnnotationData(s string) string {
	r := strings.NewReplacer(
		"%", "%25",
		"\r", "%0D",
		"\n", "%0A",
	)
	return r.Replace(s)
}

// escapeAnnotationProperty additionally covers the characters that terminate
// or separate the properties of a command (: and ,).
func escapeAnnotationProperty(s string) string {
	r := strings.NewReplacer(
		"%", "%25",
		"\r", "%0D",
		"\n", "%0A",
		":", "%3A",
		",", "%2C",
	)
	return r.Replace(s)
}

// writeAnnotation emits one workflow command, which GitHub renders as an
// annotation on the run summary.
func writeAnnotation(w io.Writer, level, title, message string) {
	fmt.Fprintf(w, "::%s title=%s::%s\n", //nolint:errcheck // annotations are best-effort output; a broken writer has no remedy.
		level,
		escapeAnnotationProperty(title),
		escapeAnnotationData(message),
	)
}

// emitCheckAnnotations writes ::error and ::warning lines for a check result,
// so a failing step shows why on the run summary without opening its log.
// Nothing is written when inActions is false.
func emitCheckAnnotations(w io.Writer, r *probe.CheckResult, inActions bool) {
	if !inActions {
		return
	}
	for _, c := range r.Checks {
		emitOneAnnotation(w, r.ContractID, c)
	}
}

// emitBatchAnnotations is the batch counterpart of emitCheckAnnotations: one
// annotation per check that failed or could not run, named by its entry name.
func emitBatchAnnotations(w io.Writer, r *probe.BatchResult, inActions bool) {
	if !inActions {
		return
	}
	for _, item := range r.Items {
		switch {
		case item.Error != "":
			// A check that could not run is a tool-level problem (exit 2),
			// not an unhealthy contract, so it becomes a warning.
			writeAnnotation(w, "warning", "soroprobe "+item.Name, item.Error)
		case !item.Result.OK:
			emitOneAnnotation(w, item.Name, firstFailing(item.Result))
		}
	}
}

// emitOneAnnotation annotates the step that made a single result not OK.
func emitOneAnnotation(w io.Writer, label string, c probe.Check) {
	switch c.Outcome {
	case probe.OutcomeFail:
		writeAnnotation(w, "error", "soroprobe "+label+": "+c.Name, c.Detail)
	case probe.OutcomeWarn:
		writeAnnotation(w, "warning", "soroprobe "+label+": "+c.Name, c.Detail)
	}
}

// firstFailing returns the step that decides a result's verdict: the first
// failure if there is one, otherwise the first warning. It falls back to a
// synthetic step so a not-OK result always yields exactly one annotation.
func firstFailing(r *probe.CheckResult) probe.Check {
	for _, c := range r.Checks {
		if c.Outcome == probe.OutcomeFail {
			return c
		}
	}
	for _, c := range r.Checks {
		if c.Outcome == probe.OutcomeWarn {
			return c
		}
	}
	return probe.Check{Name: "check", Outcome: probe.OutcomeFail, Detail: "check did not pass"}
}
