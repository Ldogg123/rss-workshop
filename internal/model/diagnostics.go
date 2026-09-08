package model

import "time"

// RunDiagnostics describes a bounded fetch/extraction trace with short field
// samples. Capture and storage redact sensitive strings; full source bodies,
// request headers, cookies, and reader links are never recorded here.
type RunDiagnostics struct {
	Version       int            `json:"version"`
	RecipeVersion int            `json:"recipe_version"`
	RequestedMode string         `json:"requested_mode"`
	SelectorType  string         `json:"selector_type"`
	Started       time.Time      `json:"started"`
	DurationMS    int64          `json:"duration_ms"`
	Attempts      []FetchAttempt `json:"attempts"`
}

type FetchAttempt struct {
	Mode            string   `json:"mode"`
	Outcome         string   `json:"outcome"` // success, failed, or not_modified
	Stage           string   `json:"stage"`   // fetch or extract
	Status          int      `json:"status"`  // source HTTP status, 0 if unavailable
	DurationMS      int64    `json:"duration_ms"`
	Bytes           int      `json:"bytes"`
	Matches         *int     `json:"matches,omitempty"`
	Valid           *int     `json:"valid,omitempty"`
	Filtered        int      `json:"filtered"`
	Items           *int     `json:"items,omitempty"`
	Error           string   `json:"error,omitempty"`
	Warnings        []string `json:"warnings"`
	WarningsOmitted int      `json:"warnings_omitted"`
}

type Run struct {
	ID          int64           `json:"id"`
	Ended       time.Time       `json:"ended"`
	Status      int             `json:"status"`
	Count       int             `json:"count"`
	Error       string          `json:"error"`
	Diagnostics *RunDiagnostics `json:"diagnostics,omitempty"`
}
