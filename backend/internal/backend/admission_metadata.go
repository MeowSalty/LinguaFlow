package backend

// Metadata is the fixed typed backing of the original frozen member slots.
// It is separate from O, candidate headers, and the Job's main K/E ledger.
type requestMetadataCredit struct {
	members         int
	maximum, actual int64
	dispatched      bool
	settled         bool
}

// RequestMemberMetadataBound includes promotion members (4+4096n), request
// members (32+2048n), debits (4+1024n), segment IDs (4+21n), status n and
// corrected index 16n. The typed string/identity widths are validated by the
// persistence adapter. These bounds already cover SQL JSON separators; the
// fields contain bounded integers, never arbitrary floating-point tokens.
func RequestMemberMetadataBound(members int) (int64, error) {
	if members < 0 || members > 65536 {
		return 0, ErrBuildCapacity
	}
	if members == 0 {
		return 0, nil
	}
	n, err := CheckedProduct(int64(members), 7206)
	if err != nil {
		return 0, err
	}
	return checkedSum(n, 44)
}

func (a *RequestAdmission) prepareRootMetadataLocked(id string, members int) (bool, error) {
	maximum, err := RequestMemberMetadataBound(members)
	if err != nil {
		return false, err
	}
	if existing := a.requestMetadata[id]; existing != nil {
		if existing.members != members {
			return false, ErrPipelineCapacity
		}
		return false, nil
	}
	if maximum == 0 {
		return false, nil
	}
	if a.requestMetadata == nil {
		a.requestMetadata = map[string]*requestMetadataCredit{}
	}
	a.requestMetadata[id] = &requestMetadataCredit{members: members, maximum: maximum, settled: true}
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
	maximum, err := RequestMemberMetadataBound(members)
	if err != nil || id == "" || actual < 0 || actual > maximum {
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
	a.requestMetadata[id] = &requestMetadataCredit{members: members, maximum: actual, actual: actual, dispatched: true, settled: true}
	return nil
}

// SetRequestMetadataBytes accepts authoritative post-transaction retained
// facts inside the already-paid table bound. It does not refund the live
// maximum while a response can still be saved or promoted.
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
	if credit == nil || credit.members != members && !(members == 0 && actual == 0) || actual < 0 || actual > credit.maximum {
		return ErrPipelineCapacity
	}
	credit.actual = actual
	credit.settled = true
	return nil
}

// MarkRequestMetadataUncertain precedes a SQL write. If its ACK/fact query is
// lost, cleanup conservatively retains the paid maximum until scalar rebuild.
func (a *RequestAdmission) MarkRequestMetadataUncertain(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if credit := a.requestMetadata[id]; credit != nil {
		credit.settled = false
	}
}

func (a *RequestAdmission) releaseRootMetadataLocked(id string) {
	if credit := a.requestMetadata[id]; credit != nil {
		// Both quantities were already included in the checked root vector;
		// transferring actual into base cannot increase their sum.
		retained := credit.actual
		if !credit.settled {
			retained = credit.maximum
		}
		next, err := checkedSum(a.rootMetadataBase, retained)
		if err != nil {
			return
		} // Keep the charged record if an invariant fails.
		a.rootMetadataBase = next
		delete(a.requestMetadata, id)
	}
}
