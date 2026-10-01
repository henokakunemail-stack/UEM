package devicemanagement

import "errors"

// ErrNotFound means the device (or token) does not exist.
var ErrNotFound = errors.New("device not found")

// ErrInvalidSecret means the presented device secret did not match.
var ErrInvalidSecret = errors.New("invalid device secret")

// ErrGroupInUse means a group is still the target of a deployment, schedule,
// filter policy, update campaign or maintenance job. Deleting it would leave
// those records pointing at a target that no longer resolves to any device, so
// the run would silently reach nobody.
var ErrGroupInUse = errors.New("group is still in use")
