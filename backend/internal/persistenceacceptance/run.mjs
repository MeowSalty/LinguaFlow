import { access, mkdir, mkdtemp, open, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
import { createRuntime } from '../authacceptance/runtime.mjs';
import { run } from '../authacceptance/prepare.mjs';
import { initializeSamples, validateMeasurements } from './measurements.mjs';

const backendDir = resolve(fileURLToPath(new URL('../..', import.meta.url)));
const sourceRoot = resolve(backendDir, '..');
const flags = process.argv.slice(2);
for (const flag of flags) {
  if (!['--postgres', '--bench', '--protocol'].includes(flag)) throw new Error('Unknown persistence acceptance flag: ' + flag);
}
if (new Set(flags).size !== flags.length) throw new Error('Duplicate persistence acceptance flag');
const postgres = flags.includes('--postgres');
const bench = flags.includes('--bench');
const protocol = flags.includes('--protocol');
if (bench && protocol) throw new Error('--bench and --protocol select different scopes and cannot be combined');
const scope = protocol ? 'protocol' : bench ? 'bench' : 'full';
const reportRoot = process.env.LINGUAFLOW_PERSISTENCE_OUTPUT || await mkdtemp(join(tmpdir(), 'linguaflow-persistence-'));
await mkdir(reportRoot, { recursive: true });
const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('LINGUAFLOW_') && !key.startsWith('PG')));
env.LINGUAFLOW_PERSISTENCE_DRIVER = postgres ? 'postgres' : 'sqlite3';
env.LINGUAFLOW_PERSISTENCE_REPORT = join(reportRoot, 'samples.jsonl');
// Samples are appended by Go fixtures, but acceptance belongs to this run only.
await initializeSamples(env.LINGUAFLOW_PERSISTENCE_REPORT);
const runtime = postgres ? await createRuntime() : null;
let status = 'failed';
let results = [];
let command = [];
try {
  if (postgres) {
    const bin = process.env.LINGUAFLOW_AUTH_POSTGRES_BIN || join(backendDir, 'bin', '.migration-postgres-18.6-cache', 'tools', 'pgsql', 'bin');
    await access(join(bin, process.platform === 'win32' ? 'postgres.exe' : 'postgres'));
    process.env.LINGUAFLOW_AUTH_POSTGRES_BIN = bin;
    env.LINGUAFLOW_TEST_POSTGRES_DSN = (await runtime.postgres()).dsn;
  }
  const protocols = 'Persistence|Scheduler|Preparation|ReadyHint|RequestAdmission|RequestBackend|RequestSession|RequestAccounting|Runtime|StagedRecoveryCommit|StagedStorage|StagedFixedRecoveryCapacity|FixedUnclaimedRecovery|StagedWindow|CandidateWindow|RoundStoreWindow|StoredRound|CheckpointBarrier|DBReporter|BatchRequest|CommitAcknowledgementLost|AtomicConfirmation|NoopRejects|CancellationOrders|Bulk|Abort|TransactionRetryHasOneOwner|UnknownVerification|ReservationAcknowledgement|ExpiredAttempt|LostRecordAndAbort|JoinedRollback|TimeDriver|TimeTransaction|RubyHandoffAcceptance(RecoveryOriginalMemberBoundary|RunnerFuturePinsLoweredWindow|RunnerFuturePinsAggregateCapacity|RunnerFixedFuturePrefixMakesProgress|RunnerFixedFuture16PreservesPaidBatch|RunnerEmptyEarlyRoundsDrainFuturePins|RunnerOneResourceSlotDrainsOtherRaw16)' + (postgres ? '|IndependentDatabaseCommitArbitration' : '');
  const pattern = bench ? '^TestPersistence(HotPathCosts|SealAndCheckpointCosts|LoadMatrix|TranslationLoadMatrix)$' : '^Test(' + protocols + ')';
  const packages = bench ? './internal/worker ./internal/pipeline' : './internal/worker ./internal/workstate ./internal/pipeline ./internal/progress ./internal/backend ./internal/database ./internal/persistence';
  const skip = protocol ? " -skip '^TestPersistence(LoadMatrix|TranslationLoadMatrix)$'" : '';
  const args = "-count=1 -timeout=45m -v -json -run '" + pattern + "'" + skip + ' ' + packages;
  const output = await open(join(reportRoot, 'tests.jsonl'), 'w', 0o600);
  command = [postgres && !bench ? 'backend:test:race' : 'backend:test', 'CLI_ARGS=' + args];
  try {
    await run('task', command, { cwd: sourceRoot, env, stdio: ['ignore', output.fd, output.fd] });
  } finally { await output.close(); }
  results = (await readFile(join(reportRoot, 'tests.jsonl'), 'utf8')).split(/\r?\n/).flatMap(line => { try { return [JSON.parse(line)]; } catch { return []; } });
  if (results.some(event => event.Action === 'skip' && event.Test)) throw new Error('Required persistence acceptance test skipped');
  const required = ['TestPersistenceHotPathCosts', 'TestPersistenceSealAndCheckpointCosts'];
  if (!protocol) {
    required.push('TestPersistenceLoadMatrix', 'TestPersistenceTranslationLoadMatrix');
    for (const delay of [0, 1, 2, 5]) required.push('TestPersistenceLoadMatrix/delay' + delay);
    for (const delay of [0, 5]) required.push('TestPersistenceTranslationLoadMatrix/delay' + delay);
  }
  if (protocol) required.push('TestPersistenceDelayHonorsContext');
  if (!bench) required.push('TestPersistenceSharedConcurrencyReachesTen', 'TestPersistenceReservationProtocol', 'TestPersistenceFencesAndPauseHandoff', 'TestPersistenceCandidateCursorAtomicity', 'TestPersistenceUnknownCommit', 'TestPersistenceAtomicChunkFailure','TestPersistenceLegacyCheckpointUnknownCommit','TestPersistenceReservationUnknownBoundaries','TestPersistenceCommitObservations','TestPreparationCancelledHeadDoesNotStarveReadyResource','TestSchedulerReliableBurstStillServicesNormal','TestStagedRecoveryCommitUsesActualResourceAndRequestMetadata','TestRuntimePreservesUpstreamObserverWithoutDoubleCounting','TestRequestAccountingLifetimeRetainsUpstreamObserver','TestRuntimeProductionObserverAndOriginalHandoffBudget','TestRuntimeReporterSavingObservesCheckpointSQLAndLifetime');
  if (postgres && !bench) required.push('TestPersistencePostgresLockAndDisconnect', 'TestIndependentDatabaseCommitArbitration');
  if (!bench) required.push('TestTimeDriverExecutionBudgetRequiresOwner', 'TestTimeTransactionKeepsCallerDeadlineCause');
  if (!bench) required.push('TestPersistenceTranslationContract');
  if (!bench) required.push('TestPersistenceRoundMemberPageBounds');
  if (!bench) required.push('TestPersistenceCommitEncodedQualityIssues', 'TestPersistenceStoredEncodedQualityIssues');
  if (!bench) required.push('TestStagedStorageFailureStopsSharedProducerAndPreservesPaidFacts', 'TestStagedStorageControlErrorsDoNotCloseSharedDispatch', 'TestStagedStorageCallerCancellationDoesNotCloseSharedDispatch');
  if (!bench) required.push('TestStagedFixedRecoveryCapacityPrecedesBlockedPreparation', 'TestRubyHandoffAcceptanceRecoveryOriginalMemberBoundary', 'TestRubyHandoffAcceptanceRunnerFuturePinsLoweredWindow', 'TestRubyHandoffAcceptanceRunnerFuturePinsAggregateCapacity', 'TestRubyHandoffAcceptanceRunnerFixedFuturePrefixMakesProgress', 'TestRubyHandoffAcceptanceRunnerEmptyEarlyRoundsDrainFuturePins', 'TestRubyHandoffAcceptanceRunnerOneResourceSlotDrainsOtherRaw16');
  if (!bench) required.push('TestFixedUnclaimedRecoveryTransfersOnlyActualRestoredMembers', 'TestFixedUnclaimedRecoveryRejectsNoSingletonSpace');
  if (!bench) required.push('TestRubyHandoffAcceptanceRunnerFixedFuture16PreservesPaidBatch/S17', 'TestRubyHandoffAcceptanceRunnerFixedFuture16PreservesPaidBatch/S32');
  if (!bench) required.push('TestPersistenceRuntimeInventoryBoundedByteState');
  if (!bench) required.push('TestStagedWindowLargeBatchCompletesAndResumes', 'TestStagedWindowLargeBatchPreservesFuturePin', 'TestStagedWindowRejectsOversizedSingleton', 'TestStagedWindowActiveOwnerWaitsWithSameBatchAndAttempt', 'TestStagedWindowInactiveRecoveryOwnerYieldsThenResumes', 'TestStagedWindowWaitingPauseCancelLeavesMembersAndCredit');
  if (!bench) required.push('TestStagedWindowSameResourceFutureRoundWithoutClaimMakesProgress', 'TestStagedWindowCurrentScopeRecoveryPageClaimsConsumer', 'TestStagedWindowMultipleWaitersDoNotWakeEachOthersPreparation');
  if (!bench) required.push('TestStagedWindowPartialFuturePinCompletesEarlierAndRecoversLater', 'TestStagedWindowPinAndActiveOccupancyWaitAfterFixedSplit', 'TestCandidateWindowFixedPinRequiresDurableScopeAndCompleteRecovery', 'TestStagedWindowFixedPinWaitPauseCancelPreservesBatch', 'TestRoundStoreWindowHeaderUsesDurableScopeIdentity');
  for (const name of required) {
    if (!results.some(event => event.Test === name && event.Action === 'pass')) throw new Error('Required acceptance fixture did not pass: ' + name);
  }
  const samples = await readFile(env.LINGUAFLOW_PERSISTENCE_REPORT, 'utf8');
  if (!samples.trim()) throw new Error('Acceptance did not produce required fixture samples');
  const measurements = samples.trim().split(/\r?\n/).map(line => JSON.parse(line));
  validateMeasurements(measurements, { driver: env.LINGUAFLOW_PERSISTENCE_DRIVER, protocol, bench });
  status = 'passed';
} finally {
  // The complete Go process has exited before closing the owned cluster.
  if (runtime) await runtime.close();
  if (!results.length) {
    try { results = (await readFile(join(reportRoot, 'tests.jsonl'), 'utf8')).split(/\r?\n/).flatMap(line => { try { return [JSON.parse(line)]; } catch { return []; } }); } catch { /* Startup failures have no test output. */ }
  }
  const manifestPath = process.env.LINGUAFLOW_PERSISTENCE_SOURCE_MANIFEST || null;
  const manifestHash = manifestPath ? createHash('sha256').update(await readFile(manifestPath)).digest('hex') : null;
  await writeFile(join(reportRoot, 'run.json'), JSON.stringify({ tested_at: new Date().toISOString(), status, scope, driver: env.LINGUAFLOW_PERSISTENCE_DRIVER, bench, source: sourceRoot, source_manifest: manifestPath, source_manifest_sha256: manifestHash, command: ['task', ...command], passed: results.filter(event => event.Action === 'pass' && event.Test).length, test_results: results.filter(event => event.Test && ['pass', 'fail', 'skip'].includes(event.Action)).map(event => ({ test: event.Test, action: event.Action })), samples: env.LINGUAFLOW_PERSISTENCE_REPORT }, null, 2) + '\n');
  console.log('Persistence acceptance report (' + scope + '): ' + reportRoot);
}
