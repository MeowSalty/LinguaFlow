package service

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

// A previously accepted execution uses its own bindings, independently of the
// creator's later membership. API authorization is performed by the caller.
func (s *JobService) checkExecutionPolicies(ctx context.Context, job *ent.Job) error {
	spec, err := GetSnapshot(job)
	if err != nil {
		return err
	}
	check := func(b BackendSnapshot) error {
		if s.backends == nil || s.backends.Credentials() == nil {
			return credential.ErrUnavailable
		}
		ep, _ := b.Options["base_url"].(string)
		return s.backends.Credentials().Check(ctx, b.Credential, b.ID, b.Type, ep)
	}
	for _, round := range spec.Rounds {
		if round.Mode != "correct" {
			if err := check(round.Backend); err != nil {
				return err
			}
		}
	}
	if spec.RubyRetry != nil && spec.RubyRetry.Enabled {
		return check(spec.RubyRetry.Backend)
	}
	return nil
}
