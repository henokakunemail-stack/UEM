package securityaudit

import "time"

type CheckCategory string

const (
	CategoryCoreApp   CheckCategory = "core_app"
	CategoryData      CheckCategory = "data_security"
	CategoryNetwork   CheckCategory = "network"
	CategoryPipeline  CheckCategory = "pipeline"
	CategoryHardening CheckCategory = "hardening"
)

type CheckStatus string

const (
	StatusPass CheckStatus = "pass"
	StatusWarn CheckStatus = "warn"
	StatusFail CheckStatus = "fail"
	// StatusNotVerified is a control this audit cannot observe at runtime: a
	// property of the server's code rather than of this deployment. It is
	// listed so the operator knows it exists, and it is left out of the score.
	// It used to be reported as "pass" with a fixed sentence of evidence,
	// which made the grade say more than anything had checked.
	StatusNotVerified CheckStatus = "not_verified"
)

type CheckItem struct {
	ID          int           `json:"id"`
	Part        int           `json:"part"` // 1 = Core App & Data, 2 = Network, Pipeline & Hardening
	Number      int           `json:"number"`
	Title       string        `json:"title"`
	Category    CheckCategory `json:"category"`
	Status      CheckStatus   `json:"status"`
	Description string        `json:"description"`
	Evidence    string        `json:"evidence"`
	Remediation string        `json:"remediation"`
	OWASPTag    string        `json:"owasp_tag"`
}

type AuditReport struct {
	GeneratedAt time.Time `json:"generated_at"`
	Grade       string    `json:"grade"` // "A+", "A", "B", "C", "F"
	Score       int       `json:"score"` // 0..100, over the evaluated checks only
	TotalChecks int       `json:"total_checks"`
	Passed      int       `json:"passed"`
	Warned      int       `json:"warned"`
	Failed      int       `json:"failed"`
	// NotVerified counts the checks listed but not evaluated; Evaluated is
	// the rest, and the only checks the score and grade are computed from.
	NotVerified int         `json:"not_verified"`
	Evaluated   int         `json:"evaluated"`
	Checks      []CheckItem `json:"checks"`
}
