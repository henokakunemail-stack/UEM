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
	GeneratedAt time.Time   `json:"generated_at"`
	Grade       string      `json:"grade"` // "A+", "A", "B", "C", "F"
	Score       int         `json:"score"` // 0..100
	TotalChecks int         `json:"total_checks"`
	Passed      int         `json:"passed"`
	Warned      int         `json:"warned"`
	Failed      int         `json:"failed"`
	Checks      []CheckItem `json:"checks"`
}
