// Package diagnostics bounds the metadata retained for troubleshooting runs.
package diagnostics

import (
	"regexp"
	"strings"
	"time"
	"unicode"

	"rss-workshop/internal/model"
)

const MaxWarnings = 20

var (
	urls    = regexp.MustCompile(`(?i)\b(?:https?|postgres(?:ql)?|wss?)://[^\s<>"']+`)
	headers = regexp.MustCompile(`(?im)\b(authorization|proxy-authorization|cookie|set-cookie)\s*:\s*[^\r\n]+`)
	secrets = regexp.MustCompile(`(?i)\b(password|access[_-]?token|refresh[_-]?token|api[_-]?key)\s*=\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)`)
)

// SafeText redacts absolute URLs and credential header values. It preserves
// readable errors while bounding their encoded size for both stores.
func SafeText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	value = strings.ToValidUTF8(value, "\uFFFD")
	value = urls.ReplaceAllString(value, "[URL omitted]")
	value = headers.ReplaceAllString(value, "$1: [redacted]")
	value = secrets.ReplaceAllString(value, "$1=[redacted]")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return '\uFFFD'
		}
		return r
	}, value)
	if len(value) > limit {
		value = strings.ToValidUTF8(value[:limit], "")
	}
	return value
}

func mode(value string) string {
	switch value {
	case "static", "browser", "auto", "flaresolverr":
		return value
	case "":
		return "static"
	default:
		return "unknown"
	}
}

func count(value *int, maximum int) *int {
	if value == nil {
		return nil
	}
	n := min(max(*value, 0), maximum)
	return &n
}

// Sanitize returns a detached copy so later edits cannot alter a recorded run.
func Sanitize(value *model.RunDiagnostics) *model.RunDiagnostics {
	if value == nil {
		return nil
	}
	out := *value
	out.Version = 1
	out.RecipeVersion = max(value.RecipeVersion, 0)
	out.RequestedMode = mode(value.RequestedMode)
	if out.SelectorType != "css" && out.SelectorType != "xpath" {
		out.SelectorType = "unknown"
	}
	if value.Started.Year() < 1 || value.Started.Year() > 9999 {
		out.Started = time.Time{}
	} else {
		out.Started = value.Started.UTC()
	}
	out.DurationMS = min(max(value.DurationMS, 0), 3600000)
	out.Attempts = make([]model.FetchAttempt, 0, min(len(value.Attempts), 2))
	for _, attempt := range value.Attempts[:min(len(value.Attempts), 2)] {
		attempt.Mode = mode(attempt.Mode)
		switch attempt.Outcome {
		case "success", "failed", "not_modified":
		default:
			attempt.Outcome = "unknown"
		}
		if attempt.Stage != "fetch" && attempt.Stage != "extract" {
			attempt.Stage = "unknown"
		}
		if attempt.Status < 100 || attempt.Status > 599 {
			attempt.Status = 0
		}
		attempt.DurationMS = min(max(attempt.DurationMS, 0), 3600000)
		attempt.Bytes = min(max(attempt.Bytes, 0), 8<<20)
		attempt.Matches = count(attempt.Matches, 1000000)
		attempt.Items = count(attempt.Items, 1000)
		attempt.Error = SafeText(attempt.Error, 1000)
		warnings := make([]string, 0, min(len(attempt.Warnings), MaxWarnings))
		for _, warning := range attempt.Warnings[:min(len(attempt.Warnings), MaxWarnings)] {
			warnings = append(warnings, SafeText(warning, 512))
		}
		attempt.WarningsOmitted = min(min(max(attempt.WarningsOmitted, 0), 1000000)+min(max(len(attempt.Warnings)-MaxWarnings, 0), 1000000), 1000000)
		attempt.Warnings = warnings
		out.Attempts = append(out.Attempts, attempt)
	}
	return &out
}
