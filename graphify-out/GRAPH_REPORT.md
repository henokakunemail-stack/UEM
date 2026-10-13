# Graph Report - Desktop Manage  (2026-10-13)

## Corpus Check
- 506 files · ~475,483 words
- Verdict: corpus is large enough that graph structure adds value.
- Unclassified: 18 file(s) not represented in the graph (top: (none) 7, .nsi 4, .css 2)

## Summary
- 4041 nodes · 15419 edges · 222 communities (150 shown, 72 thin omitted)
- Extraction: 95% EXTRACTED · 5% INFERRED · 0% AMBIGUOUS · INFERRED: 846 edges (avg confidence: 0.85)
- Token cost: 0 input · 0 output

## Graph Freshness
- Built from commit: `ffa2d073`
- Run `git rev-parse HEAD` and compare to check if the graph is stale.
- Run `graphify update .` after code changes (no API cost).

## Community Hubs (Navigation)
- server/main.go
- context.Context
- testing.T
- auth/session_test.go
- .Open
- github.com/jmoiron/sqlx.DB
- NewRepository
- types/api.ts
- usePermission
- Contact
- App.tsx
- maintenance.go
- buildServer
- Client
- WithRole
- UserIDFromContext
- chain_test.go
- time.Time
- user-management/handler_test.go
- maintenance/repository_test.go
- damage_test.go
- Handler
- WSHandler
- rcRouteFixture
- package.json
- scanner_windows_test.go
- LoginHandler
- Handler
- maintenance/repository.go
- Handler
- newPackageFixture
- loadEnvFile
- rebindConn
- InputEvent
- time.Duration
- net/http.ResponseWriter
- Repository
- Repository
- net/http.Handler
- Handler
- execFixture
- dialectFor
- go_pkg_net
- Handler
- os/exec.Cmd
- newConnectFixture
- net/http.Request
- react
- linux/osinfo_linux.go
- .Run
- AuthenticateAgent
- Repository
- ticket_test.go
- Handler
- config.go
- Claims
- securityheaders_test.go
- Repository
- github.com/gorilla/websocket.Conn
- Repository
- Handler
- Session
- Handler
- compilerOptions
- windows/inventory_windows.go
- linux/inventory_linux.go
- runProcess
- newQueueFixture
- net/http.HandlerFunc
- newSoftwareEnv
- DeviceDTO
- inventory/scheduler.go
- Handler
- networkfilter/engine_test.go
- capture_windows.go
- session0_windows.go
- Manager
- Handler
- TestE2EEnrollConnectCommand
- patch-management/model.go
- check-css.mjs
- Repository
- TestE2EGroups
- compilerOptions
- macos/inventory_darwin.go
- service.go
- inventory.go
- Engine
- process_test.go
- Repository
- enroll_test.go
- silent_uninstall_windows.go
- engine_windows.go
- Evaluator
- DataTable
- uninstall_test.go
- NewEngine
- newCampaignFixture
- 0001_init.sql
- software-deployment/sweep_test.go
- uninstallFixture
- scanner_linux.go
- installedProgram
- net/http.Client
- devicesQuery.ts
- runAgent
- remotecontrol/session_test.go
- Engine
- AuthContext.tsx
- Repository
- Modal
- MaintenancePage.tsx
- resolve.go
- scm_windows.go
- inventory_cim_test.go
- deployment_tasks
- dnsadapter.go
- crypto/ed25519.PublicKey
- NewWebSocketDialer
- 0006_patch_management.sql
- lifecycle_scope_test.go
- cleanup_locked_windows_test.go
- darwinManager
- linuxManager
- TaskQueue
- sync.RWMutex
- main
- TerminalRelay
- devices
- 0005_remote_execution.sql
- 0009_task_scheduler.sql
- newProgressFixture
- NewRepository
- CLAUDE.md
- mockReporter
- diffSoftware
- newUploadRouter
- 0002_device_mgmt.sql
- 0008_alerting.sql
- Conn
- NewHub
- setupTestServer
- 0013_asset_license_management.sql
- 0014_maintenance.sql
- stubHub
- bytesReader
- go_pkg_syscall
- output_test.go
- Read-AgentCommand
- 0011_network_filter.sql
- 0012_agent_self_update.sql
- .oxlintrc.json
- uninstall_darwin.go
- hwModel
- ReleaseManifest
- remote_control_sessions
- 0019_directory_sync.sql
- deploy/update-server.sh
- offlineHub
- sync.Mutex
- offlineHub
- scripts/update-server.sh
- 0024_system_policies.sql
- newSafeWebhookClient
- update.sh
- install-ubuntu.sh
- testHubRecorder
- darwin/build.sh
- uninstall-gui.sh
- capturingHub
- wedgedHub
- acceptingHub
- captureHub
- offlineHub
- 0017_wsticket.sql
- capturingDeploymentHub
- liveHub
- silentHub
- tsconfig.json
- linux/build.sh
- install-gui.sh
- install-agent.sh script
- install-server.sh script
- uninstall-agent.sh script
- uninstall-server.sh script
- github.com/henokakunemail-stack/Endpoint-Manager

## God Nodes (most connected - your core abstractions)
1. `UserIDFromContext()` - 73 edges
2. `buildServer()` - 72 edges
3. `NewRepository()` - 43 edges
4. `newEnv()` - 42 edges
5. `HashToken()` - 41 edges
6. `react` - 40 edges
7. `DataTable()` - 39 edges
8. `usePermission()` - 38 edges
9. `useToast()` - 37 edges
10. `Modal()` - 36 edges

## Surprising Connections (you probably didn't know these)
- `captureServer` --references--> `StepReport`  [EXTRACTED]
  agent/shared/maintenance/maintenance_test.go → server/modules/maintenance/model.go
- `init()` --calls--> `Init()`  [EXTRACTED]
  tests/integration/transport_test.go → server/core/logger/logger.go
- `mustList()` --references--> `Device`  [EXTRACTED]
  tests/unit/device_test.go → server/modules/device-management/model.go
- `env` --references--> `Repository`  [EXTRACTED]
  tests/integration/transport_test.go → server/modules/device-management/repository.go
- `newRemoteExecEnv()` --references--> `Repository`  [EXTRACTED]
  tests/integration/remote_exec_test.go → server/modules/device-management/repository.go

## Import Cycles
- None detected.

## Communities (222 total, 72 thin omitted)

### Community 0 - "server/main.go"
Cohesion: 0.07
Nodes (55): TestMain(), newPlatformRunner(), newPlatformRunner(), newPlatformRunner(), newPlatformRunner(), createRuleReq, diskTarget, patchTarget (+47 more)

### Community 1 - "context.Context"
Cohesion: 0.03
Nodes (43): AgentRelease, DeviceUpdateTask, discardAuditor, noopAuditor, Repository, UpdateCampaign, stubAudit, nopAuditor (+35 more)

### Community 2 - "testing.T"
Cohesion: 0.03
Nodes (136): Allowed(), assertDistinctMirrors(), NewEngine(), stepTimeout(), engineFor(), jsonTags(), newCaptureServer(), setModTime() (+128 more)

### Community 3 - "auth/session_test.go"
Cohesion: 0.12
Nodes (42): env, httpResult, TestSessionStore_CacheHitAndInvalidation(), SessionStore, NewSessionStore(), tokenPair, newEnv(), signLegacyAccess() (+34 more)

### Community 4 - ".Open"
Cohesion: 0.08
Nodes (39): Backup(), copyFile(), pruneOldest(), quickCheck(), Restore(), Snapshot(), StartBackupJob(), seedTestDevice() (+31 more)

### Community 5 - "github.com/jmoiron/sqlx.DB"
Cohesion: 0.05
Nodes (64): fakeRoles, Repository, auditAdapter, markOfflineBatch(), runOfflineSweep(), NewLoginHandler(), incidentCount(), newTickingEvaluator() (+56 more)

### Community 6 - "NewRepository"
Cohesion: 0.07
Nodes (54): auditWriter, mockExecHub, JWTService, TokenPair, NewJWTService(), NewHeartbeatFlusher(), TestHeartbeatFlusher_BatchExecution(), TestHeartbeatFlusher_RemovePreventsStaleOverwrite() (+46 more)

### Community 7 - "types/api.ts"
Cohesion: 0.07
Nodes (49): STATUS_CLASS, STATUS_LABEL, emptyConfig(), PlanTable(), SettingsPage(), INSTALL_ARG_PRESETS, PackageType, SoftwarePageProps (+41 more)

### Community 8 - "usePermission"
Cohesion: 0.11
Nodes (36): lucide-react, react-router-dom, BINARY, EnrollTokenModal(), EnrollTokenModalProps, ToastContext, ToastContextType, ToastItem (+28 more)

### Community 9 - "Contact"
Cohesion: 0.07
Nodes (26): Config, ConfigView, Contact, directoryClient, fakeClient, ldapClient, Repository, SyncPlan (+18 more)

### Community 10 - "App.tsx"
Cohesion: 0.07
Nodes (40): AgentUpdatesPage, AlertsPage, App(), AssetLicensePage, AuditPage, ConsoleRoot(), DashboardPage, DevicesPage (+32 more)

### Community 11 - "maintenance.go"
Cohesion: 0.14
Nodes (49): countOrphanedLaunchAgents(), darwinCleanupTemp(), darwinDiskCheck(), darwinFlushDNS(), darwinLogMaintenance(), darwinMemoryHygiene(), darwinPerUserTempDirs(), darwinSecurityAudit() (+41 more)

### Community 12 - "buildServer"
Cohesion: 0.06
Nodes (46): TestConfig_ReadsPostgresSettings(), TestOpenDatabase_DefaultsToSQLite(), TestOpenDatabase_IgnoresBareSQLiteDriver(), TestOpenDatabase_SQLiteDriverIsExplicit(), bootstrapAdmin(), buildServer(), mustJSON(), openDatabase() (+38 more)

### Community 13 - "Client"
Cohesion: 0.06
Nodes (28): NewEngine(), ExecuteAndReport(), ReportResult(), ToHTTPURL(), ExecuteInstall(), ReportProgress(), ToHTTPURL(), taskContext() (+20 more)

### Community 14 - "WithRole"
Cohesion: 0.13
Nodes (25): WithRole(), assertRefused(), assetFixture(), createAsset(), seedDevice(), TestADuplicateAssetTagIsRefusedWithAMessageNotAConstraintError(), TestAnAssetKeepingItsOwnTagIsNotAConflict(), TestAnAssetOutlivesTheDeviceItWasLinkedTo() (+17 more)

### Community 15 - "UserIDFromContext"
Cohesion: 0.12
Nodes (15): auditBridge, Handler, hubSender, inventoryHandler, Log(), UserIDFromContext(), toDTO(), writeErr() (+7 more)

### Community 16 - "chain_test.go"
Cohesion: 0.12
Nodes (37): ChainReport, chainLockFor(), entryHashOf(), nullDetails(), presentIn(), short(), tailHash(), dbFileOf() (+29 more)

### Community 17 - "time.Time"
Cohesion: 0.06
Nodes (20): AgentReleaseDTO, offlineTarget, LicenseAllocation, UpdateAssetRequest, Entry, verifyRow, Session, createEnrollTokenResp (+12 more)

### Community 18 - "user-management/handler_test.go"
Cohesion: 0.06
Nodes (69): addRuleReq, policyRow(), putPolicy(), scopedPolicyFixture(), TestAnEmptyPolicyUpdateChangesNothing(), TestAPartialPolicyUpdateKeepsTheScopeItDoesNotMention(), TestAPolicyUpdateRefusesAValueItCannotStore(), TestAScopedPolicyCanBeWidenedBackToTheWholeFleet() (+61 more)

### Community 19 - "maintenance/repository_test.go"
Cohesion: 0.12
Nodes (37): stubHub, call(), newHandlerTest(), newStubHub(), roleContext(), seedOnlineDevice(), seedTaskForReport(), TestAgentResultRejectsBodyTaskIDMismatch() (+29 more)

### Community 20 - "damage_test.go"
Cohesion: 0.12
Nodes (28): DecodeFrame(), DecodeFrameHeader(), EncodeFrame(), encodeRect(), NewDamage(), absInt(), solidFrame(), TestAdaptiveQualityReducesLargeDamagePayload() (+20 more)

### Community 21 - "Handler"
Cohesion: 0.20
Nodes (8): AuditLogger, DeviceValidator, Handler, HubDispatcher, NewHandler(), strPtr(), writeErr(), writeJSON()

### Community 22 - "WSHandler"
Cohesion: 0.12
Nodes (7): Envelope, FilterSyncer, InventoryReceiver, SystemPolicySyncer, TerminalReceiver, UpdateQueue, WSHandler

### Community 23 - "rcRouteFixture"
Cohesion: 0.07
Nodes (33): busyHub, nullAuditor, dev1Headers(), issueTicket(), mustExecDevices(), rcRouteFixture(), startSessionRequest(), TestAMalformedFrameFromTheOperatorIsIgnoredNotFatal() (+25 more)

### Community 24 - "package.json"
Cohesion: 0.06
Nodes (35): oxlint, react-dom, tailwindcss, @tailwindcss/vite, @types/node, @types/react, @types/react-dom, typescript (+27 more)

### Community 25 - "scanner_windows_test.go"
Cohesion: 0.15
Nodes (24): searchOf(), TestDriverUpdateIsClassifiedAsDriver(), TestHiddenUpdateIsSkipped(), TestInstallScriptSearchesEveryUpdateType(), TestSecurityCategoryIsFoundBeyondTheFirst(), update(), buildInstallScript(), installOSWithPrefix() (+16 more)

### Community 26 - "LoginHandler"
Cohesion: 0.12
Nodes (11): liveUser, LoginHandler, writeErr(), writeJSON(), SessionIDFromContext(), newJTI(), ClientIPResolver, parseTrustedProxy() (+3 more)

### Community 27 - "Handler"
Cohesion: 0.11
Nodes (15): AuditLogger, Handler, Hub, OriginChecker, NewOriginChecker(), validateOrigin(), ValidateWebSocketOrigin(), TestNewOriginChecker() (+7 more)

### Community 28 - "maintenance/repository.go"
Cohesion: 0.09
Nodes (20): DeviceTarget, Job, JobFilter, JobProgress, RunRequest, TargetType, Task, TaskFilter (+12 more)

### Community 29 - "Handler"
Cohesion: 0.32
Nodes (4): Handler, NewHandler(), writeErr(), writeJSON()

### Community 30 - "newPackageFixture"
Cohesion: 0.14
Nodes (26): msiFields(), newPackageFixture(), seedPackage(), TestADeploymentDispatchesToOnlineAgentsAndMarksThoseTasksDispatched(), TestADeploymentToAnAgentThatCannotRunItIsRefused(), TestADeploymentToNothingIsRefused(), TestAGroupTargetOnlyReachesItsOwnMembers(), TestAMissingPackageIs404OnBothReadAndDelete() (+18 more)

### Community 31 - "loadEnvFile"
Cohesion: 0.27
Nodes (9): TestLoadEnvFileAppliesKeyValues(), TestLoadEnvFileDoesNotOverrideARealEnvironmentValue(), TestLoadEnvFileRejectsAMalformedLine(), TestLoadEnvFileReportsAMissingFile(), ensureJWTSecret(), loadEnvFile(), TestEnsureJWTSecretLeavesAnOperatorSuppliedSecretAlone(), TestEnsureJWTSecretLeavesNoTempFile() (+1 more)

### Community 32 - "rebindConn"
Cohesion: 0.09
Nodes (7): rebindConn, rebindDriver, mockBindConn, mockBindDriver, rebind(), setupMockDB(), TestPolicyRepository_PostgresIntegerBinding()

### Community 33 - "InputEvent"
Cohesion: 0.09
Nodes (9): TestCapabilitiesAdvertisesTheCommandName(), NewPlatformCapturer(), TestAFailedProbeIsReportedVerbatim(), Capabilities, DarwinCapturer, fakeCapturer, InputEvent, LinuxCapturer (+1 more)

### Community 34 - "time.Duration"
Cohesion: 0.06
Nodes (25): ipRecord, BackupConfig, EnrollmentHandler, purgeSessions(), IPRateLimiter, NewClientIPResolver(), NewIPRateLimiter(), TestClientIP() (+17 more)

### Community 35 - "net/http.ResponseWriter"
Cohesion: 0.21
Nodes (10): writeJSON(), isRunnableScheduleType(), NewHandler(), normalizeTaskStatus(), writeErr(), writeJSON(), TestTheTypeTheSchedulerRunsIsAccepted(), TestScriptTypeAllowlist() (+2 more)

### Community 36 - "Repository"
Cohesion: 0.12
Nodes (13): TestCompileEffectivePolicy_Precedence(), PolicySettings, USBStorageMode, WallpaperStyle, WindowsUpdateMode, boolToInt(), ComputePolicyVersion(), NewID() (+5 more)

### Community 37 - "Repository"
Cohesion: 0.11
Nodes (10): TestANonIntervalScheduleHasNoNextRun(), boolToInt(), CalculateSHA256(), Repository, NewID(), nextRunAt(), ScheduledTaskDeviceRun, ScheduledTaskRun (+2 more)

### Community 38 - "net/http.Handler"
Cohesion: 0.05
Nodes (14): Handler, deny(), RequirePermission(), RequireRole(), TestRequirePermission(), TestUnknownPermissionPanics(), passthroughAuth(), NewHandler() (+6 more)

### Community 39 - "Handler"
Cohesion: 0.22
Nodes (7): AuditLogger, Handler, Hub, filterApplyEnvelope(), NewHandler(), writeErr(), writeJSON()

### Community 40 - "execFixture"
Cohesion: 0.08
Nodes (33): recordingAudit, SetWebSocketTicketIssuer(), DSNForPath(), execFixture(), execRow(), execSchema(), resultReportAs(), TestADispatchTheAgentAcceptsStillCreatesARunningExecution() (+25 more)

### Community 41 - "dialectFor"
Cohesion: 0.07
Nodes (33): dialect, overlayFS, dsn(), Open(), OpenWithDriver(), dialectFor(), migrationNames(), readMigration() (+25 more)

### Community 42 - "go_pkg_net"
Cohesion: 0.11
Nodes (14): TestChunkIPsCoversEverything(), applyFirewall(), chunkIPs(), isNotFound(), newFirewallBackend(), runNFT(), newFirewallBackend(), joinIPs() (+6 more)

### Community 43 - "Handler"
Cohesion: 0.18
Nodes (10): checkExtensionMatches(), DeviceLookup, NewHandler(), writeErr(), writeJSON(), NewID(), TestCheckExtensionMatches(), AuditLogger (+2 more)

### Community 44 - "os/exec.Cmd"
Cohesion: 0.10
Nodes (10): createShellCmd(), createShellCmd(), createShellCmd(), createShellCmd(), terminate(), processTree, newProcessTree(), processTree (+2 more)

### Community 45 - "newConnectFixture"
Cohesion: 0.10
Nodes (20): connectFixture, fakeHub, newDispatchFixture(), TestAutoDispatchOnPolicyDeleteRestoresAccess(), TestAutoDispatchOnRuleAddAndRuleDelete(), withUserCtx(), assertFilterMessage(), newConnectFixture() (+12 more)

### Community 46 - "net/http.Request"
Cohesion: 0.11
Nodes (13): Auditor, Handler, HubDispatcher, Auditor, Handler, RoleFromContext(), NewHandler(), writeJSON() (+5 more)

### Community 47 - "react"
Cohesion: 0.13
Nodes (22): react, AlertsList(), AlertsListProps, formatRelativeTime(), getIcon(), KPICard(), KPICardProps, FALLBACK (+14 more)

### Community 48 - "linux/osinfo_linux.go"
Cohesion: 0.14
Nodes (14): newOSInfoProvider(), newOSInfoProvider(), newOSInfoProvider(), New(), parseOSRelease(), readKey(), New(), swVers() (+6 more)

### Community 49 - ".Run"
Cohesion: 0.30
Nodes (7): StepReport, reportBackoff(), stepsFor(), TestTruncateBoundsAgentStrings(), truncate(), truncateLog(), Engine

### Community 50 - "AuthenticateAgent"
Cohesion: 0.14
Nodes (13): SecretLookup, AuditLogger, Handler, Hub, AuthenticateAgent(), TestAuthenticateAgent_AcceptsMatchingPair(), TestAuthenticateAgent_RejectsMissingCredentials(), TestAuthenticateAgent_RejectsSecretBoundToAnotherDevice() (+5 more)

### Community 51 - "Repository"
Cohesion: 0.11
Nodes (8): runDeploymentSweep(), Repository, AgentCommand, DeploymentTask, DeviceCapability, SoftwareDeployment, SoftwarePackage, TaskProgressReport

### Community 52 - "ticket_test.go"
Cohesion: 0.18
Nodes (20): hashTicket(), NewStore(), newTicket(), expireTicket(), issue(), liveRows(), newStore(), TestConcurrentConsumeHasOneWinner() (+12 more)

### Community 53 - "Handler"
Cohesion: 0.22
Nodes (7): NewHandler(), systemPolicyApplyEnvelope(), writeErr(), writeJSON(), AuditLogger, Handler, Hub

### Community 54 - "config.go"
Cohesion: 0.08
Nodes (30): buildNXDOMAIN(), buildSinkholeResponse(), extractHostFromURL(), forwardDNS(), normalizePatterns(), parseDNSQuestion(), StartDNSProxy(), buildQueryPacket() (+22 more)

### Community 55 - "Claims"
Cohesion: 0.15
Nodes (11): RoleReader, TokenKind, WebSocketTicketIssuer, Claims, JWTService, ClaimsForUser(), CurrentRoleReader(), currentRoleReader() (+3 more)

### Community 56 - "securityheaders_test.go"
Cohesion: 0.16
Nodes (13): cspResponseWriter, fakeHijackerRecorder, SecurityHeadersConfig, DefaultSecurityHeaders(), isTLS(), itoa(), SecurityHeaders(), TestDefaultSecurityHeadersConfig() (+5 more)

### Community 57 - "Repository"
Cohesion: 0.13
Nodes (6): AssetSummary, HardwareAsset, LicenseComplianceSummary, Repository, SoftwareLicense, newID()

### Community 58 - "github.com/gorilla/websocket.Conn"
Cohesion: 0.20
Nodes (3): activeRelay, RelayManager, startKeepalive()

### Community 59 - "Repository"
Cohesion: 0.13
Nodes (7): CompiledRules, DeviceFilterState, FilterPolicy, FilterRule, Repository, boolToInt(), NewID()

### Community 60 - "Handler"
Cohesion: 0.24
Nodes (7): AuditLogger, DeviceValidator, Handler, NewHandler(), writeErr(), writeJSON(), NewID()

### Community 61 - "Session"
Cohesion: 0.21
Nodes (4): NewPlatformCapturer(), NewPlatformCapturer(), ScreenCapturer, Session

### Community 62 - "Handler"
Cohesion: 0.23
Nodes (7): Auditor, Handler, boolStr(), itoa(), viewOf(), writeErr(), writeJSON()

### Community 63 - "compilerOptions"
Cohesion: 0.10
Nodes (19): compilerOptions, allowArbitraryExtensions, allowImportingTsExtensions, erasableSyntaxOnly, jsx, lib, module, moduleDetection (+11 more)

### Community 64 - "windows/inventory_windows.go"
Cohesion: 0.18
Nodes (12): newInventoryCollector(), archName(), isARPVisible(), isUpdateDisplayName(), isUpdateReleaseType(), NewCollector(), regGetString(), regIsOne() (+4 more)

### Community 65 - "linux/inventory_linux.go"
Cohesion: 0.20
Nodes (9): linuxUnescapeMount(), parseDpkgStatus(), queryRpm(), readCPUInfo(), utsString(), DedupeSoftware(), Software, linuxCollector (+1 more)

### Community 66 - "runProcess"
Cohesion: 0.18
Nodes (13): decodeOutput(), decodeUTF16(), looksUTF16LE(), runProcess(), runPlatformUninstall(), isMsiUninstall(), msiProductCode(), runMsiUninstall() (+5 more)

### Community 67 - "newQueueFixture"
Cohesion: 0.17
Nodes (13): recordingHub, queueFixture, TestAFailedTaskCanStillBeCorrectedToSuccess(), TestAFinishedTaskIsNotDrivenBackwards(), queueFixture, newQueueFixture(), TestAPendingTaskWhoseReleaseIsGoneFailsWithAReason(), TestDispatchToAnOfflineDeviceLeavesAFindablePendingTask() (+5 more)

### Community 68 - "net/http.HandlerFunc"
Cohesion: 0.15
Nodes (8): chiRouter, chiRouter, LimitJSONBody(), LimitJSONBodyFunc(), TestABodyOverTheCapIsRejectedAt413(), TestABodyUnderTheCapIsUnaffected(), TestLimitJSONBodyFuncMatchesLimitJSONBody(), TestTheCapIsNotOffByTheEnvelope()

### Community 69 - "newSoftwareEnv"
Cohesion: 0.22
Nodes (14): mockHub, uninstallEnv, NewRepository(), newMockHub(), newSoftwareEnv(), readBody(), setupUninstall(), TestE2EDeploymentActionDefaultsToInstallAndRejectsTypos() (+6 more)

### Community 70 - "DeviceDTO"
Cohesion: 0.14
Nodes (15): DeviceDetailModal(), DeviceDetailModalProps, formatAge(), InteractiveTerminalModalProps, FrameRect, PREVENT_DEFAULT_KEYS, RemoteControlModalProps, SessionStatus (+7 more)

### Community 71 - "inventory/scheduler.go"
Cohesion: 0.14
Nodes (13): newInventoryCollector(), newInventoryCollector(), NewCollector(), NewCollector(), Collector, CollectOnce(), initialOffset(), NewScheduler() (+5 more)

### Community 72 - "Handler"
Cohesion: 0.19
Nodes (13): ComparePassword(), HashPassword(), NewHandler(), writeErr(), writeJSON(), NewID(), TestE2EDashboardAPI(), TestE2ESoftwareDeployment() (+5 more)

### Community 73 - "networkfilter/engine_test.go"
Cohesion: 0.23
Nodes (12): setResolver(), setResolverFor(), TestApplyClearsPreviousFirewallRules(), TestApplyNarrowsRatherThanAccumulates(), TestApplySurvivesAnUnavailableFirewallButReportsIt(), TestCleanApplyIsNotMarkedDegraded(), testEngine(), TestHostsWriteFailureIsFatal() (+4 more)

### Community 74 - "capture_windows.go"
Cohesion: 0.20
Nodes (8): attachToInputDesktop(), isExtendedVK(), NewPlatformCapturer(), bitmapInfoHeader, keyboardInput, mouseInput, win32Input, WindowsCapturer

### Community 75 - "session0_windows.go"
Cohesion: 0.18
Nodes (12): goChildArgv(), TestWorkerCommandLineCarriesNoSecret(), TestWorkerCommandLineReachesAGoChildIntact(), TestWorkerFlagNamesMatchTheWorkerMain(), SpawnSessionWorker(), activeInteractiveSession(), primaryToken(), quoteArg() (+4 more)

### Community 76 - "Manager"
Cohesion: 0.12
Nodes (5): NewManager(), NewManager(), Manager, otherManager, windowsManager

### Community 77 - "Handler"
Cohesion: 0.31
Nodes (5): AuditLogger, Handler, NewHandler(), writeErr(), writeJSON()

### Community 78 - "TestE2EEnrollConnectCommand"
Cohesion: 0.23
Nodes (12): env, contains(), dialAgent(), enroll(), expectDialFail(), postJSON(), readEnv(), send() (+4 more)

### Community 79 - "patch-management/model.go"
Cohesion: 0.17
Nodes (8): DevicePatch, FleetPatchSummary, InstallRequest, PatchInstallJob, PatchInstallReport, PatchScanReport, Repository, reconcileAbsentPatches()

### Community 80 - "check-css.mjs"
Cohesion: 0.18
Nodes (10): collectClassNames(), findMissing(), loadTs(), main(), probeResolves(), require, root, RUNTIME_BUILT (+2 more)

### Community 81 - "Repository"
Cohesion: 0.21
Nodes (4): isUniqueViolation(), Repository, UpdateUserRequest, User

### Community 82 - "TestE2EGroups"
Cohesion: 0.21
Nodes (17): invDelete(), invDeleteCode(), invDo(), invGet(), invGetCode(), invPost(), invPostCode(), seedInvDevice() (+9 more)

### Community 83 - "compilerOptions"
Cohesion: 0.12
Nodes (16): compilerOptions, allowImportingTsExtensions, erasableSyntaxOnly, lib, module, moduleDetection, noEmit, noFallthroughCasesInSwitch (+8 more)

### Community 84 - "macos/inventory_darwin.go"
Cohesion: 0.29
Nodes (7): firstNonEmpty(), macParseMemory(), macProfileHardware(), macReadAppInfo(), macSysctl(), macCollector, macHardwareJSON

### Community 85 - "service.go"
Cohesion: 0.22
Nodes (12): triggerRestart(), DefaultConfig(), GetExecutablePath(), systemdUnit(), TestDefaultConfigNamesTheServiceThePackagingInstalls(), TestGetExecutablePathIsAbsolute(), TestSystemdUnitCarriesTheArgumentsAndTheRecoverySettings(), TestSystemdUnitWithoutArgumentsHasNoTrailingSpace() (+4 more)

### Community 86 - "inventory.go"
Cohesion: 0.26
Nodes (9): CollectNICs(), CPU, Disk, Hardware, Model, OSDetail, Report, winCollector (+1 more)

### Community 87 - "Engine"
Cohesion: 0.28
Nodes (3): removeManagedSection(), writeAtomic(), Engine

### Community 88 - "process_test.go"
Cohesion: 0.29
Nodes (11): helperArgs(), helperMode(), helperSelf(), processAlive(), TestCappedWriterKeepsHead(), TestCappedWriterUnderCap(), TestHelperNoop(), TestHelperOutput() (+3 more)

### Community 89 - "Repository"
Cohesion: 0.14
Nodes (6): ActivityItem, Alert, OSMetric, Repository, SiteMetric, Summary

### Community 90 - "enroll_test.go"
Cohesion: 0.22
Nodes (14): TestRunEnrollOnly_DeviceIDMismatch(), TestRunEnrollOnly_InvalidToken(), TestRunEnrollOnly_MissingServer(), TestRunEnrollOnly_MissingToken(), TestRunEnrollOnly_Success(), runEnrollOnly(), Exchange(), Load() (+6 more)

### Community 91 - "silent_uninstall_windows.go"
Cohesion: 0.18
Nodes (12): Serve(), extractUninstallerBase(), frameworkSilentSwitches(), isElevated(), isInnoExeName(), requireElevatedForUninstall(), resolveSilentArgs(), isRegularFile() (+4 more)

### Community 92 - "engine_windows.go"
Cohesion: 0.23
Nodes (8): applyInactivityLock(), applyRestrictions(), applyUSBStoragePolicy(), applyWallpaper(), applyWindowsUpdate(), revertWallpaper(), setOrDeleteDWord(), winBackend

### Community 93 - "Evaluator"
Cohesion: 0.17
Nodes (7): AlertIncident, AlertRule, EvalResult, Evaluator, Repository, boolToInt(), NewID()

### Community 94 - "DataTable"
Cohesion: 0.18
Nodes (14): DataTable(), DataTableProps, decorateBody(), findTable(), headerLabels(), Props, textOf(), AuditPage() (+6 more)

### Community 95 - "uninstall_test.go"
Cohesion: 0.21
Nodes (11): compact(), nameMatches(), normalizeProgramName(), exactMatches(), resolveUninstallerPath(), TestComponentNameLeavesBothCandidates(), TestExactMatchesBeatsLongerPrefix(), TestNameMatches() (+3 more)

### Community 96 - "NewEngine"
Cohesion: 0.23
Nodes (10): TestApplyUpdate_EmptyDownloadURLDoesNotPanic(), NewEngine(), runApply(), TestAChecksumMismatchStillRollsBack(), TestAnUnbootableReleaseIsNotReportedAsASuccessfulUpdate(), TestTheOriginalBinaryIsRestoredAfterAFailedHealthCheck(), TestAgentEngineManifestVerification(), fakeExecutable() (+2 more)

### Community 97 - "newCampaignFixture"
Cohesion: 0.54
Nodes (6): campaignFixture, newCampaignFixture(), TestACampaignIsNotRolledUpEarlyByAProgressReport(), TestACampaignReachesCompletedWhenEveryDeviceReports(), TestACampaignWhoseDevicesAllFailedIsFailed(), TestACampaignWithOneSuccessIsCompleted()

### Community 98 - "0001_init.sql"
Cohesion: 0.20
Nodes (12): agent_commands, audit_logs, idx_audit_created, idx_commands_created, idx_commands_device_status, idx_devices_last_seen, idx_devices_status, auth_sessions (+4 more)

### Community 99 - "software-deployment/sweep_test.go"
Cohesion: 0.27
Nodes (13): TestDevicesWithoutCapability(), TestDevicesWithoutCapabilityIgnoresUnknownIDs(), TestDevicesWithoutCapabilityReportsOnlyTheIncapableOnes(), idStr(), mustExec(), TestSyncDeploymentStatusRollsUpFailedWhenNothingSucceeded(), addTask(), sweepFixture() (+5 more)

### Community 100 - "uninstallFixture"
Cohesion: 0.29
Nodes (11): commandRows(), postUninstall(), TestUninstallByNameDispatchesToAnOnlineCapableAgent(), TestUninstallByNameRefusesADeviceWithNoStoredCapabilities(), TestUninstallByNameRefusesAnAgentThatCannotHandleIt(), TestUninstallByNameRefusesAnEmptyName(), TestUninstallByNameRefusesAnOfflineEndpoint(), TestUninstallByNameRefusesAnUnknownDevice() (+3 more)

### Community 101 - "scanner_linux.go"
Cohesion: 0.16
Nodes (15): installOS(), installOS(), installOS(), installOS(), scanOS(), aptCategory(), scanApt(), scanDnf() (+7 more)

### Community 102 - "installedProgram"
Cohesion: 0.24
Nodes (9): isRebootRequiredExit(), resolveSilentArgs(), findDpkgInstalled(), findInstalled(), findRpmInstalled(), firstLine(), runPlatformUninstall(), resolveTarget() (+1 more)

### Community 103 - "net/http.Client"
Cohesion: 0.14
Nodes (11): PolicySettings, USBStorageMode, WallpaperStyle, WindowsUpdateMode, NewEngine(), newPlatformBackend(), TestEngine_Report(), newPlatformBackend() (+3 more)

### Community 104 - "devicesQuery.ts"
Cohesion: 0.21
Nodes (11): P(), DEVICE_STATUSES, DEVICE_TABS, DevicesQuery, DeviceStatus, DeviceTab, MAX_PAGE, PAGE_SIZES (+3 more)

### Community 105 - "runAgent"
Cohesion: 0.25
Nodes (8): guardAgentGoroutine(), inventoryPeriod(), inventoryStaggerWindow(), runAgent(), Capabilities(), HandleCancel(), NewClient(), wsURL()

### Community 106 - "remotecontrol/session_test.go"
Cohesion: 0.21
Nodes (12): NewSession(), TestABlankModeBecomesFullControl(), TestACaptureIsRawPixelsSizedToTheDesktop(), TestANoticeUsesTheSameShapeAsTheCaptureRefusal(), TestAnUnknownModeIsNotStored(), TestASessionKeepsTheRelayURLItWasGiven(), TestSendNoticeOnADeadSessionIsSafe(), TestStopIsIdempotent() (+4 more)

### Community 107 - "Engine"
Cohesion: 0.24
Nodes (7): Engine, releaseArch(), releaseOSName(), belowMinimum(), parseVersion(), SignedManifest, UpdateParams

### Community 108 - "AuthContext.tsx"
Cohesion: 0.27
Nodes (11): AuthContext, AuthContextType, AuthProvider(), clearStoredTokens(), fetchRaw(), getStoredRefreshToken(), getStoredToken(), parseJwtClaims() (+3 more)

### Community 109 - "Repository"
Cohesion: 0.32
Nodes (3): RemoteControlSession, Repository, NewID()

### Community 110 - "Modal"
Cohesion: 0.12
Nodes (23): TerminalMessage, ConfirmDialog(), ConfirmDialogProps, Modal(), ModalProps, ModalSize, ASSET_STATUSES, AssetLicensePage() (+15 more)

### Community 111 - "MaintenancePage.tsx"
Cohesion: 0.26
Nodes (11): formatBytes(), formatTime(), isJobStatus(), isRunningStatus(), jobPill(), MaintenancePage(), taskPill(), MaintenanceJobDTO (+3 more)

### Community 112 - "resolve.go"
Cohesion: 0.24
Nodes (9): defaultHostsPath(), NewEngine(), TestNormalizeDomainStripsWhatOperatorsPaste(), newResolver(), normalizeDomain(), resolveEntry, resolver, TestNetworkFilter_AgentHostsEngine() (+1 more)

### Community 114 - "inventory_cim_test.go"
Cohesion: 0.25
Nodes (8): TestAsInt64(), TestCimAcceptsBareObjectEvenThoughCompressDoesNotEmitOne(), TestCimRejectsBrokenOutput(), TestCimShapeIsExactlyWhatPowerShellEmits(), asInt64(), cimQuery(), winCollector, parseCIMRows()

### Community 115 - "deployment_tasks"
Cohesion: 0.31
Nodes (9): deployment_tasks, idx_deploy_tasks_deploy, idx_deploy_tasks_device, idx_deploy_tasks_status, idx_packages_target, software_deployments, software_packages, idx_software_deployments_action (+1 more)

### Community 116 - "dnsadapter.go"
Cohesion: 0.38
Nodes (9): configureLinuxDNS(), ConfigureLoopbackDNS(), configureWindowsDNS(), getActiveLinuxInterface(), getConnectedWindowsAdapters(), GetPrimaryUpstreamDNS(), getWindowsDNSBackup(), RestoreOriginalDNS() (+1 more)

### Community 117 - "crypto/ed25519.PublicKey"
Cohesion: 0.27
Nodes (4): Engine, parsePublicKey(), loadPublicKey(), agentConfig

### Community 118 - "NewWebSocketDialer"
Cohesion: 0.29
Nodes (5): GetTLSConfig(), NewWebSocketDialer(), TestGetTLSConfig_Defaults(), TestGetTLSConfig_InsecureSkipVerify(), TestNewWebSocketDialer_Configuration()

### Community 119 - "0006_patch_management.sql"
Cohesion: 0.36
Nodes (9): device_patches, idx_patch_jobs_device, idx_patch_jobs_operator, idx_patch_jobs_status, idx_patches_device, idx_patches_patchid, idx_patches_severity, idx_patches_state (+1 more)

### Community 121 - "cleanup_locked_windows_test.go"
Cohesion: 0.33
Nodes (5): lockFile(), TestCleanupTempStaysGreenWithALockedFileInTheRoot(), TestLockedFileIsNotAPrivilegeRefusal(), TestPermissionDeniedIsStillARefusal(), isPermissionDenied()

### Community 124 - "TaskQueue"
Cohesion: 0.19
Nodes (7): TestQueueRejectsPastDepth(), TestQueueSerializes(), TestQueueSurvivesPanic(), NewTaskQueue(), shortID(), task, TaskQueue

### Community 125 - "sync.RWMutex"
Cohesion: 0.16
Nodes (8): NewTerminalManager(), NewDispatcher(), sessionCache, sessionCacheEntry, TerminalManager, getSessionCache(), CommandHandler, Dispatcher

### Community 126 - "main"
Cohesion: 0.40
Nodes (5): defaultCredsPath(), envOr(), handleServiceAction(), main(), runRCWorker()

### Community 128 - "devices"
Cohesion: 0.31
Nodes (7): devices, idx_devices_os, idx_devices_site, idx_inventory_disk_free, idx_devices_enrollment_token, idx_devices_secret_hash, idx_devices_status_last_seen

### Community 129 - "0005_remote_execution.sql"
Cohesion: 0.39
Nodes (8): users, idx_remote_exec_device, idx_remote_exec_started, idx_remote_exec_status, idx_term_sessions_device, idx_term_sessions_status, remote_executions, terminal_sessions

### Community 130 - "0009_task_scheduler.sql"
Cohesion: 0.44
Nodes (8): idx_sched_device_runs_dev, idx_sched_device_runs_run, idx_scheduled_runs_sched, idx_task_schedules_enabled, scheduled_task_device_runs, scheduled_task_runs, script_templates, task_schedules

### Community 131 - "newProgressFixture"
Cohesion: 0.39
Nodes (7): newProgressFixture(), reportAs(), taskRow(), TestAnAgentCannotReportProgressForAnotherDevicesTask(), TestAnAgentCanStillReportItsOwnTask(), TestAnUnauthenticatedAgentCannotReportProgress(), TestProgressOnAnUnknownTaskIsIndistinguishableFromAnotherDevicesTask()

### Community 132 - "NewRepository"
Cohesion: 0.15
Nodes (15): intervalSchedule(), openScheduleDB(), TestChangingTheIntervalMovesTheNextRun(), TestDisablingAScheduleClearsItsNextRun(), TestFiringAScheduleAdvancesItsNextRun(), TestTheNextRunColumnIsFilled(), pollFixture(), TestTheBackgroundSchedulerActuallyPolls() (+7 more)

### Community 136 - "diffSoftware"
Cohesion: 0.25
Nodes (6): inventoryDiff, softwareDiff, decodeSoftwareNames(), diffSoftware(), TestDiffSoftwareAddRemove(), TestDiffSoftwareMalformed()

### Community 137 - "newUploadRouter"
Cohesion: 0.39
Nodes (6): multipartBody(), newUploadRouter(), setMaxPackageBytes(), TestAnOversizedPackageUploadIsRefused(), TestAPackageUnderTheLimitStillUploads(), TestTheFormFieldsStillFitUnderTheCap()

### Community 138 - "0002_device_mgmt.sql"
Cohesion: 0.43
Nodes (7): device_group_members, device_groups, device_inventory, idx_group_members_device, idx_groups_name, idx_inventory_collected, idx_inventory_device

### Community 139 - "0008_alerting.sql"
Cohesion: 0.46
Nodes (7): alert_incidents, alert_rules, idx_alert_dedup, idx_alert_incidents_device, idx_alert_incidents_rule, idx_alert_incidents_status, idx_alert_rules_enabled

### Community 140 - "Conn"
Cohesion: 0.21
Nodes (3): Hub, newConn(), Conn

### Community 141 - "NewHub"
Cohesion: 0.43
Nodes (8): NewHub(), dialAgent(), TestDisconnectWithdrawsAndClosesAgentSocket(), TestSendIsNotClosed(), TestSendOnAConnBeingTornDownNeverPanics(), TestSupersededConnectionStopsWithoutRacingItsPump(), TestWritePumpStillDeliversWhatWasQueuedFirst(), TestWritePumpStopsWhenTheConnectionIsTornDown()

### Community 142 - "setupTestServer"
Cohesion: 0.25
Nodes (5): releaseDownloadURL(), setupTestServer(), TestAgentConfigEndpoint(), TestSignReleaseEndpoint(), uploadReleaseFixture()

### Community 143 - "0013_asset_license_management.sql"
Cohesion: 0.48
Nodes (6): hardware_assets, idx_hardware_assets_dev, idx_hardware_assets_status, idx_software_licenses_name, license_allocations, software_licenses

### Community 144 - "0014_maintenance.sql"
Cohesion: 0.52
Nodes (6): idx_maint_jobs_started, idx_maint_tasks_device, idx_maint_tasks_job, idx_maint_tasks_status, maintenance_jobs, maintenance_tasks

### Community 145 - "stubHub"
Cohesion: 0.29
Nodes (3): offlineHub, oneOnlineHub, stubHub

### Community 146 - "bytesReader"
Cohesion: 0.33
Nodes (3): bytesReader(), countingReader, countingReader

### Community 148 - "output_test.go"
Cohesion: 0.60
Nodes (4): encodeUTF16(), TestDecodeOutput(), utf16be(), utf16le()

### Community 150 - "0011_network_filter.sql"
Cohesion: 0.53
Nodes (5): device_filter_states, filter_policies, filter_rules, idx_filter_policies_target, idx_filter_rules_policy

### Community 151 - "0012_agent_self_update.sql"
Cohesion: 0.53
Nodes (5): agent_releases, device_update_tasks, idx_agent_releases_lookup, idx_device_update_tasks_dev, update_campaigns

### Community 152 - ".oxlintrc.json"
Cohesion: 0.33
Nodes (5): plugins, rules, react/only-export-components, react/rules-of-hooks, $schema

### Community 153 - "uninstall_darwin.go"
Cohesion: 0.53
Nodes (5): findInstalled(), lastComponent(), listReceipts(), receiptVersion(), runPlatformUninstall()

### Community 155 - "hwModel"
Cohesion: 0.50
Nodes (5): hwJSON, hwModel, decodeHW(), hwModelValue(), modelsEqual()

### Community 157 - "remote_control_sessions"
Cohesion: 0.70
Nodes (4): idx_rc_sessions_device, idx_rc_sessions_operator, idx_rc_sessions_status, remote_control_sessions

### Community 158 - "0019_directory_sync.sql"
Cohesion: 0.60
Nodes (4): directory_contacts, directory_sync_config, idx_directory_contacts_dept, idx_directory_contacts_name

### Community 161 - "sync.Mutex"
Cohesion: 0.15
Nodes (3): ProcessSession, cappedWriter, HeartbeatFlusher

### Community 164 - "0024_system_policies.sql"
Cohesion: 0.67
Nodes (3): device_system_policy_states, idx_system_policies_target, system_policies

### Community 165 - "newSafeWebhookClient"
Cohesion: 0.50
Nodes (4): isPrivateOrBlockedIP(), newSafeWebhookClient(), TestIsPrivateOrBlockedIP(), TestSafeWebhookClientRejectsPrivateIP()

## Knowledge Gaps
- **10 isolated node(s):** `github.com/henokakunemail-stack/Endpoint-Manager`, `oxlint`, `tailwindcss`, `@types/node`, `@types/react` (+5 more)
  These have ≤1 connection - possible missing edges or undocumented components. (Counts symbols only; 484 node(s) total have ≤1 connection when file, concept and rationale nodes are included.)
- **72 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `TerminalManager` connect `sync.RWMutex` to `server/main.go`, `sync.Mutex`?**
  _High betweenness centrality (0.018) - this node is a cross-community bridge._
- **What connects `github.com/henokakunemail-stack/Endpoint-Manager`, `oxlint`, `tailwindcss` to the rest of the system?**
  _10 weakly-connected nodes found - possible documentation gaps or missing edges._
- **Should `server/main.go` be split into smaller, more focused modules?**
  _Cohesion score 0.06688489932313778 - nodes in this community are weakly interconnected._
- **Why does `UserIDFromContext()` connect `UserIDFromContext` to `server/main.go`, `context.Context`, `auth/session_test.go`, `net/http.ResponseWriter`, `Handler`, `Handler`, `Handler`, `buildServer`, `Handler`, `net/http.Request`, `AuthenticateAgent`, `user-management/handler_test.go`, `Handler`, `Handler`, `LoginHandler`, `Handler`, `Handler`, `Handler`?**
  _High betweenness centrality (0.016) - this node is a cross-community bridge._
- **Should `context.Context` be split into smaller, more focused modules?**
  _Cohesion score 0.026246719160104987 - nodes in this community are weakly interconnected._
- **Why does `windowsManager` connect `Manager` to `server/main.go`, `service.go`?**
  _High betweenness centrality (0.014) - this node is a cross-community bridge._
- **Should `testing.T` be split into smaller, more focused modules?**
  _Cohesion score 0.026659312373598087 - nodes in this community are weakly interconnected._