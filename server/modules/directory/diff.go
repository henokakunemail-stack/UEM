package directory

// diffContacts decides what a sync would do, without touching the database or
// the network.
//
// It is a pure function on purpose. Everything interesting about directory sync
// is wrong in the *comparison* — the same person arriving twice, a name change
// that should not count as a new person, a contact who vanished — and none of
// that needs a live AD server to test. Only the Search that produces `incoming`
// needs one, and that is behind the directoryClient interface.
//
// The comparison is on the fields a directory owns. IsActive and the
// timestamps are deliberately excluded: those are this server's to set, so
// including them would make every contact look changed on every sync and turn
// every run into N pointless writes.
func diffContacts(existing, incoming []Contact) SyncPlan {
	plan := SyncPlan{
		Adds:             []Contact{},
		Updates:          []Contact{},
		Deactivations:    []Contact{},
		TotalInDirectory: len(incoming),
	}

	byExternalID := make(map[string]Contact, len(existing))
	for _, c := range existing {
		byExternalID[c.ExternalID] = c
	}

	seen := make(map[string]bool, len(incoming))
	for _, in := range incoming {
		// A directory that returns the same person twice is a real occurrence
		// (paging over a reorg, two matching search bases). First one wins;
		// letting both through would try to insert the same UNIQUE
		// external_id twice and abort the whole transaction.
		if seen[in.ExternalID] {
			continue
		}
		seen[in.ExternalID] = true

		cur, known := byExternalID[in.ExternalID]
		if !known {
			plan.Adds = append(plan.Adds, in)
			continue
		}
		if contactFieldsDiffer(cur, in) || !cur.IsActive {
			// Keep the row's own identity and history; only the directory's
			// own fields move. Losing first_seen_at would make "new contact
			// each week" look true for a fleet that never changed.
			//
			// `|| !cur.IsActive` is the reactivation case: somebody who left
			// and came back has identical directory fields, so a
			// fields-only comparison would call them unchanged and leave them
			// permanently absent from the PIC dropdown.
			in.ID = cur.ID
			in.FirstSeenAt = cur.FirstSeenAt
			in.IsActive = true
			plan.Updates = append(plan.Updates, in)
			continue
		}
		plan.Unchanged++
	}

	// Anything stored but not in this result is gone from the directory.
	// Deactivated, not deleted: hardware_assets.assigned_user stores a
	// display name, so the row has to survive for that history to still mean
	// something after the person leaves the company.
	for _, c := range existing {
		if seen[c.ExternalID] {
			continue
		}
		if !c.IsActive {
			continue // already deactivated; re-reporting it every sync is noise
		}
		plan.Deactivations = append(plan.Deactivations, c)
	}

	return plan
}

func contactFieldsDiffer(a, b Contact) bool {
	return a.DistinguishedName != b.DistinguishedName ||
		a.DisplayName != b.DisplayName ||
		a.Email != b.Email ||
		a.Department != b.Department ||
		a.Title != b.Title ||
		a.Username != b.Username ||
		a.Source != b.Source
}
