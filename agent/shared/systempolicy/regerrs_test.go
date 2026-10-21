package systempolicy

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

// A policy whose registry writes fail must say so: the Windows policy
// functions used to return nil whatever happened, and the console showed the
// policy as applied.
func TestRegErrsReportsFailuresButNotAbsentValues(t *testing.T) {
	var ok regErrs
	ok.add("EnableFirewall", nil)
	ok.add("DisableAntiSpyware", fmt.Errorf("delete: %w", fs.ErrNotExist))
	if ok.err() != nil {
		t.Fatalf("successful writes and deleting an absent value reported %v", ok.err())
	}

	var bad regErrs
	bad.add("EnableFirewall", errors.New("Access is denied."))
	bad.add("netsh advfirewall", errors.New("exit status 1"))
	err := bad.err()
	if err == nil || !strings.Contains(err.Error(), "EnableFirewall: Access is denied.") || !strings.Contains(err.Error(), "netsh advfirewall") {
		t.Fatalf("err = %v, want both failures named", err)
	}
}
