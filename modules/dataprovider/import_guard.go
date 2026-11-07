package dataprovider

// CheckEmptyImportTarget is the shared native-v2 guard to call INSIDE the
// admitted CAS callback, after validating/staging the entire archive. It neither
// parses an archive nor compares its inert revision to the target revision.
// EnsureEmpty outside the transaction cannot substitute for this check.
func CheckEmptyImportTarget(current Snapshot, archiveBoard Board) error {
	if err := ValidateScope(archiveBoard.Scope); err != nil {
		return err
	}
	if current.Board != archiveBoard {
		return NewError(ScopeConflict)
	}
	if len(current.Elements) != 0 || len(current.Provenance) != 0 || len(current.Resources) != 0 {
		return NewError(AlreadyExists)
	}
	return nil
}
