package systempolicy

import (
	"errors"
	"io/fs"
	"strings"
)

// regErrs collects the failures of one policy's registry writes so the policy
// can report all of them, not just the first.
type regErrs []string

// add records err under what. Deleting a value that does not exist is the state
// the delete was after, so a not-found error is not a failure (on Windows,
// ERROR_FILE_NOT_FOUND matches fs.ErrNotExist).
func (r *regErrs) add(what string, err error) {
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return
	}
	*r = append(*r, what+": "+err.Error())
}

// err is nil when every write succeeded.
func (r regErrs) err() error {
	if len(r) == 0 {
		return nil
	}
	return errors.New(strings.Join(r, "; "))
}
