package devicemanagement

import "testing"

func osWithDomain(domain string) string {
	if domain == "" {
		return `{"edition":"Pro","build_number":"10.0.26100"}`
	}
	return `{"edition":"Pro","build_number":"10.0.26100","domain":` + domain + `}`
}

// Joining or leaving a domain changes who can sign in to the machine, so it
// reaches the audit trail the way a hardware swap does.
func TestADomainJoinIsAuditWorthy(t *testing.T) {
	workgroup := `{"joined":false,"type":"workgroup","name":"WORKGROUP"}`
	corp := `{"joined":true,"type":"active_directory","name":"corp.example.com"}`

	d := diffInventory("{}", "{}", osWithDomain(workgroup), osWithDomain(corp))
	if !d.DomainChanged || !d.auditWorthy() {
		t.Fatalf("workgroup -> corp.example.com not flagged: %+v", d)
	}
	if got := d.summary(softwareDiff{}); got != "changed: domain" {
		t.Errorf("summary = %q", got)
	}
	if d := diffInventory("{}", "{}", osWithDomain(corp), osWithDomain(workgroup)); !d.DomainChanged {
		t.Error("leaving the domain not flagged")
	}
}

func TestAnUnchangedOrNewlyReportedDomainIsNotAChange(t *testing.T) {
	corp := `{"joined":true,"type":"active_directory","name":"corp.example.com"}`
	corpUpper := `{"joined":true,"type":"active_directory","name":"CORP.EXAMPLE.COM"}`
	if d := diffInventory("{}", "{}", osWithDomain(corp), osWithDomain(corpUpper)); d.DomainChanged {
		t.Error("the same domain in another case was flagged")
	}
	// An agent upgraded to one that reports the field: the previous snapshot
	// has none, and that is not a join.
	if d := diffInventory("{}", "{}", osWithDomain(""), osWithDomain(corp)); d.DomainChanged {
		t.Error("the first report carrying a domain was flagged as a change")
	}
}
