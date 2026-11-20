// This is the additional required matrix, not an inventory of every dynamic t.Run.
export const modulePath = 'github.com/MeowSalty/LinguaFlow/backend';
export const workerPackage = `${modulePath}/internal/worker`;
export const epubTop = 'TestAllocationEPUBWorkerEndToEnd';
export const epubLeaves = [
  `${epubTop}/dense_256_seven_keys_512_words`,
  `${epubTop}/synthetic_book_5338_word_bounded_batches`,
];
export const requiredTests = {
  [`${modulePath}/internal/worker`]: [
    ...epubLeaves,
    'TestPersistenceTranslationContract',
    'TestMemberResponseWorkSQLite',
    'TestMemberPublicationCapabilityRejectedBeforeDispatch',
    'TestMemberSystemRejectionPreservesPaidResponseAcrossRecovery',
    'TestMemberSavedBusinessCanonicalLoweredJRecovery',
    'TestRubyHandoffAcceptanceHTTPResponseFitsRButOneMemberExceedsQ',
    ...['same_round_two_resources', 'different_rounds_C2_C32', 'two_jobs_shared_backend', 'two_jobs_shared_RPM', 'cold_backend_warm_backend'].map(name => `TestRubyHandoffAcceptanceHTTPResourceAndJobScopes/${name}`),
  ],
  [`${modulePath}/internal/pipeline`]: [
    'TestMemberResponseWorkMemory',
    'TestMemberRecoveryCompletesBeforePromotion',
    'TestMemberRecoveryAttemptKeepsOneDeadlineAcrossCheckpointAndPromotion',
    ...['exit', 'completed_without_claim', 'claim_and_release'].map(name => `TestStagedWindowForeignScopeLoadingWaitsForClaimOrExit/${name}`),
    ...['same_pool_source_attempt', 'word_split_priority', 'oversize_first', 'full_leftover_before_S', 'fallback_shrink', 'future_ready', 'pool_barrier', 'empty_state', 'noncanonical_context', 'revision_words_span', 'active_candidate_exclusions'].map(name => `TestIncrementalMainSchedulerOracle/${name}`),
    ...[32, 64, 128, 256].map(size => `TestIncrementalMainSchedulerWorkBound/${size}`),
    ...['self_only', 'self_only_empty_group', 'earlier_deadline_ready', 'empty_group_rebuilt', 'rebuilt_then_empty'].map(name => `TestDeferredMainPlanReturnedWaitKeepsSemanticVersion/${name}`),
    'TestOwnedDeferredMainPlanLifecycle',
    'TestMainSchedulerContextRangeExcludesSaturatedMain',
    'TestMainSchedulerRunSummarySaturation',
    'TestStagedWindowActiveOwnerWaitsWithSameBatchAndAttempt',
    'TestStagedWindowWaitingPauseCancelLeavesMembersAndCredit',
    'TestStagedMainConcurrencyIgnoresUnrelatedByteWake',
    'TestStagedUnqueuedMainConcurrencyIgnoresUnrelatedByteWake',
    'TestColdMainSchedulerSealedRevisionWaitsForBuildStation',
    ...['main_concurrency', 'backend_rpm', 'paused', 'admission_closed', 'cancelled', 'invalid_round'].map(name => `TestPreparationUnqueuedWaitPreservesAdmissionCause/${name}`),
    'TestPreparationReadyQueueKeepsHeadPriority',
    'TestResourceBudgetStoppedProducerYieldsTurnBeforePaidWriterJoins',
  ],
  [`${modulePath}/internal/backend`]: [
    ...['prepare', 'grant_rollback', 'rename', 'fresh'].map(name => `TestDeferredPlanKeysBorrowCanonicalInventoryBacking/${name}`),
  ],
  [`${modulePath}/internal/workstate`]: [
    ...['same_transaction_projection', 'metadata_missing', 'metadata_mismatch', 'charged_field', 'clear_field', 'identity_change', 'scope_edge', 'scope_scalar', 'second_mutation', 'second_charged_mutation', 'second_identity_mutation'].map(name => `TestStorageBeforeIdentitySQLWork/${name}`),
    'TestStorageBeforeIdentityUnknownACK',
  ],
  [`${modulePath}/internal/api`]: [
    'TestStreamTerminationAcceptsOnlyHTTPAbort',
    ...['realtime', 'healthy_return', 'write_boundary_abort'].map(name => `TestJobStreamFreezesOriginalSessionExpiry/${name}`),
    ...['healthy_return', 'write_boundary_abort', 'pending_abort'].map(name => `TestJobStreamHTTPWriterSessionExpiry/http1/${name}`),
    ...['healthy_return', 'write_boundary_abort'].map(name => `TestJobStreamHTTPWriterSessionExpiry/http2/${name}`),
    ...['write_budget', 'session_expiry'].map(name => `TestJobStreamWriterSuccessfulFlushAtDeadlineDoesNotReset/${name}`),
    ...['write', 'flush'].map(name => `TestJobStreamWriterLocalExpiryWithPendingAbortsConnectionBuffer/${name}`),
    'TestJobStreamWriterSessionExpiryNeverResumes',
    ...['pending', 'panic', 'header_panic'].map(name => `TestJobStreamWriterPendingAndPanicAbort/${name}`),
    'TestJobStreamWriterExpiredSessionNeverStarts',
    'TestJobStreamIOWindowsAndIdleRevocation',
    ...['1h0m0s', '1s'].map(name => `TestJobStreamBlockedWriteKeepsDeadlineDuringRevocation/${name}`),
    ...['revocation_at_page_end', 'canceled_within_page'].map(name => `TestJobStreamReplayPageChecksAccessBeforeFlush/${name}`),
    ...['set_deadline', 'write', 'short_write', 'flush', 'clear_deadline', 'expired_after_write'].map(name => `TestJobStreamWriterFailureDoesNotRecover/${name}`),
  ],
};

// A skip must match both its named test and the documented reason. Descendant
// rules cover fixture-generated subcases; the actual full names are recorded.
const rules = [];
const add = (pkg, names, reason, condition = 'always', descendants = false) => {
  for (const test of names) rules.push({ package: `${modulePath}/internal/${pkg}`, test, reason, condition, descendants });
};
add('worker', ['TestRubyHandoffAcceptanceMainCrashHelper', 'TestRubyBatchHTTPProcessCrashHelper'], 'subprocess helper');
add('workstate', ['TestBatchWorkstateCrashHelper', 'TestWorkstateCrashHelper'], 'subprocess helper');
add('database', ['TestSQLiteConcurrentOpenProcessHelper'], 'SQLite subprocess helper');
add('worker', ['TestPersistenceTranslationLoadMatrix'], 'long translation matrix:');
add('worker', ['TestPersistenceLoadMatrix'], 'long extraction matrix:');
add('worker', ['TestPersistenceSharedConcurrencyCapacityObservations'], 'full capacity and observer acceptance:');
add('service', ['TestTaskHistoryLargeAtomicDelete'], 'set LINGUAFLOW_CAPACITY_TESTS=1', 'capacity-off');
add('database', ['TestPostgresTimeConnectionsAndPrecision', 'TestPostgresMigrationLock', 'TestPostgresIntegration', 'TestMigrationPinnedConnectionPostgres', 'TestAuthFormatPostgresIsolationAndConcurrency'], 'LINGUAFLOW_TEST_POSTGRES_DSN is not set', 'postgres-off', true);
add('service', ['TestCredentialLifecyclePostgres', 'TestPostgresJobTimeBoundariesAndSessionExpiry', 'TestInitializationIndependentPostgresConnections', 'TestGlossarySyncPostgresIndependentTransactionCompetition', 'TestOrganizationPostgresIndependentTransactionCompetition', 'TestOperationsPostgresTimePagination', 'TestStoragePostgresDeletionWaitsForMaintenanceGate', 'TestTaskRetentionIndependentPostgresTransactions', 'TestTaskHistorySQLiteRevokeAfterRepeatedWriteConflicts'], 'LINGUAFLOW_TEST_POSTGRES_DSN is not set', 'postgres-off', true);
add('service', ['TestAuthenticationConcurrency', 'TestAuthenticationRevocationAndDeadlines', 'TestRegistrationAtomicAndUnverifiedEmailDoesNotClaimAddress', 'TestTaskHistoryPostgresTransactionCompetition'], 'LINGUAFLOW_TEST_POSTGRES_DSN is not set', 'postgres-off', true);
add('workstate', ['TestIndependentDatabaseCommitArbitration'], 'LINGUAFLOW_TEST_POSTGRES_DSN is not set', 'postgres-off', true);
add('migration/upgrade/v015', ['TestPostgresInplaceExternalRestoreAcceptance', 'TestPostgresAutoInplaceDumpOnlyAcceptance', 'TestPostgresServeReleaseDumpRestoreAcceptance'], 'unverified:', 'always', true);
add('migration/cli', ['TestPostgresStorageProtectionAcceptance'], 'requires the isolated PostgreSQL acceptance Task');
add('migration/cli', ['TestBuiltCLIReleaseAcceptance'], 'run task backend:migrate:test:binary');
add('migration/cli', ['TestExactLegacyV1BinaryInterruptedOperationAcceptance'], 'preserved original v0.16 binary explicitly missing in external clean CI');
add('migration/snapshot', ['TestPostgresCatalogRejectsLiveUnfingerprintedObjects'], 'dedicated catalog PostgreSQL DSN is not configured');
add('cli', ['TestBindListenerKeepsRequestedPort'], 'allocated port leaves no sequential candidate range');
add('cli', ['TestBindListenerIPv6AndBrowserURL'], 'IPv6 loopback unavailable:');
add('config', ['TestReferencesRejectMixingAndEscape'], 'symlink unavailable:', 'always', true);
add('store/localstore', ['TestSymlinkCannotEscapeRoot'], 'symlink creation not permitted:');
add('service', ['TestStorageRetirementAvailabilityResolvesSpaceConnection'], 'IDs coincide; regression still covered by other cases', 'always', true);
add('sysmem', ['TestReadRSSWithRealProc'], '非 Linux 环境，跳过', 'windows');
add('migration/upgrade/v013', ['TestLocalWindowsRejectsSymlink', 'TestUnpublishedStagingCleanupRejectsLinkedDirectory', 'TestSQLiteRejectsLinkedSidecarsBeforeOpeningSource'], 'symlink privilege unavailable:', 'windows', true);
add('migration/upgrade/v013', ['TestLocalMigrationRejectedUnderModifyOnlyParent'], 'requires a non-elevated token', 'windows');
add('migration/upgrade/v013', ['TestSQLiteWindowsPathSpellingAllowsStandardPaths'], 'does not provide an 8.3 alias', 'windows', true);
add('migration/operation', ['TestValidatePathRejectsSymlinkReparsePoint'], 'Windows denied symlink creation', 'windows');
add('credential', ['TestPreparePrivateDirectoryChangesOwnerWhenElevated'], 'requires an already elevated token', 'windows');
add('credential', ['TestPrivateCreationUnderModifyOnlyParent', 'TestPreparePrivateDirectoryRepairsModifyOnlyOwner'], 'must run with a non-elevated token', 'windows');
add('service', [
  'TestAuthConnectionCloseRevokesRefreshRace/postgres',
  'TestAuthenticationB1MigrationPreservesStageA/postgres',
  'TestAuthenticationB4/postgres',
  'TestAuthenticationB4OAuthAtomicCompletion/postgres',
  'TestAuthenticationB4OAuthCompletePasswordPolicySaveRace/postgres',
  'TestAuthenticationB4OAuthCompleteSignupRuleSaveRace/postgres',
  'TestAuthenticationB4OAuthFailedFreshRetestPreservesCurrentCapability/postgres',
  'TestAuthenticationB4OAuthFreshDowngradeNeedsMaintenanceFallback/postgres',
  'TestAuthenticationB4OAuthIdentityAndStateIsolation/postgres',
  'TestAuthenticationB4OAuthIdentityCannotReplaceNewBinding/postgres',
  'TestAuthenticationB4OAuthOIDCWithoutAuthTime/postgres',
  'TestAuthenticationB4OAuthPolicyDependencies/postgres',
  'TestAuthenticationB4OAuthRulesAndRefreshEvidence/postgres',
  'TestAuthenticationB4OAuthSignupAndMaintenance/postgres',
  'TestAuthenticationB5ConfirmationCancellationRace/postgres',
  'TestAuthenticationB5CredentialConsumptionRollback/postgres',
  'TestAuthenticationB5ExternalSignupMatrix/postgres',
  'TestAuthenticationB5InvalidSignupDataPrecedesAdmission/postgres',
  'TestAuthenticationB5LinkProofAtomicCreationAndInitializationFields/postgres',
  'TestAuthenticationB5LocalDisableAndUnlinkRace/postgres',
  'TestAuthenticationB5LocalPolicyAndSignup/postgres',
  'TestAuthenticationB5LoginPolicyAndPasswordDeleteRace/postgres',
  'TestAuthenticationB5PasswordDisableAndRestore/postgres',
  'TestAuthenticationB5PasswordReenableKeepsRevocation/postgres',
  'TestAuthenticationB5RecoveryAndPolicySaveRace/postgres',
  'TestAuthenticationB5RecoveryWriteRollback/postgres',
  'TestAuthenticationB5RegistrationPublicationRace/postgres',
  'TestAuthenticationB5SignupAuditRollback/postgres',
  'TestAuthenticationB5SignupLocalDependencyAndMaintenance/postgres',
  'TestAuthenticationB5SignupRevisionIsolation/postgres',
  'TestAuthenticationIdentityModel/postgres',
  'TestAuthenticationIdentityUsernameRegistrationConcurrency/postgres',
  'TestAuthenticationPasswordSnapshotRaces/postgres',
  'TestAuthenticationRecoveryAuditRollback/postgres',
  'TestAuthenticationRecoveryRejectsExpiredRevokedAndTargetChanges/postgres',
  'TestAuthenticationRecoveryRejectsStaleAuthorization/postgres',
  'TestAuthenticationRecoveryRejectsTargetAndAuthorizationChanges/postgres',
  'TestAuthenticationRecoveryRejectsTargetAuthorizationChanges/postgres',
  'TestAuthenticationRecoverySingleUseRace/postgres',
  'TestAuthRulesPurposeAndEvidenceExpiry/postgres',
  'TestAuthRulesSaveAndCompleteRace/postgres',
  'TestAuthRulesSaveAndFallbackRemovalRace/postgres',
  'TestAuthRulesSaveAndRefreshRace/postgres',
  'TestAuthRulesSaveRechecksExpiredEvidence/postgres',
  'TestAuthRulesUpdateUnknownAndFallback/postgres',
  'TestExternalAuthSecretStorageAndCleanup/postgres',
  'TestExternalAuthStateMachineConcurrency/postgres',
  'TestLoginBindingReleaseAndOldTicketInvalidation/postgres',
  'TestLoginMethodRemovalRequiresAlternativeProof/postgres',
  'TestReauthenticationProofConcurrentConsumption/postgres',
], 'PostgreSQL authentication concurrency unverified: LINGUAFLOW_TEST_POSTGRES_DSN is not set', 'postgres-off', true);
add('service', ['TestStorageDeploymentPostgresCapacityAndLeaseConcurrency', 'TestStorageDeploymentS3AuthorizationRevocation', 'TestStorageDeploymentS3RetirementReleasesExactMarker', 'TestStorageDeploymentS3ReadonlyMigrationCutoverAndCleanup'], 'real deployment not verified: set LINGUAFLOW_STORAGE_ACCEPTANCE=1 and isolated test environment', 'storage-acceptance-off');
add('store/s3store', ['TestStorageDeploymentS3ProviderContract'], 'real deployment not verified: set LINGUAFLOW_STORAGE_ACCEPTANCE=1 and isolated test environment', 'storage-acceptance-off');
export const skipRules = rules;
