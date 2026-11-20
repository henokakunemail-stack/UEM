package compliance

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Built-in regulatory templates. A JSON file with the same id in
// DATA_DIR/compliance-templates replaces the built-in one, and a new id adds a
// framework, both without rebuilding the server: templates are re-read on
// every request.
//
//go:embed frameworks/*.json
var builtinFrameworks embed.FS

// Control assessment statuses, in addition to the evidence statuses.
const AssessOutOfScope = "out_of_scope"

// Coverage says how much of a control the application can evidence at all.
const (
	CoverageFull    = "full"
	CoveragePartial = "partial"
)

// EvidenceRef is one evidence source a control relies on. In a template it is
// either a bare string or {"source": ..., "params": {...}}.
type EvidenceRef struct {
	Source string         `json:"source"`
	Params map[string]any `json:"params,omitempty"`
}

// UnmarshalJSON accepts the string shorthand.
func (e *EvidenceRef) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		e.Source = s
		return nil
	}
	type plain EvidenceRef
	return json.Unmarshal(b, (*plain)(e))
}

func (e EvidenceRef) key() string {
	if len(e.Params) == 0 {
		return e.Source
	}
	b, _ := json.Marshal(e.Params)
	return e.Source + string(b)
}

// TemplateControl is one control or article of a framework.
type TemplateControl struct {
	Ref   string `json:"ref"`
	Group string `json:"group,omitempty"`
	Title string `json:"title"`
	// Verified false means the reference (article number) has not been
	// checked against the official text; the report prints the topic only.
	Verified *bool         `json:"verified,omitempty"`
	Evidence []EvidenceRef `json:"evidence,omitempty"`
	// Coverage partial caps the result at "partial": the application can
	// only evidence part of what the control asks.
	Coverage         string `json:"coverage,omitempty"`
	Note             string `json:"note,omitempty"`
	OutOfScopeReason string `json:"out_of_scope_reason,omitempty"`
}

// RefVerified is the reference's verification state; unset means verified.
func (c TemplateControl) RefVerified() bool { return c.Verified == nil || *c.Verified }

// Template is a regulatory framework as data.
type Template struct {
	ID         string `json:"id"`
	Family     string `json:"family"`
	Name       string `json:"name"`
	Regulation string `json:"regulation"`
	// EffectiveFrom / EffectiveUntil (YYYY-MM-DD, inclusive) choose between
	// versions of one family by the report period.
	EffectiveFrom           string            `json:"effective_from,omitempty"`
	EffectiveUntil          string            `json:"effective_until,omitempty"`
	ReviewedAt              string            `json:"reviewed_at"`
	SourceNote              string            `json:"source_note,omitempty"`
	Pending                 []string          `json:"pending,omitempty"`
	DefaultOutOfScopeReason string            `json:"default_out_of_scope_reason,omitempty"`
	Controls                []TemplateControl `json:"controls"`
	// Origin is "builtin" or "custom"; File is where a custom one came from.
	Origin string `json:"origin"`
	File   string `json:"file,omitempty"`
}

// TemplateError is a template file that could not be used.
type TemplateError struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

func parseDate(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, reportZone)
}

// Validate rejects a template a report could not be built from honestly.
func (t *Template) Validate() error {
	var problems []string
	if strings.TrimSpace(t.ID) == "" {
		problems = append(problems, "id is required")
	}
	if strings.TrimSpace(t.Name) == "" {
		problems = append(problems, "name is required")
	}
	if strings.TrimSpace(t.Family) == "" {
		t.Family = t.ID
	}
	for _, d := range []struct{ name, v string }{{"effective_from", t.EffectiveFrom}, {"effective_until", t.EffectiveUntil}, {"reviewed_at", t.ReviewedAt}} {
		if d.v != "" {
			if _, err := parseDate(d.v); err != nil {
				problems = append(problems, d.name+" must be YYYY-MM-DD")
			}
		}
	}
	if len(t.Controls) == 0 {
		problems = append(problems, "controls must not be empty")
	}
	seen := map[string]bool{}
	for i, c := range t.Controls {
		where := fmt.Sprintf("controls[%d] (%s)", i, c.Ref)
		if strings.TrimSpace(c.Ref) == "" || strings.TrimSpace(c.Title) == "" {
			problems = append(problems, where+": ref and title are required")
		}
		if seen[c.Ref] {
			problems = append(problems, where+": duplicate ref")
		}
		seen[c.Ref] = true
		if c.Coverage != "" && c.Coverage != CoverageFull && c.Coverage != CoveragePartial {
			problems = append(problems, where+": coverage must be full or partial")
		}
		for _, e := range c.Evidence {
			if !KnownEvidence(e.Source) {
				problems = append(problems, where+": unknown evidence source "+e.Source)
			}
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// TemplateDir is where custom templates live.
func (g *Generator) TemplateDir() string { return filepath.Join(g.DataDir, "compliance-templates") }

func decodeTemplate(b []byte) (Template, error) {
	var t Template
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return t, err
	}
	return t, t.Validate()
}

// LoadTemplates reads the built-in templates and then the custom directory.
// A custom template with a built-in's id replaces it. A file that does not
// parse or validate is reported and left out; it never half-loads.
func (g *Generator) LoadTemplates() ([]Template, []TemplateError) {
	byID := map[string]Template{}
	var errs []TemplateError
	_ = fs.WalkDir(builtinFrameworks, "frameworks", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		b, _ := builtinFrameworks.ReadFile(path)
		t, err := decodeTemplate(b)
		if err != nil {
			errs = append(errs, TemplateError{File: "builtin:" + path, Error: err.Error()})
			return nil
		}
		t.Origin = "builtin"
		byID[t.ID] = t
		return nil
	})
	if g.DataDir != "" {
		entries, err := os.ReadDir(g.TemplateDir())
		if err != nil && !os.IsNotExist(err) {
			errs = append(errs, TemplateError{File: g.TemplateDir(), Error: err.Error()})
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			path := filepath.Join(g.TemplateDir(), e.Name())
			b, err := os.ReadFile(path)
			if err == nil && len(b) > 2<<20 {
				err = errors.New("file larger than 2 MB")
			}
			var t Template
			if err == nil {
				t, err = decodeTemplate(b)
			}
			if err != nil {
				errs = append(errs, TemplateError{File: e.Name(), Error: err.Error()})
				continue
			}
			t.Origin, t.File = "custom", e.Name()
			byID[t.ID] = t
		}
	}
	out := make([]Template, 0, len(byID))
	for _, t := range byID {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Family != out[j].Family {
			return out[i].Family < out[j].Family
		}
		return out[i].EffectiveFrom < out[j].EffectiveFrom
	})
	return out, errs
}

// periodEnd is the last day of a YYYY-MM period.
func periodEnd(period string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01", period, reportZone)
	if err != nil {
		return t, fmt.Errorf("period must be YYYY-MM")
	}
	return t.AddDate(0, 1, -1), nil
}

func (t Template) covers(day time.Time) bool {
	if t.EffectiveFrom != "" {
		if from, err := parseDate(t.EffectiveFrom); err == nil && day.Before(from) {
			return false
		}
	}
	if t.EffectiveUntil != "" {
		if until, err := parseDate(t.EffectiveUntil); err == nil && day.After(until) {
			return false
		}
	}
	return true
}

// SelectTemplate picks a template by exact id, or by family: the version
// whose effective range contains the last day of the period.
func SelectTemplate(ts []Template, key, period string) (Template, error) {
	for _, t := range ts {
		if t.ID == key {
			return t, nil
		}
	}
	day, err := periodEnd(period)
	if err != nil {
		return Template{}, err
	}
	found := false
	for _, t := range ts {
		if t.Family != key {
			continue
		}
		found = true
		if t.covers(day) {
			return t, nil
		}
	}
	if found {
		return Template{}, fmt.Errorf("no version of %s is in force on %s", key, day.Format("2006-01-02"))
	}
	return Template{}, ErrNotFound
}

// ControlAssessment is one control's result in a framework report.
type ControlAssessment struct {
	Ref         string           `json:"ref"`
	RefVerified bool             `json:"ref_verified"`
	Group       string           `json:"group,omitempty"`
	Title       string           `json:"title"`
	Status      string           `json:"status"`
	Coverage    string           `json:"coverage,omitempty"`
	Note        string           `json:"note,omitempty"`
	Reason      string           `json:"reason,omitempty"`
	Evidence    []EvidenceResult `json:"evidence,omitempty"`
}

// combineEvidence reduces the evidence of one control. Every source must
// support it for "supported"; a gap with nothing in place is "gap"; no data
// at all is "no_data"; anything mixed is "partial".
func combineEvidence(ev []EvidenceResult) string {
	var sup, part, gap, none int
	for _, e := range ev {
		switch e.Status {
		case EvidenceSupported:
			sup++
		case EvidencePartial:
			part++
		case EvidenceGap:
			gap++
		default:
			none++
		}
	}
	switch {
	case len(ev) == 0:
		return EvidenceNoData
	case sup == len(ev):
		return EvidenceSupported
	case none == len(ev):
		return EvidenceNoData
	case sup == 0 && part == 0:
		return EvidenceGap
	default:
		return EvidencePartial
	}
}

// FrameworkSummary counts controls by status.
type FrameworkSummary struct {
	Controls   int `json:"controls"`
	Supported  int `json:"supported"`
	Partial    int `json:"partial"`
	Gap        int `json:"gap"`
	NoData     int `json:"no_data"`
	OutOfScope int `json:"out_of_scope"`
}

// Assess evaluates every control of a template.
func (ec *EvidenceContext) Assess(ctx context.Context, t Template) ([]ControlAssessment, []EvidenceResult, FrameworkSummary) {
	cache := map[string]EvidenceResult{}
	var order []string
	collect := func(ref EvidenceRef) EvidenceResult {
		k := ref.key()
		if r, ok := cache[k]; ok {
			return r
		}
		r := ec.Collect(ctx, ref)
		cache[k] = r
		order = append(order, k)
		return r
	}
	var out []ControlAssessment
	var sum FrameworkSummary
	for _, c := range t.Controls {
		a := ControlAssessment{Ref: c.Ref, RefVerified: c.RefVerified(), Group: c.Group, Title: c.Title,
			Coverage: c.Coverage, Note: c.Note}
		if len(c.Evidence) == 0 {
			a.Status = AssessOutOfScope
			a.Reason = c.OutOfScopeReason
			if a.Reason == "" {
				a.Reason = t.DefaultOutOfScopeReason
			}
			if a.Reason == "" {
				a.Reason = "Tidak dapat dibuktikan dari data Endpoint Manager."
			}
			sum.OutOfScope++
		} else {
			for _, ref := range c.Evidence {
				a.Evidence = append(a.Evidence, collect(ref))
			}
			a.Status = combineEvidence(a.Evidence)
			// The application only reaches part of this control: even perfect
			// evidence cannot claim all of it.
			if a.Status == EvidenceSupported && c.Coverage == CoveragePartial {
				a.Status = EvidencePartial
			}
			switch a.Status {
			case EvidenceSupported:
				sum.Supported++
			case EvidencePartial:
				sum.Partial++
			case EvidenceGap:
				sum.Gap++
			default:
				sum.NoData++
			}
		}
		sum.Controls++
		out = append(out, a)
	}
	catalog := make([]EvidenceResult, 0, len(order))
	for _, k := range order {
		catalog = append(catalog, cache[k])
	}
	return out, catalog, sum
}
