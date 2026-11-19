package backend

// Metadata is the fixed typed backing of the original frozen member slots.
// It is separate from O, candidate headers, and the Job's main K/E ledger.
type requestMetadataCredit struct {
	members    int
	actual     int64
	dispatched bool
	settled    bool
	loaned     bool
	retired    bool
}

func (a *RequestAdmission) prepareRootMetadataLocked(id string, members int) (bool, error) {
	if members < 0 || id == "" {
		return false, ErrBuildCapacity
	}
	if existing := a.requestMetadata[id]; existing != nil {
		if existing.members != members {
			return false, ErrPipelineCapacity
		}
		return false, nil
	}
	if members == 0 {
		return false, nil
	}
	if a.requestMetadata == nil {
		a.requestMetadata = map[string]*requestMetadataCredit{}
	}
	a.requestMetadata[id] = &requestMetadataCredit{members: members, settled: true, loaned: true}
	return true, nil
}

// RestoreRootMetadata installs the body-free aggregate of retained request
// metadata. Pure terminal audit columns belong only to logical storage.
func (a *RequestAdmission) RestoreRootMetadata(bytes int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if bytes < 0 || a.root != nil || len(a.requestMetadata) > 0 {
		return ErrPipelineCapacity
	}
	a.rootMetadataBase = bytes
	return nil
}

// RestoreRequestMetadata transfers one original member table out of restored
// scalar stock. No root is increased when a reliable response is reattached.
func (a *RequestAdmission) RestoreRequestMetadata(id string, members int, actual int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if members < 0 || id == "" || actual < 0 {
		return ErrPipelineCapacity
	}
	if a.root == nil || a.rootScope.JobID == 0 {
		return nil
	}
	if existing := a.requestMetadata[id]; existing != nil {
		if existing.members != members || existing.actual != actual {
			return ErrPipelineCapacity
		}
		return nil
	}
	if actual > a.rootMetadataBase {
		return ErrPipelineCapacity
	}
	if a.requestMetadata == nil {
		a.requestMetadata = map[string]*requestMetadataCredit{}
	}
	a.rootMetadataBase -= actual
	a.requestMetadata[id] = &requestMetadataCredit{members: members, actual: actual, dispatched: true, settled: true}
	return nil
}

// SetRequestMetadataBytes accepts authoritative post-transaction retained
// facts. Current working copies use the shared actual meter; no future member
// width is used to prepay memory. Existing restored stock retains actual bytes.
func (a *RequestAdmission) SetRequestMetadataBytes(id string, members int, actual int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.root == nil {
		return nil
	}
	credit := a.requestMetadata[id]
	if credit == nil && actual == 0 {
		return nil
	} // Pure terminal audit owns no P.
	if credit == nil || credit.members != members && !(members == 0 && actual == 0) || actual < 0 {
		d := RejectionEvidence{RequestID: id, RequestedMembers: int64(members), RequestedBytes: actual, InventoryKnown: credit != nil}
		if credit != nil {
			d.InventoryMembers, d.InventoryBytes = int64(credit.members), credit.actual
		}
		return a.rejectLocked(ErrPipelineCapacity, "request_metadata_facts_mismatch", "settle_request_metadata", d)
	}
	if credit.retired {
		if a.closed && actual != credit.actual {
			return a.rejectLocked(ErrAdmissionClosed, "request_metadata_admission_closed", "settle_request_metadata", RejectionEvidence{RequestID: id, InventoryKnown: true, InventoryMembers: int64(credit.members), RequestedMembers: int64(members), InventoryBytes: credit.actual, RequestedBytes: actual})
		}
		attached := false
		if !a.closed {
			_, attached = a.root.Usage(a.rootScope, a.rootID)
		}
		if !attached && actual != credit.actual {
			return a.rejectLocked(ErrAdmissionClosed, "request_metadata_root_detached", "settle_request_metadata", RejectionEvidence{RequestID: id, InventoryKnown: true, InventoryMembers: int64(credit.members), RequestedMembers: int64(members), InventoryBytes: credit.actual, RequestedBytes: actual})
		}
		next, err := checkedSum(a.rootMetadataBase, actual)
		if err != nil {
			return err
		}
		oldBase := a.rootMetadataBase
		a.rootMetadataBase = next
		delete(a.requestMetadata, id)
		if attached {
			if err := a.syncRootLocked(0); err != nil {
				a.rootMetadataBase = oldBase
				a.requestMetadata[id] = credit
				return err
			}
		}
		return nil
	}
	credit.actual = actual
	credit.settled = true
	return nil
}

// MarkRequestMetadataUncertain precedes a SQL write. If its ACK/fact query is
// lost, cleanup retains only the most recent authoritative byte fact.
func (a *RequestAdmission) MarkRequestMetadataUncertain(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if credit := a.requestMetadata[id]; credit != nil {
		credit.settled = false
	}
}

func (a *RequestAdmission) releaseRootMetadataLocked(id string) {
	if credit := a.requestMetadata[id]; credit != nil {
		if !credit.settled {
			// A source can retire while its durable write is still unconfirmed.
			// Keep only the original slots and last authoritative scalar, so a
			// later storage settlement can resolve it without rearming that source.
			credit.retired, credit.loaned = true, false
			return
		}
		// Ended working owners transfer their authoritative retained SQL fact.
		retained := credit.actual
		next, err := checkedSum(a.rootMetadataBase, retained)
		if err != nil {
			return
		} // Keep the charged record if an invariant fails.
		a.rootMetadataBase = next
		delete(a.requestMetadata, id)
	}
}
