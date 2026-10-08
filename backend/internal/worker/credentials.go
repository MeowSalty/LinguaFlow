package worker

import "github.com/MeowSalty/LinguaFlow/backend/internal/credential"

// SetCredentials methods are construction-time dependency injection. Call before
// starting workers; all server runners share the same live credential service.
func (f *EngineFactory) SetCredentials(reader credential.Reader, checker credential.Checker) {
	f.reader = reader
	f.checker = checker
}
func (r *JobRunner) SetCredentials(reader credential.Reader, checker credential.Checker) {
	r.credentialReader = reader
	r.credentialChecker = checker
}
func (r *PreviewRunner) SetCredentials(reader credential.Reader, checker credential.Checker) {
	r.factory.SetCredentials(reader, checker)
}
func (r *RevisionPreviewRunner) SetCredentials(reader credential.Reader, checker credential.Checker) {
	r.factory.SetCredentials(reader, checker)
}
func (r *QuickTranslateRunner) SetCredentials(reader credential.Reader, checker credential.Checker) {
	r.factory.SetCredentials(reader, checker)
}
