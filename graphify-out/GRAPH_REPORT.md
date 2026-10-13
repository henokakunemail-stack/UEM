# Graph Report - Desktop Manage  (2026-10-13)

## Corpus Check
- cluster-only mode — file stats not available

## Summary
- 4039 nodes · 15418 edges · 226 communities (151 shown, 75 thin omitted)
- Extraction: 95% EXTRACTED · 5% INFERRED · 0% AMBIGUOUS · INFERRED: 846 edges (avg confidence: 0.85)
- Token cost: 0 input · 0 output

## Graph Freshness
- Built from commit: `ad9c3038`
- Run `git rev-parse HEAD` and compare to check if the graph is stale.
- Run `graphify update .` after code changes (no API cost).

## Community Hubs (Navigation)
- Community 0
- Community 1
- Community 2
- Community 3
- Community 4
- Community 5
- Community 6
- Community 7
- Community 8
- Community 9
- Community 10
- Community 11
- Community 12
- Community 13
- Community 14
- Community 15
- Community 16
- Community 17
- Community 18
- Community 19
- Community 20
- Community 21
- Community 22
- Community 23
- Community 24
- Community 25
- Community 26
- Community 27
- Community 28
- Community 29
- Community 30
- Community 31
- Community 32
- Community 33
- Community 34
- Community 35
- Community 36
- Community 37
- Community 38
- Community 39
- Community 40
- Community 41
- Community 42
- Community 43
- Community 44
- Community 45
- Community 46
- Community 47
- Community 48
- Community 49
- Community 50
- Community 51
- Community 52
- Community 53
- Community 54
- Community 55
- Community 56
- Community 57
- Community 58
- Community 59
- Community 60
- Community 61
- Community 62
- Community 63
- Community 64
- Community 65
- Community 66
- Community 67
- Community 68
- Community 69
- Community 70
- Community 71
- Community 72
- Community 73
- Community 74
- Community 75
- Community 76
- Community 77
- Community 78
- Community 79
- Community 80
- Community 81
- Community 82
- Community 83
- Community 84
- Community 85
- Community 86
- Community 87
- Community 88
- Community 89
- Community 90
- Community 91
- Community 92
- Community 93
- Community 94
- Community 95
- Community 96
- Community 97
- Community 98
- Community 99
- Community 100
- Community 101
- Community 102
- Community 103
- Community 104
- Community 105
- Community 106
- Community 107
- Community 108
- Community 109
- Community 110
- Community 111
- Community 112
- Community 113
- Community 114
- Community 115
- Community 116
- Community 117
- Community 118
- Community 119
- Community 120
- Community 121
- Community 122
- Community 123
- Community 124
- Community 125
- Community 126
- Community 127
- Community 128
- Community 129
- Community 130
- Community 131
- Community 132
- Community 133
- Community 134
- Community 135
- Community 136
- Community 137
- Community 138
- Community 139
- Community 140
- Community 141
- Community 142
- Community 143
- Community 144
- Community 145
- Community 146
- Community 147
- Community 148
- Community 149
- Community 150
- Community 151
- Community 152
- Community 153
- Community 154
- Community 155
- Community 156
- Community 157
- Community 158
- Community 159
- Community 160
- Community 161
- Community 162
- Community 163
- Community 164
- Community 165
- Community 166
- Community 167
- Community 168
- Community 169
- Community 170
- Community 171
- Community 172
- Community 174
- Community 175
- Community 176
- Community 177
- Community 178
- Community 179
- Community 180
- Community 181
- Community 182
- Community 183
- Community 186
- Community 190
- Community 191
- Community 192
- Community 193
- Community 194
- Community 209

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

## Communities (226 total, 75 thin omitted)

### Community 0 - "Community 0"
Cohesion: 0.07
Nodes (50): TestMain(), newPlatformRunner(), newPlatformRunner(), newPlatformRunner(), newPlatformRunner(), createRuleReq, diskTarget, patchTarget (+42 more)

### Community 1 - "Community 1"
Cohesion: 0.03
Nodes (41): AgentRelease, DeviceUpdateTask, discardAuditor, noopAuditor, Repository, UpdateCampaign, stubAudit, nopAuditor (+33 more)

### Community 2 - "Community 2"
Cohesion: 0.03
Nodes (102): TestACapturedFrameIsRawPixelsSizedToTheDesktop(), TestACapturerWorksFromMoreThanOneThread(), TestInjectKeyboardEventSucceeds(), TestInjectMouseEventSucceeds(), TestKeyboardTrackingIsSafeUnderConcurrency(), TestTheProbeAndTheCaptureAgreeOnThisHost(), TestWin32InputStructContract(), parseProbeOutput() (+94 more)

### Community 3 - "Community 3"
Cohesion: 0.10
Nodes (59): env, httpResult, mockHub, uninstallEnv, NewLoginHandler(), NewSessionStore(), tokenPair, newEnv() (+51 more)

### Community 4 - "Community 4"
Cohesion: 0.05
Nodes (62): BackupConfig, overlayFS, TestSessionStore_CacheHitAndInvalidation(), Backup(), copyFile(), DefaultBackupConfig(), quickCheck(), Restore() (+54 more)

### Community 5 - "Community 5"
Cohesion: 0.05
Nodes (60): dialect, auditBridge, fakeRoles, auditAdapter, dsn(), DSNForPath(), Open(), OpenWithDriver() (+52 more)

### Community 6 - "Community 6"
Cohesion: 0.06
Nodes (66): bootstrapAdmin(), jsonKind(), sortedKeys(), testConfig(), TestHealthzAndReadyzEndpoints(), TestListEndpointResponseShape(), TestListEndpointsNeverReturnNull(), TestObjectEndpointsExposeTheirFields() (+58 more)

### Community 7 - "Community 7"
Cohesion: 0.07
Nodes (53): BINARY, EnrollTokenModalProps, AuthContext, AuthContextType, AuthProvider(), STATUS_FILTERS, AuditPage(), emptyConfig() (+45 more)

### Community 8 - "Community 8"
Cohesion: 0.11
Nodes (46): DeviceDetailModal(), EnrollTokenModal(), ConfirmDialog(), ConfirmDialogProps, DataTable(), DataTableProps, decorateBody(), findTable() (+38 more)

### Community 9 - "Community 9"
Cohesion: 0.08
Nodes (36): Contact, directoryClient, fakeClient, ldapClient, nopAuditor, SyncPlan, contactFieldsDiffer(), diffContacts() (+28 more)

### Community 10 - "Community 10"
Cohesion: 0.07
Nodes (42): lucide-react, react-router-dom, AgentUpdatesPage, AlertsPage, App(), AssetLicensePage, AuditPage, ConsoleRoot() (+34 more)

### Community 11 - "Community 11"
Cohesion: 0.14
Nodes (49): countOrphanedLaunchAgents(), darwinCleanupTemp(), darwinDiskCheck(), darwinFlushDNS(), darwinLogMaintenance(), darwinMemoryHygiene(), darwinPerUserTempDirs(), darwinSecurityAudit() (+41 more)

### Community 12 - "Community 12"
Cohesion: 0.07
Nodes (46): Allowed(), assertDistinctMirrors(), NewEngine(), stepTimeout(), engineFor(), jsonTags(), newCaptureServer(), setModTime() (+38 more)

### Community 13 - "Community 13"
Cohesion: 0.05
Nodes (32): NewEngine(), ExecuteAndReport(), ReportResult(), ToHTTPURL(), ExecuteInstall(), ReportProgress(), ToHTTPURL(), taskContext() (+24 more)

### Community 14 - "Community 14"
Cohesion: 0.08
Nodes (42): addRuleReq, WithRole(), assertRefused(), assetFixture(), createAsset(), seedDevice(), TestADuplicateAssetTagIsRefusedWithAMessageNotAConstraintError(), TestAnAssetKeepingItsOwnTagIsNotAConflict() (+34 more)

### Community 15 - "Community 15"
Cohesion: 0.11
Nodes (17): auditWriter, Handler, hubSender, inventoryHandler, Log(), UserIDFromContext(), toDTO(), writeErr() (+9 more)

### Community 16 - "Community 16"
Cohesion: 0.10
Nodes (43): ChainReport, Entry, verifyRow, List(), newID(), chainLockFor(), entryHashOf(), nullDetails() (+35 more)

### Community 17 - "Community 17"
Cohesion: 0.06
Nodes (22): AgentReleaseDTO, offlineTarget, LicenseAllocation, UpdateAssetRequest, createEnrollTokenResp, deviceDTO, Config, ConfigView (+14 more)

### Community 18 - "Community 18"
Cohesion: 0.15
Nodes (32): ComparePassword(), newEnv(), strictEnv(), TestADeactivatedUserKeepsTheirAuditTrail(), TestADeactivatedUserLosesApiAccess(), TestADemotedUsersSessionsAreRevoked(), TestADuplicateUsernameIsRefused(), TestAMissingRoleDefaultsToViewer() (+24 more)

### Community 19 - "Community 19"
Cohesion: 0.12
Nodes (37): stubHub, call(), newHandlerTest(), newStubHub(), roleContext(), seedOnlineDevice(), seedTaskForReport(), TestAgentResultRejectsBodyTaskIDMismatch() (+29 more)

### Community 20 - "Community 20"
Cohesion: 0.12
Nodes (28): DecodeFrame(), DecodeFrameHeader(), EncodeFrame(), encodeRect(), NewDamage(), absInt(), solidFrame(), TestAdaptiveQualityReducesLargeDamagePayload() (+20 more)

### Community 21 - "Community 21"
Cohesion: 0.10
Nodes (19): RoleReader, WebSocketTicketIssuer, AuditLogger, DeviceValidator, Handler, HubDispatcher, ClaimsForUser(), CurrentRoleReader() (+11 more)

### Community 22 - "Community 22"
Cohesion: 0.10
Nodes (9): Envelope, NewWSHandler(), Conn, FilterSyncer, InventoryReceiver, SystemPolicySyncer, TerminalReceiver, UpdateQueue (+1 more)

### Community 23 - "Community 23"
Cohesion: 0.08
Nodes (31): busyHub, dev1Headers(), issueTicket(), mustExecDevices(), rcRouteFixture(), startSessionRequest(), TestAMalformedFrameFromTheOperatorIsIgnoredNotFatal(), TestAnOperatorSocketWithNoCredentialIsRefused() (+23 more)

### Community 24 - "Community 24"
Cohesion: 0.06
Nodes (35): oxlint, react-dom, tailwindcss, @tailwindcss/vite, @types/node, @types/react, @types/react-dom, typescript (+27 more)

### Community 25 - "Community 25"
Cohesion: 0.11
Nodes (30): searchOf(), TestDriverUpdateIsClassifiedAsDriver(), TestHiddenUpdateIsSkipped(), TestInstallScriptSearchesEveryUpdateType(), TestSecurityCategoryIsFoundBeyondTheFirst(), update(), installOS(), installOS() (+22 more)

### Community 26 - "Community 26"
Cohesion: 0.10
Nodes (13): liveUser, TokenKind, Claims, JWTService, TokenPair, LoginHandler, writeErr(), writeJSON() (+5 more)

### Community 27 - "Community 27"
Cohesion: 0.09
Nodes (22): AuditLogger, Handler, Hub, buildServer(), handleServiceAction(), main(), markOfflineBatch(), mustJSON() (+14 more)

### Community 28 - "Community 28"
Cohesion: 0.09
Nodes (19): DeviceTarget, Job, JobFilter, JobProgress, RunRequest, TargetType, Task, TaskFilter (+11 more)

### Community 29 - "Community 29"
Cohesion: 0.17
Nodes (9): Auditor, Handler, Handler, NewHandler(), redactLicenseKey(), writeJSON(), NewHandler(), writeErr() (+1 more)

### Community 30 - "Community 30"
Cohesion: 0.14
Nodes (26): msiFields(), newPackageFixture(), seedPackage(), TestADeploymentDispatchesToOnlineAgentsAndMarksThoseTasksDispatched(), TestADeploymentToAnAgentThatCannotRunItIsRefused(), TestADeploymentToNothingIsRefused(), TestAGroupTargetOnlyReachesItsOwnMembers(), TestAMissingPackageIs404OnBothReadAndDelete() (+18 more)

### Community 31 - "Community 31"
Cohesion: 0.10
Nodes (28): TestDBPathForBackupDefault(), TestDBPathForBackupNeedsNoJWTSecret(), TestDBPathForBackupReadsEnvFile(), unsetForEnvFileTest(), TestConfig_ReadsPostgresSettings(), TestOpenDatabase_DefaultsToSQLite(), TestOpenDatabase_IgnoresBareSQLiteDriver(), TestOpenDatabase_SQLiteDriverIsExplicit() (+20 more)

### Community 32 - "Community 32"
Cohesion: 0.09
Nodes (7): rebindConn, rebindDriver, mockBindConn, mockBindDriver, rebind(), setupMockDB(), TestPolicyRepository_PostgresIntegerBinding()

### Community 33 - "Community 33"
Cohesion: 0.08
Nodes (10): TestCapabilitiesAdvertisesTheCommandName(), NewPlatformCapturer(), TestACaptureIsRawPixelsSizedToTheDesktop(), TestAFailedProbeIsReportedVerbatim(), Capabilities, DarwinCapturer, fakeCapturer, InputEvent (+2 more)

### Community 34 - "Community 34"
Cohesion: 0.09
Nodes (16): ipRecord, EnrollmentHandler, ClientIPResolver, IPRateLimiter, NewClientIPResolver(), NewIPRateLimiter(), parseTrustedProxy(), remoteHost() (+8 more)

### Community 35 - "Community 35"
Cohesion: 0.18
Nodes (12): validateOrigin(), ValidateWebSocketOrigin(), TestValidateWebSocketOrigin(), isRunnableScheduleType(), NewHandler(), normalizeTaskStatus(), writeErr(), writeJSON() (+4 more)

### Community 36 - "Community 36"
Cohesion: 0.10
Nodes (16): setupTestDB(), TestCompileEffectivePolicy_Precedence(), TestDevicePolicyState_CRUD(), PolicySettings, USBStorageMode, WallpaperStyle, WindowsUpdateMode, boolToInt() (+8 more)

### Community 37 - "Community 37"
Cohesion: 0.11
Nodes (10): TestANonIntervalScheduleHasNoNextRun(), boolToInt(), CalculateSHA256(), Repository, NewID(), nextRunAt(), ScheduledTaskDeviceRun, ScheduledTaskRun (+2 more)

### Community 39 - "Community 39"
Cohesion: 0.17
Nodes (9): AuditLogger, Handler, Hub, passthroughAuth(), filterApplyEnvelope(), NewHandler(), writeErr(), writeJSON() (+1 more)

### Community 40 - "Community 40"
Cohesion: 0.10
Nodes (24): mockExecHub, recordingAudit, TestAnEmptyShellDefaultsToPowerShellAndTheTimeoutIsClamped(), withRouteParam(), NewID(), TestAcceptTerminalRejectsMismatchedDevice(), TestConcurrentUnregisterAndWrite(), TestUnregisterStopsLaterWritesFromReachingTheSocket() (+16 more)

### Community 41 - "Community 41"
Cohesion: 0.15
Nodes (19): runFixture, newRunFixture(), newRunFixtureWithHub(), TestADeviceCanStillReportItsOwnResult(), TestAFullyFailedRunIsFailedNotCompleted(), TestAFullySuccessfulRunReachesATerminalStatus(), TestAMixedRunIsNotClosedEarly(), TestAnAgentCannotReportAnotherDevicesResult() (+11 more)

### Community 42 - "Community 42"
Cohesion: 0.11
Nodes (14): TestChunkIPsCoversEverything(), applyFirewall(), chunkIPs(), isNotFound(), newFirewallBackend(), runNFT(), newFirewallBackend(), joinIPs() (+6 more)

### Community 43 - "Community 43"
Cohesion: 0.18
Nodes (10): checkExtensionMatches(), DeviceLookup, NewHandler(), writeErr(), writeJSON(), NewID(), TestCheckExtensionMatches(), AuditLogger (+2 more)

### Community 44 - "Community 44"
Cohesion: 0.10
Nodes (10): createShellCmd(), createShellCmd(), createShellCmd(), createShellCmd(), terminate(), processTree, newProcessTree(), processTree (+2 more)

### Community 45 - "Community 45"
Cohesion: 0.10
Nodes (20): connectFixture, fakeHub, newDispatchFixture(), TestAutoDispatchOnPolicyDeleteRestoresAccess(), TestAutoDispatchOnRuleAddAndRuleDelete(), withUserCtx(), assertFilterMessage(), newConnectFixture() (+12 more)

### Community 46 - "Community 46"
Cohesion: 0.17
Nodes (6): Auditor, Handler, HubDispatcher, NewHandler(), writeJSON(), manifestForRelease()

### Community 47 - "Community 47"
Cohesion: 0.15
Nodes (21): react, AlertsList(), AlertsListProps, formatRelativeTime(), getIcon(), KPICard(), KPICardProps, FALLBACK (+13 more)

### Community 48 - "Community 48"
Cohesion: 0.14
Nodes (14): newOSInfoProvider(), newOSInfoProvider(), newOSInfoProvider(), New(), parseOSRelease(), readKey(), New(), swVers() (+6 more)

### Community 49 - "Community 49"
Cohesion: 0.13
Nodes (11): StepReport, reportBackoff(), stepsFor(), TestTruncateBoundsAgentStrings(), truncate(), truncateLog(), Engine, newTicket() (+3 more)

### Community 50 - "Community 50"
Cohesion: 0.15
Nodes (13): SecretLookup, AuditLogger, Handler, Hub, AuthenticateAgent(), TestAuthenticateAgent_AcceptsMatchingPair(), TestAuthenticateAgent_RejectsMissingCredentials(), TestAuthenticateAgent_RejectsSecretBoundToAnotherDevice() (+5 more)

### Community 51 - "Community 51"
Cohesion: 0.11
Nodes (8): runDeploymentSweep(), Repository, AgentCommand, DeploymentTask, DeviceCapability, SoftwareDeployment, SoftwarePackage, TaskProgressReport

### Community 52 - "Community 52"
Cohesion: 0.20
Nodes (19): hashTicket(), NewStore(), expireTicket(), issue(), liveRows(), newStore(), TestConcurrentConsumeHasOneWinner(), TestConsumeForRequiresPurpose() (+11 more)

### Community 53 - "Community 53"
Cohesion: 0.22
Nodes (7): NewHandler(), systemPolicyApplyEnvelope(), writeErr(), writeJSON(), AuditLogger, Handler, Hub

### Community 54 - "Community 54"
Cohesion: 0.14
Nodes (15): buildNXDOMAIN(), buildSinkholeResponse(), extractHostFromURL(), forwardDNS(), normalizePatterns(), parseDNSQuestion(), StartDNSProxy(), buildQueryPacket() (+7 more)

### Community 55 - "Community 55"
Cohesion: 0.11
Nodes (22): DescribeUninstall(), TestDescribeUninstallKeepsTheOutputOfARealFailure(), TestDescribeUninstallStillCallsANothingRanRefusalARefusal(), isMSIShape(), TestDescribeUninstallSaysWhatHappened(), TestNotInstalledIsReportedAsSuccess(), TestRebootRequiredExitIsReadOffTheExitCodeNotTheError(), TestResolveSilentArgsInnoSetupFallback() (+14 more)

### Community 56 - "Community 56"
Cohesion: 0.16
Nodes (13): cspResponseWriter, fakeHijackerRecorder, SecurityHeadersConfig, DefaultSecurityHeaders(), isTLS(), itoa(), SecurityHeaders(), TestDefaultSecurityHeadersConfig() (+5 more)

### Community 57 - "Community 57"
Cohesion: 0.13
Nodes (6): AssetSummary, HardwareAsset, LicenseComplianceSummary, Repository, SoftwareLicense, newID()

### Community 58 - "Community 58"
Cohesion: 0.14
Nodes (6): activeRelay, nullAuditor, RelayManager, newConn(), startKeepalive(), TestTheRelayPingsSoItsOwnDeadlineCanBeRefreshed()

### Community 59 - "Community 59"
Cohesion: 0.13
Nodes (7): CompiledRules, DeviceFilterState, FilterPolicy, FilterRule, Repository, boolToInt(), NewID()

### Community 60 - "Community 60"
Cohesion: 0.24
Nodes (7): AuditLogger, DeviceValidator, Handler, NewHandler(), writeErr(), writeJSON(), NewID()

### Community 61 - "Community 61"
Cohesion: 0.21
Nodes (4): NewPlatformCapturer(), NewPlatformCapturer(), ScreenCapturer, Session

### Community 62 - "Community 62"
Cohesion: 0.22
Nodes (8): Auditor, Handler, boolStr(), itoa(), NewHandler(), viewOf(), writeErr(), writeJSON()

### Community 63 - "Community 63"
Cohesion: 0.10
Nodes (19): compilerOptions, allowArbitraryExtensions, allowImportingTsExtensions, erasableSyntaxOnly, jsx, lib, module, moduleDetection (+11 more)

### Community 64 - "Community 64"
Cohesion: 0.18
Nodes (12): newInventoryCollector(), archName(), isARPVisible(), isUpdateDisplayName(), isUpdateReleaseType(), NewCollector(), regGetString(), regIsOne() (+4 more)

### Community 65 - "Community 65"
Cohesion: 0.20
Nodes (9): linuxUnescapeMount(), parseDpkgStatus(), queryRpm(), readCPUInfo(), utsString(), macReadAppInfo(), Software, linuxCollector (+1 more)

### Community 66 - "Community 66"
Cohesion: 0.18
Nodes (14): decodeOutput(), decodeUTF16(), looksUTF16LE(), runProcess(), runPlatformUninstall(), runPlatformUninstall(), isMsiUninstall(), msiProductCode() (+6 more)

### Community 67 - "Community 67"
Cohesion: 0.22
Nodes (10): recordingHub, queueFixture, newQueueFixture(), TestAPendingTaskWhoseReleaseIsGoneFailsWithAReason(), TestDispatchToAnOfflineDeviceLeavesAFindablePendingTask(), TestDispatchToAnOnlineDeviceStillSendsImmediately(), TestFlushingADeviceWithNothingQueuedIsANoOp(), TestTheQueuedUpdateIsDeliveredWhenTheDeviceReconnects() (+2 more)

### Community 68 - "Community 68"
Cohesion: 0.15
Nodes (8): chiRouter, chiRouter, LimitJSONBody(), LimitJSONBodyFunc(), TestABodyOverTheCapIsRejectedAt413(), TestABodyUnderTheCapIsUnaffected(), TestLimitJSONBodyFuncMatchesLimitJSONBody(), TestTheCapIsNotOffByTheEnvelope()

### Community 69 - "Community 69"
Cohesion: 0.15
Nodes (3): Session, purgeSessions(), SessionStore

### Community 70 - "Community 70"
Cohesion: 0.13
Nodes (15): DeviceDetailModalProps, formatAge(), InteractiveTerminalModalProps, TerminalMessage, FrameRect, PREVENT_DEFAULT_KEYS, RemoteControlModalProps, SessionStatus (+7 more)

### Community 71 - "Community 71"
Cohesion: 0.20
Nodes (10): newInventoryCollector(), newInventoryCollector(), NewCollector(), NewCollector(), Collector, CollectOnce(), initialOffset(), NewScheduler() (+2 more)

### Community 72 - "Community 72"
Cohesion: 0.29
Nodes (7): HashPassword(), NewHandler(), writeErr(), writeJSON(), AuditLogger, Handler, SessionRevoker

### Community 73 - "Community 73"
Cohesion: 0.23
Nodes (12): setResolver(), setResolverFor(), TestApplyClearsPreviousFirewallRules(), TestApplyNarrowsRatherThanAccumulates(), TestApplySurvivesAnUnavailableFirewallButReportsIt(), TestCleanApplyIsNotMarkedDegraded(), testEngine(), TestHostsWriteFailureIsFatal() (+4 more)

### Community 74 - "Community 74"
Cohesion: 0.22
Nodes (8): attachToInputDesktop(), isExtendedVK(), NewPlatformCapturer(), bitmapInfoHeader, keyboardInput, mouseInput, win32Input, WindowsCapturer

### Community 75 - "Community 75"
Cohesion: 0.18
Nodes (12): goChildArgv(), TestWorkerCommandLineCarriesNoSecret(), TestWorkerCommandLineReachesAGoChildIntact(), TestWorkerFlagNamesMatchTheWorkerMain(), SpawnSessionWorker(), activeInteractiveSession(), primaryToken(), quoteArg() (+4 more)

### Community 76 - "Community 76"
Cohesion: 0.12
Nodes (5): NewManager(), NewManager(), Manager, otherManager, windowsManager

### Community 77 - "Community 77"
Cohesion: 0.31
Nodes (5): AuditLogger, Handler, NewHandler(), writeErr(), writeJSON()

### Community 78 - "Community 78"
Cohesion: 0.21
Nodes (14): env, contains(), dialAgent(), enroll(), expectDialFail(), mustJSON(), newEnv(), postJSON() (+6 more)

### Community 79 - "Community 79"
Cohesion: 0.18
Nodes (8): DevicePatch, FleetPatchSummary, InstallRequest, PatchInstallJob, PatchInstallReport, PatchScanReport, Repository, reconcileAbsentPatches()

### Community 80 - "Community 80"
Cohesion: 0.18
Nodes (10): collectClassNames(), findMissing(), loadTs(), main(), probeResolves(), require, root, RUNTIME_BUILT (+2 more)

### Community 81 - "Community 81"
Cohesion: 0.15
Nodes (8): NewID(), isUniqueViolation(), ChangePasswordRequest, CreateUserRequest, Repository, ResetPasswordRequest, UpdateUserRequest, User

### Community 82 - "Community 82"
Cohesion: 0.15
Nodes (14): ToastContext, ToastContextType, ToastItem, ToastType, LevelFilter, levelOf(), LEVELS, LogPage() (+6 more)

### Community 83 - "Community 83"
Cohesion: 0.12
Nodes (16): compilerOptions, allowImportingTsExtensions, erasableSyntaxOnly, lib, module, moduleDetection, noEmit, noFallthroughCasesInSwitch (+8 more)

### Community 84 - "Community 84"
Cohesion: 0.23
Nodes (8): firstNonEmpty(), macParseMemory(), macProfileHardware(), macSysctl(), CollectNICs(), DedupeSoftware(), macCollector, macHardwareJSON

### Community 85 - "Community 85"
Cohesion: 0.20
Nodes (13): handleServiceAction(), triggerRestart(), DefaultConfig(), GetExecutablePath(), systemdUnit(), TestDefaultConfigNamesTheServiceThePackagingInstalls(), TestGetExecutablePathIsAbsolute(), TestSystemdUnitCarriesTheArgumentsAndTheRecoverySettings() (+5 more)

### Community 86 - "Community 86"
Cohesion: 0.27
Nodes (8): CPU, Disk, Hardware, Model, OSDetail, Report, winCollector, NIC

### Community 87 - "Community 87"
Cohesion: 0.28
Nodes (3): removeManagedSection(), writeAtomic(), Engine

### Community 88 - "Community 88"
Cohesion: 0.21
Nodes (15): helperArgs(), helperMode(), helperSelf(), processAlive(), TestCappedWriterKeepsHead(), TestCappedWriterUnderCap(), TestHelperNoop(), TestHelperOutput() (+7 more)

### Community 89 - "Community 89"
Cohesion: 0.14
Nodes (6): ActivityItem, Alert, OSMetric, Repository, SiteMetric, Summary

### Community 90 - "Community 90"
Cohesion: 0.22
Nodes (14): TestRunEnrollOnly_DeviceIDMismatch(), TestRunEnrollOnly_InvalidToken(), TestRunEnrollOnly_MissingServer(), TestRunEnrollOnly_MissingToken(), TestRunEnrollOnly_Success(), runEnrollOnly(), Exchange(), Load() (+6 more)

### Community 91 - "Community 91"
Cohesion: 0.18
Nodes (12): Serve(), extractUninstallerBase(), frameworkSilentSwitches(), isElevated(), isInnoExeName(), requireElevatedForUninstall(), resolveSilentArgs(), isRegularFile() (+4 more)

### Community 92 - "Community 92"
Cohesion: 0.22
Nodes (8): applyInactivityLock(), applyRestrictions(), applyUSBStoragePolicy(), applyWallpaper(), applyWindowsUpdate(), revertWallpaper(), setOrDeleteDWord(), winBackend

### Community 93 - "Community 93"
Cohesion: 0.21
Nodes (4): AlertIncident, Repository, boolToInt(), NewID()

### Community 94 - "Community 94"
Cohesion: 0.20
Nodes (7): Handler, NewHandler(), TestWriteCSVQuotesFieldsContainingCommas(), TestWriteCSVReportsARowErrorAsAFailedDownload(), TestWriteCSVServesTheCompleteReportWhenEncodingSucceeds(), writeCSV(), writeJSON()

### Community 95 - "Community 95"
Cohesion: 0.21
Nodes (11): compact(), nameMatches(), normalizeProgramName(), exactMatches(), resolveUninstallerPath(), TestComponentNameLeavesBothCandidates(), TestExactMatchesBeatsLongerPrefix(), TestNameMatches() (+3 more)

### Community 96 - "Community 96"
Cohesion: 0.21
Nodes (10): TestApplyUpdate_EmptyDownloadURLDoesNotPanic(), NewEngine(), runApply(), TestAChecksumMismatchStillRollsBack(), TestAnUnbootableReleaseIsNotReportedAsASuccessfulUpdate(), TestTheOriginalBinaryIsRestoredAfterAFailedHealthCheck(), TestAgentEngineManifestVerification(), fakeExecutable() (+2 more)

### Community 97 - "Community 97"
Cohesion: 0.26
Nodes (12): campaignFixture, newCampaignFixture(), TestACampaignIsNotRolledUpEarlyByAProgressReport(), TestACampaignReachesCompletedWhenEveryDeviceReports(), TestACampaignWhoseDevicesAllFailedIsFailed(), TestACampaignWithOneSuccessIsCompleted(), newReportFixture(), report() (+4 more)

### Community 98 - "Community 98"
Cohesion: 0.20
Nodes (12): agent_commands, audit_logs, idx_audit_created, idx_commands_created, idx_commands_device_status, idx_devices_last_seen, idx_devices_status, auth_sessions (+4 more)

### Community 99 - "Community 99"
Cohesion: 0.27
Nodes (13): TestDevicesWithoutCapability(), TestDevicesWithoutCapabilityIgnoresUnknownIDs(), TestDevicesWithoutCapabilityReportsOnlyTheIncapableOnes(), idStr(), mustExec(), TestSyncDeploymentStatusRollsUpFailedWhenNothingSucceeded(), addTask(), sweepFixture() (+5 more)

### Community 100 - "Community 100"
Cohesion: 0.29
Nodes (11): commandRows(), postUninstall(), TestUninstallByNameDispatchesToAnOnlineCapableAgent(), TestUninstallByNameRefusesADeviceWithNoStoredCapabilities(), TestUninstallByNameRefusesAnAgentThatCannotHandleIt(), TestUninstallByNameRefusesAnEmptyName(), TestUninstallByNameRefusesAnOfflineEndpoint(), TestUninstallByNameRefusesAnUnknownDevice() (+3 more)

### Community 101 - "Community 101"
Cohesion: 0.23
Nodes (9): scanOS(), aptCategory(), scanApt(), scanDnf(), scanOS(), scanOS(), Engine, PatchItem (+1 more)

### Community 102 - "Community 102"
Cohesion: 0.24
Nodes (9): isRebootRequiredExit(), resolveSilentArgs(), findDpkgInstalled(), findInstalled(), findRpmInstalled(), firstLine(), runPlatformUninstall(), resolveTarget() (+1 more)

### Community 103 - "Community 103"
Cohesion: 0.18
Nodes (10): PolicySettings, USBStorageMode, WallpaperStyle, WindowsUpdateMode, NewEngine(), newPlatformBackend(), TestEngine_Report(), newPlatformBackend() (+2 more)

### Community 104 - "Community 104"
Cohesion: 0.21
Nodes (11): P(), DEVICE_STATUSES, DEVICE_TABS, DevicesQuery, DeviceStatus, DeviceTab, MAX_PAGE, PAGE_SIZES (+3 more)

### Community 105 - "Community 105"
Cohesion: 0.17
Nodes (12): defaultCredsPath(), envOr(), guardAgentGoroutine(), inventoryPeriod(), inventoryStaggerWindow(), main(), runAgent(), runRCWorker() (+4 more)

### Community 106 - "Community 106"
Cohesion: 0.26
Nodes (11): NewSession(), TestABlankModeBecomesFullControl(), TestANoticeUsesTheSameShapeAsTheCaptureRefusal(), TestAnUnknownModeIsNotStored(), TestASessionKeepsTheRelayURLItWasGiven(), TestSendNoticeOnADeadSessionIsSafe(), TestStopIsIdempotent(), TestSwitchingToViewOnlyReleasesHeldKeys() (+3 more)

### Community 107 - "Community 107"
Cohesion: 0.24
Nodes (7): Engine, releaseArch(), releaseOSName(), belowMinimum(), parseVersion(), SignedManifest, UpdateParams

### Community 108 - "Community 108"
Cohesion: 0.23
Nodes (4): ExecResultReport, RemoteExecution, Repository, TerminalSession

### Community 109 - "Community 109"
Cohesion: 0.21
Nodes (10): incidentCount(), newTickingEvaluator(), TestTheBackgroundEvaluatorActuallyEvaluates(), TestTheEvaluatorStopsWhenItsContextIsCancelled(), waitForIncidents(), NewEvaluator(), newRuleFixture(), TestUpdateRuleKeepsTheFieldsTheClientDidNotSend() (+2 more)

### Community 110 - "Community 110"
Cohesion: 0.21
Nodes (11): ASSET_STATUSES, AssetLicensePage(), AssetLicensePageProps, AssetTab, emptyAssetForm(), fromDateInput(), toDateInput(), AssetSummaryDTO (+3 more)

### Community 111 - "Community 111"
Cohesion: 0.26
Nodes (11): formatBytes(), formatTime(), isJobStatus(), isRunningStatus(), jobPill(), MaintenancePage(), taskPill(), MaintenanceJobDTO (+3 more)

### Community 112 - "Community 112"
Cohesion: 0.27
Nodes (8): defaultHostsPath(), NewEngine(), TestNormalizeDomainStripsWhatOperatorsPaste(), newResolver(), normalizeDomain(), resolveEntry, resolver, TestNetworkFilter_AgentHostsEngine()

### Community 114 - "Community 114"
Cohesion: 0.25
Nodes (8): TestAsInt64(), TestCimAcceptsBareObjectEvenThoughCompressDoesNotEmitOne(), TestCimRejectsBrokenOutput(), TestCimShapeIsExactlyWhatPowerShellEmits(), asInt64(), cimQuery(), winCollector, parseCIMRows()

### Community 115 - "Community 115"
Cohesion: 0.31
Nodes (9): deployment_tasks, idx_deploy_tasks_deploy, idx_deploy_tasks_device, idx_deploy_tasks_status, idx_packages_target, software_deployments, software_packages, idx_software_deployments_action (+1 more)

### Community 116 - "Community 116"
Cohesion: 0.38
Nodes (9): configureLinuxDNS(), ConfigureLoopbackDNS(), configureWindowsDNS(), getActiveLinuxInterface(), getConnectedWindowsAdapters(), GetPrimaryUpstreamDNS(), getWindowsDNSBackup(), RestoreOriginalDNS() (+1 more)

### Community 117 - "Community 117"
Cohesion: 0.27
Nodes (4): Engine, parsePublicKey(), loadPublicKey(), agentConfig

### Community 118 - "Community 118"
Cohesion: 0.27
Nodes (3): Repository, boolToInt(), newID()

### Community 119 - "Community 119"
Cohesion: 0.36
Nodes (9): device_patches, idx_patch_jobs_device, idx_patch_jobs_operator, idx_patch_jobs_status, idx_patches_device, idx_patches_patchid, idx_patches_severity, idx_patches_state (+1 more)

### Community 120 - "Community 120"
Cohesion: 0.42
Nodes (10): newTestDB(), seedDevice(), seedInventory(), TestDashboardAlerts(), TestDashboardHTTPHandlers(), TestDashboardOSMetrics(), TestDashboardSiteMetrics(), TestDashboardSummaryEmpty() (+2 more)

### Community 121 - "Community 121"
Cohesion: 0.33
Nodes (5): lockFile(), TestCleanupTempStaysGreenWithALockedFileInTheRoot(), TestLockedFileIsNotAPrivilegeRefusal(), TestPermissionDeniedIsStillARefusal(), isPermissionDenied()

### Community 124 - "Community 124"
Cohesion: 0.28
Nodes (3): shortID(), task, TaskQueue

### Community 125 - "Community 125"
Cohesion: 0.28
Nodes (6): NewDispatcher(), sessionCache, sessionCacheEntry, getSessionCache(), CommandHandler, Dispatcher

### Community 126 - "Community 126"
Cohesion: 0.58
Nodes (3): AlertRule, EvalResult, Evaluator

### Community 128 - "Community 128"
Cohesion: 0.31
Nodes (7): devices, idx_devices_os, idx_devices_site, idx_inventory_disk_free, idx_devices_enrollment_token, idx_devices_secret_hash, idx_devices_status_last_seen

### Community 129 - "Community 129"
Cohesion: 0.39
Nodes (8): users, idx_remote_exec_device, idx_remote_exec_started, idx_remote_exec_status, idx_term_sessions_device, idx_term_sessions_status, remote_executions, terminal_sessions

### Community 130 - "Community 130"
Cohesion: 0.44
Nodes (8): idx_sched_device_runs_dev, idx_sched_device_runs_run, idx_scheduled_runs_sched, idx_task_schedules_enabled, scheduled_task_device_runs, scheduled_task_runs, script_templates, task_schedules

### Community 131 - "Community 131"
Cohesion: 0.39
Nodes (7): newProgressFixture(), reportAs(), taskRow(), TestAnAgentCannotReportProgressForAnotherDevicesTask(), TestAnAgentCanStillReportItsOwnTask(), TestAnUnauthenticatedAgentCannotReportProgress(), TestProgressOnAnUnknownTaskIsIndistinguishableFromAnotherDevicesTask()

### Community 133 - "Community 133"
Cohesion: 0.22
Nodes (8): INSTALL_ARG_PRESETS, PackageType, SoftwarePageProps, SoftwareTab, UNINSTALL_ARG_PRESETS, DeploymentTaskDTO, SoftwareDeploymentDTO, SoftwarePackageDTO

### Community 134 - "Community 134"
Cohesion: 0.25
Nodes (4): TestInitialOffset(), TestScheduler_ImmediateAndCancel(), mockCollector, mockReporter

### Community 135 - "Community 135"
Cohesion: 0.29
Nodes (3): NewTerminalManager(), ProcessSession, TerminalManager

### Community 136 - "Community 136"
Cohesion: 0.25
Nodes (6): inventoryDiff, softwareDiff, decodeSoftwareNames(), diffSoftware(), TestDiffSoftwareAddRemove(), TestDiffSoftwareMalformed()

### Community 137 - "Community 137"
Cohesion: 0.39
Nodes (6): multipartBody(), newUploadRouter(), setMaxPackageBytes(), TestAnOversizedPackageUploadIsRefused(), TestAPackageUnderTheLimitStillUploads(), TestTheFormFieldsStillFitUnderTheCap()

### Community 138 - "Community 138"
Cohesion: 0.43
Nodes (7): device_group_members, device_groups, device_inventory, idx_group_members_device, idx_groups_name, idx_inventory_collected, idx_inventory_device

### Community 139 - "Community 139"
Cohesion: 0.46
Nodes (7): alert_incidents, alert_rules, idx_alert_dedup, idx_alert_incidents_device, idx_alert_incidents_rule, idx_alert_incidents_status, idx_alert_rules_enabled

### Community 141 - "Community 141"
Cohesion: 0.43
Nodes (8): NewHub(), dialAgent(), TestDisconnectWithdrawsAndClosesAgentSocket(), TestSendIsNotClosed(), TestSendOnAConnBeingTornDownNeverPanics(), TestSupersededConnectionStopsWithoutRacingItsPump(), TestWritePumpStillDeliversWhatWasQueuedFirst(), TestWritePumpStopsWhenTheConnectionIsTornDown()

### Community 142 - "Community 142"
Cohesion: 0.29
Nodes (5): releaseDownloadURL(), setupTestServer(), TestAgentConfigEndpoint(), TestSignReleaseEndpoint(), uploadReleaseFixture()

### Community 143 - "Community 143"
Cohesion: 0.48
Nodes (6): hardware_assets, idx_hardware_assets_dev, idx_hardware_assets_status, idx_software_licenses_name, license_allocations, software_licenses

### Community 144 - "Community 144"
Cohesion: 0.52
Nodes (6): idx_maint_jobs_started, idx_maint_tasks_device, idx_maint_tasks_job, idx_maint_tasks_status, maintenance_jobs, maintenance_tasks

### Community 145 - "Community 145"
Cohesion: 0.29
Nodes (3): offlineHub, oneOnlineHub, stubHub

### Community 146 - "Community 146"
Cohesion: 0.33
Nodes (3): bytesReader(), countingReader, countingReader

### Community 148 - "Community 148"
Cohesion: 0.60
Nodes (4): encodeUTF16(), TestDecodeOutput(), utf16be(), utf16le()

### Community 150 - "Community 150"
Cohesion: 0.53
Nodes (5): device_filter_states, filter_policies, filter_rules, idx_filter_policies_target, idx_filter_rules_policy

### Community 151 - "Community 151"
Cohesion: 0.53
Nodes (5): agent_releases, device_update_tasks, idx_agent_releases_lookup, idx_device_update_tasks_dev, update_campaigns

### Community 152 - "Community 152"
Cohesion: 0.33
Nodes (5): plugins, rules, react/only-export-components, react/rules-of-hooks, $schema

### Community 153 - "Community 153"
Cohesion: 0.70
Nodes (4): findInstalled(), lastComponent(), listReceipts(), receiptVersion()

### Community 154 - "Community 154"
Cohesion: 0.60
Nodes (3): queueFixture, TestAFailedTaskCanStillBeCorrectedToSuccess(), TestAFinishedTaskIsNotDrivenBackwards()

### Community 155 - "Community 155"
Cohesion: 0.50
Nodes (5): hwJSON, hwModel, decodeHW(), hwModelValue(), modelsEqual()

### Community 157 - "Community 157"
Cohesion: 0.70
Nodes (4): idx_rc_sessions_device, idx_rc_sessions_operator, idx_rc_sessions_status, remote_control_sessions

### Community 158 - "Community 158"
Cohesion: 0.60
Nodes (4): directory_contacts, directory_sync_config, idx_directory_contacts_dept, idx_directory_contacts_name

### Community 164 - "Community 164"
Cohesion: 0.67
Nodes (3): device_system_policy_states, idx_system_policies_target, system_policies

### Community 165 - "Community 165"
Cohesion: 0.50
Nodes (4): isPrivateOrBlockedIP(), newSafeWebhookClient(), TestIsPrivateOrBlockedIP(), TestSafeWebhookClientRejectsPrivateIP()

## Knowledge Gaps
- **10 isolated node(s):** `github.com/henokakunemail-stack/Endpoint-Manager`, `oxlint`, `tailwindcss`, `@types/node`, `@types/react` (+5 more)
  These have ≤1 connection - possible missing edges. (Counts symbols only; 482 node(s) total have ≤1 connection when file, concept and rationale nodes are included.)
- **75 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `UserIDFromContext()` connect `Community 15` to `Community 0`, `Community 1`, `Community 3`, `Community 35`, `Community 39`, `Community 72`, `Community 43`, `Community 77`, `Community 46`, `Community 50`, `Community 18`, `Community 21`, `Community 53`, `Community 26`, `Community 27`, `Community 60`, `Community 29`, `Community 62`?**
  _High betweenness centrality (0.015) - this node is a cross-community bridge._
- **What connects `github.com/henokakunemail-stack/Endpoint-Manager`, `oxlint`, `tailwindcss` to the rest of the system?**
  _10 weakly-connected nodes found - possible documentation gaps or missing edges._
- **Should `Community 0` be split into smaller, more focused modules?**
  _Cohesion score 0.06809097414951738 - nodes in this community are weakly interconnected._
- **Why does `buildServer()` connect `Community 27` to `Community 0`, `Community 2`, `Community 3`, `Community 4`, `Community 5`, `Community 6`, `Community 9`, `Community 141`, `Community 15`, `Community 17`, `Community 18`, `Community 19`, `Community 21`, `Community 22`, `Community 23`, `Community 29`, `Community 31`, `Community 34`, `Community 35`, `Community 36`, `Community 38`, `Community 39`, `Community 40`, `Community 43`, `Community 46`, `Community 50`, `Community 51`, `Community 52`, `Community 53`, `Community 56`, `Community 60`, `Community 62`, `Community 69`, `Community 72`, `Community 77`, `Community 94`, `Community 109`, `Community 120`?**
  _High betweenness centrality (0.010) - this node is a cross-community bridge._
- **Should `Community 1` be split into smaller, more focused modules?**
  _Cohesion score 0.026838709677419356 - nodes in this community are weakly interconnected._
- **Why does `dialect` connect `Community 5` to `Community 0`?**
  _High betweenness centrality (0.010) - this node is a cross-community bridge._
- **Should `Community 2` be split into smaller, more focused modules?**
  _Cohesion score 0.028146183318597112 - nodes in this community are weakly interconnected._