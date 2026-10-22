package devicemanagement

import (
	"context"
	"time"
)

// MembershipObserver is told which devices just joined or left a group. A module
// whose policies can target a group re-sends what those devices should now
// enforce: group membership used to change only in device_group_members, so a
// device moved into a group with a USB block kept its old policy until it next
// reconnected or someone pressed Sync.
//
// The interface lives here, with the event, so this module does not import the
// policy modules (they already import it); cmd/server wires the two together.
type MembershipObserver interface {
	DevicesRegrouped(ctx context.Context, deviceIDs []string)
}

// regroupNotifyTimeout bounds one round of observer work. Re-dispatching is a
// compile and a socket send per online device, so it is not done on the
// request path.
const regroupNotifyTimeout = 2 * time.Minute

// WithMembershipObservers registers the modules to tell about membership changes.
func (h *inventoryHandler) WithMembershipObservers(obs ...MembershipObserver) *inventoryHandler {
	h.observers = append(h.observers, obs...)
	return h
}

// notifyRegrouped tells every observer, in the background, which devices
// changed groups. The request that changed membership has already succeeded and
// must not wait on, or fail because of, a re-dispatch.
func (h *inventoryHandler) notifyRegrouped(deviceIDs []string) {
	if len(h.observers) == 0 || len(deviceIDs) == 0 {
		return
	}
	ids := append([]string(nil), deviceIDs...)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), regroupNotifyTimeout)
		defer cancel()
		for _, o := range h.observers {
			o.DevicesRegrouped(ctx, ids)
		}
	}()
}
