# Nocturn — Testplan (Greenfield: `agentkit` + `app/*`)

> Zu implementierende Testfälle für die **neue Welt** (`agentkit`-Library + `app/*`-Consumer). Das
> alte `internal/*`-Modul ist **nicht** Teil dieses Plans.
>
> Aktueller Bestand (NICHT duplizieren): `agentkit/guards_paused_test.go`
> (`TestActiveSince_ExcludesPausedSpan`, `TestPausedClock_SharedAcrossNesting`,
> `TestPause_NoClock_NoOp`), `app/chat/manager_test.go`
> (`TestManager_TurnSurvivesWithoutViewer_AndOpenShares`), `app/chat/store_tools_test.go`
> (`TestStore_AppendTools_IndexAlignedAndNoLostUpdate`, `TestStore_AppendTools_NoTranscript_NoOp`).

## Konventionen (CLAUDE.md §9)
- Externes `_test`-Paket (`agentkit_test`, `gate_test`, `chat_test`, …) für die öffentliche API;
  internes (gleicher Paketname) nur wo Unexportiertes direkt geprüft werden muss (pro Fall vermerkt).
- Table-driven wo viele Fälle.
- Fakes über Interfaces (Model/LLM, Store, Notifier, Approver, Sink, Resolver, Sessions).
- Zeit-abhängig via `testing/synctest` (Go 1.25, echte `time`, Fake-Clock im Bubble) — **keine**
  Clock-Injektion. Markiert mit **[synctest]**.
- Blockierende Funktionen mit Goroutine + Kanälen koordinieren (kein `sleep`); `-race` grün.
- Compile-Zeit-Asserts: `var _ agentkit.LLM = (*openai.Client)(nil)` etc.

## Zwei Design-vs-Code-Lücken (vor dem Schreiben klären)
1. **`app/hitl/broker.go` hat KEIN HMAC-Single-Use/Expiry-Token und KEIN ntfy** (entgegen CLAUDE.md).
   Real: zufällige 12-hex-id, In-Memory-`pending`, gepufferter `chan int` (first-answer-wins), 2-min-
   Timeout, Placeholder-`LogPusher`. Die „deny kann nicht zu approve gefälscht werden / single-use /
   expiry"-Properties haben **noch keine Implementierung** zum Testen — Fälle unten sind gegen den
   echten Code geschrieben; die Token-Fälle sind als „falls das Design landet" markiert.
2. **`app/tools` gaten über `agentkit/gate.Check(Action{Kind,Target})`** — alle Tool-Gating-Fälle
   gegen `gate` schreiben (die einzige Guard-Naht der neuen Welt).

## Gemeinsame Fakes (einmal bauen)
- `fakeLLM` — `agentkit.LLM.Next`; scripted `[]Step` (einer pro Call), optionaler Fehler, zeichnet
  `conv`+`tools` auf. Blockierende Variante (Signal-Kanal hält einen Turn offen) für Cancel/Inflight/
  reap-while-running.
- `fakeStore` — `agentkit.Store`; Maps + Call-Counter für Save/Load/List; `saveErr`/`loadErr`.
- `fakeTool`/`NewTool(...)`-Closures — zeichnen ctx (Frame/Budget) auf, blocken auf Kanal, geben Wert/
  Fehler zurück oder rufen `agentkit.Pause`.
- `captureSink` — `func(Event)` mit Mutex-`[]Event`, via `WithSink`; Haupt-Beobachtung für Loop/Frame.
- `fakeApprover` (gate) — `Approver.Ask`; scripted `(approved, grant, recall, err)`, zeichnet `Action`
  auf, kann blocken.
- `fakePolicy` = `gate.PolicyFunc`; seeded `MemGrants`.
- `httptest.Server` (net, openai-SSE), WAT/wasm-Gast-Korpus (sandbox), fake `Notifier`/`Pusher`/
  `Resolver`/`Sessions`.

---

# 1 — `agentkit`

## session.go — der Agent-Loop (`agentkit_test`, meist über `Once`+captureSink; async über `NewSession`)

### Kritisch
- `TestOnce_FinalAnswer_NoTools` — ein `Step{Answer:"hi"}` → Once liefert `("hi",nil)`, Historie = `[{user},{assistant,"hi"}]`.
- `TestOnce_ToolCallRoundTrip` — step1 tool-call, step2 answer; fake-Tool liefert "R"; assert 2. `conv` enthält Assistant-tool-call + `RoleTool{ToolCallID, "R"}` in Reihenfolge.
- `TestOnce_ParallelToolExecution` — ein Step mit 3 Calls, jeder blockt auf Barrier; assert alle 3 concurrent, Ergebnisse in **Call-Reihenfolge** (index-aligned), nicht Completion-Reihenfolge.
- `TestOnce_MaxStepsStop` — `WithMaxSteps(2)`, immer tool-calls → `ErrMaxSteps`, genau 2 `Next`.
- `TestOnce_ToolErrorFedBackNonFatal` — Tool-Fehler → `RoleTool`-Content `"error: boom"`, Loop läuft weiter zu finaler Antwort (`err==nil`).
- `TestOnce_UnknownToolNonFatal` — unbekanntes Tool → Content `agentkit: unknown tool "x"`, Loop weiter.
- `TestOnce_TokenLimitStop_ProviderUsage` — `WithTokenLimit(100)`, 2×`Tokens{60}` → nach step2 `ErrTokenLimit`, kein 3. `Next`.
- `TestOnce_TokenLimitStop_OnFinalAnswer` — Limit 50, finaler Step `Tokens{80}` → Antwort geliefert, aber `ErrTokenLimit`.
- `TestOnce_TokenizerFallback_WhenProviderReportsZero` — `WithTokenizer`, Provider meldet `Total:0` → geschätzt (TurnEnd.Tokens>0), Limit greift; Gegenprobe: Provider-Usage vorhanden → Tokenizer NICHT konsultiert.
- `TestOnce_HistoryAndProducedAccumulation` — multi-round; `produced`-Reihenfolge assistant(tc)→tool-results→…→finaler assistant; `assemble()` re-sendet system+Historie.
- `TestOnce_TokenTotalsAccumulateOnTurnEnd` — zwei Round-Trips `Tokens{10,5,15}` → `TurnEnd.Tokens=={20,10,30}`.
- `TestTurn_TurnStartTurnEndBracket` — via `NewSession`+Submit; erstes Event `TurnStart{Frame:0}`, letztes `TurnEnd{Frame:0,Err:nil,Tokens}`.
- `TestTurn_PersistOncePerTurn` — fakeStore; `Save` **genau einmal** pro Turn (nicht pro Round), mit voller Post-Turn-Historie.
- `TestTurn_OneRoleUserPerTurnInvariant` — nach N Submits: genau N `RoleUser`, jede gefolgt von ihren Assistant/Tool-Messages (kein User pro Round). **(Linchpin des Forest-Alignments.)**
- `TestSession_SubmitSubscribe_Serialized` — zwei Submits; Turn 2 startet erst nach `TurnEnd` von Turn 1 (serialisierter Single-Worker).
- `TestSession_Cancel_MidTurn` — Tool blockt; `Cancel()` → `TurnEnd.Err` wraps `context.Canceled`, partielle produced appended+persisted, nächstes Submit läuft normal. (Kanal-koordiniert.)
- `TestSession_Close_ClosesStreamAndStopsLoop` — `Close()` → Subscribe-Kanal schließt; Post-Close-`Submit` no-op (kein Block/Panic; `<-s.done`-Zweig).
- `TestSession_ContextCancel_EquivalentToClose` — ctx-Cancel → gleicher Shutdown.
- `TestTurn_ErrTurnTimeout_Surfaced` — **[synctest]**; `WithTimeout(d)`, blockt über `d` → `context.Cause==ErrTurnTimeout` macht `TurnEnd.Err==ErrTurnTimeout` (nicht bare Canceled).
- `TestBuildSession_LoadsPersistedHistory` — fakeStore mit 2 Messages → Session startet mit Historie.
- `TestBuildSession_LoadError_StartsEmpty` — `Load`-Fehler → Historie leer, Warn geloggt, Session nutzbar.

### Edge
- `TestOnce_ModelError_Fatal` — `Next`-Fehler → `agentkit: model call:`-Wrap, keine produced außer User-Row.
- `TestOnce_CtxAlreadyCancelled_ReturnsImmediately` — vor-cancelter ctx → `ctx.Err()` vor jedem `Next`, 0 Calls.
- `TestOnce_EmptyAnswer` — finaler Step `Answer:""` → `("",nil)` + `{assistant,""}`-Message.
- `TestOnce_MaxStepsZero_UsesDefault` — `WithMaxSteps(0)` → `defaultMaxSteps` (16).
- `TestPersist_NoStore_NoOp` / `TestPersist_SaveError_Logged`.
- `TestSink_DoesNotBlockPastCancel` — voller `out` + ctx cancelled → `sink` kehrt via `<-ctx.Done()` zurück.
- `TestAssemble_SystemPromptEphemeral` — `RoleSystem` ans Model, NICHT persistiert.
- `TestToolset_SkillsAddsLoadSkillTool` — `WithSkills` → `toolset()` mergt `load_skill`; ohne Skills Identität (keine Kopie).
- `TestEstimate_TokenizerError_CountsZero`.

## guards.go — pausable deadlines, Token-Budget, Spawn-Caps
(exportiert → `agentkit_test`; `installDeadline`/`spend`/`enterSpawn`/`withTimeout` → same-package)

### Kritisch
- `TestWithTimeout_DeadlineFires_CancelsWithErrTurnTimeout` — **[synctest]** — `context.Cause==ErrTurnTimeout`.
- `TestWithTimeout_InheritIfPresent` — 2. Aufruf auf ctx mit `pausablesKey` → plain WithCancel, kein neuer Deadline (Set-Länge). (same-package)
- `TestWithTimeout_ZeroDuration_NoDeadline`.
- `TestPause_BanksPausedTime_DeadlineNotTripped` — **[synctest]** — Pause über Deadline hinaus, resume → nicht getrippt, Rest-Zeit erst nach echter Aktivzeit.
- `TestWithPausableBudget_AddsToSet_NotInherited` — jeder Aufruf fügt eigenen Deadline hinzu (Set wächst). (same-package)
- `TestWithPausableBudget_PausedByPause` — **[synctest]** — nested Budget feuert nicht während Pause.
- `TestWithPausableBudget_FiresWhenActiveExceeds` — **[synctest]**.
- `TestWithTokenBudget_InheritIfPresent` — 2. Aufruf → ctx unverändert (embedded zieht Parent-Pool). (same-package)
- `TestSpend_OverLimit` / `TestSpend_NoPoolOrNoLimit_NeverOver` / `TestSpend_SharedAcrossNesting`. (same-package)
- `TestEnterSpawn_MaxDepth` / `TestEnterSpawn_MaxSpawns` / `TestEnterSpawn_NoState_NoOp` / `TestWithSpawnLimits_Defaults`.

### Edge
- `TestInstallDeadline_StopFunc_RemovesFromSet` — Cause `context.Canceled` (nicht ErrTurnTimeout). (same-package)
- `TestPause_Idempotent_DoublePause` / `TestPause_AfterFired_RemainingZero` (**[synctest]**). (same-package)
- `TestActiveSince_NeverNegative` — pausedStart>banked → 0.
- `TestEnterSpawn_DepthPerBranch_PopulationShared` — Depth per-branch, Spawns shared → 5. Spawn trippt `ErrMaxSpawns`.

## tool.go — Tool-Bus, Call-id/Frame-Plumbing (`agentkit_test`; `nextCallID`/`withFrame`/`frameFrom` same-package)

### Kritisch
- `TestToolSet_Call_AssignsFreshID` — zwei Calls → ID 1,2 (monoton aus ctx-Counter).
- `TestToolSet_Call_EmitsStartEnd_FrameIsParent` — top-level → `ToolStart{Frame:0,ID:1}`…`ToolEnd{…}`.
- `TestToolSet_Call_ChildFrameNests` — Tool liest `FrameFrom(ctx)` → == eigene Call-ID (lief unter `withFrame(ctx,id)`).
- `TestToolSet_Call_UnknownTool_NonFatalError` — `("",err)` + KEIN Start/End emittiert.
- `TestToolSet_Call_DurationExcludesApprovalWait` — **[synctest]**/Kanal — Tool ruft `Pause` → `ToolEnd.Duration ≈ Aktivzeit`.
- `TestFrameFrom_TopLevelZero` / `TestNextCallID_NoCounter_ReturnsZero` (sp) / `TestWithCounter_InheritIfPresent` (sp).
- `TestNewTool_ValidatesName` (table: valid / leer / >64 / bad-char) / `TestNewTool_NilFunc_Error`.
- `TestNewToolSet_DuplicateName_Error` / `NilTool_Error` / `InvalidSpec_Error`.
- `TestToolSet_Specs_SortedByName`.
- `TestToolSet_Select_ReturnsSubsetCopy` — gedroppter Name → unknown; Original unverändert (Attenuation kann nicht weiten).

### Edge
- `TestFuncTool_Call_MaxCharsTruncation` — Result auf N Runen, Fehlerpfad raw.
- `TestTruncateChars_RuneSafe` (sp) / `TestToolSpec_Validate_DescriptionLengthNotEnforced`.
- `TestToolSet_Call_ToolErrorPropagatedOnToolEnd` — `ToolEnd.Err` gesetzt, Call gibt Fehler (Loop entscheidet Non-Fatality).

## message.go / event.go (`agentkit_test`; `stampFrame` same-package)

### Kritisch
- `TestTokenCount_Add` / `TestMessage_JSONOmitempty` (plain: keine toolCalls/toolCallID/durationMs; tool-result: toolCallID+durationMs).
- `TestEmit_StampsFrameFromCtx` — `withFrame(ctx,7)` → geliefertes Event `Frame==7`.
- `TestEmit_NoSink_NoOp` (fail-open) / `TestStampFrame_AllVariants` (sp) / `TestWithSink_NilSink_CtxUnchanged` / `TestSinkFrom_RoundTrip`.

### Edge
- `TestEvent_SealedInterface` — compile-time `var _ Event = …{}` für alle sechs.

## agent.go — AgentTool / Sub-Agent-Spawn (`agentkit_test`)

### Kritisch
- `TestAgentTool_Spec` — Name, Beschreibung erwähnt Sub-Agent, Parameter `input` required.
- `TestAgentTool_RunsToFinalAnswer_AsToolResult` — Sub-Agent-Antwort wird Tool-Result.
- `TestAgentTool_SharesParentSink_EventsHaveNonZeroFrame` — ALLE Sub-Agent-Events tragen die AgentTool-Call-ID als Frame.
- `TestAgentTool_SubAgentInternalsNotPersisted` — kein Store am Sub-Agent → kein `Save`; Parent hält nur das Tool-Result.
- `TestAgentTool_InheritsBudgetAndCounter` — Sub-Agent zieht denselben Token-Pool + setzt die id-Sequenz fort.
- `TestAgentTool_EnterSpawn_MaxDepth_AsToolResult` / `MaxSpawns_AsToolResult` — Cap als Tool-Result (Turn nicht gecrasht).
- `TestAgentTool_InvalidArgs_Error`.

### Edge
- `TestAgentTool_LeafHasNoAgentTools` / `TestAgentTool_OptsAppendedAfterDefaults` (Caller-opts gewinnen).

## gate/ — die HITL-/Guard-Naht (`gate_test`; `from`/min-recall same-package)
> **Das ist der „Guard", den die `app/tools` real rufen** (Policy→Grants→Approver). Fake `Approver`,
> fake `Policy` (`PolicyFunc`), `MemGrants`.

### Kritisch (Check)
- `TestCheck_NoMachinery_IsOpen` — ohne `With(...)` → nil (Gating opt-in).
- `TestCheck_PolicyAllow_ReturnsNil` — Approver nie gerufen (Call-Count 0).
- `TestCheck_PolicyDeny_ReturnsErrDenied` — Approver nie gerufen.
- `TestCheck_DenyBeatsGrant` — Deny trotz deckendem stehenden Grant → `ErrDenied` (deny nicht überschreibbar).
- `TestCheck_Ask_CoveringGrant_NoApprover` — `AskWith(RecallSession)` + deckender Grant + Approver nil → nil (Grant erfüllt Ask ohne Fragen).
- `TestCheck_Ask_NoApprover_Unattended_Denied` — kein Grant, Approver nil → `ErrDenied` (fail-closed).
- `TestCheck_Ask_ApproverApproves_Remembers` — `(true,grant,RecallAlways,nil)` mit Policy-Ceiling `RecallSession` → nil + `Remember` mit `min=RecallSession`.
- `TestCheck_Ask_ApproverDenies_ReturnsErrDenied` — nichts gemerkt.
- `TestCheck_RecallNever_SkipsGrantCache` — trotz deckendem Grant wird gefragt (irreversibel = jedes Mal).
- `TestCheck_RecallNever_ApproverApproves_NotRemembered` — `min(Never,chosen)=Never` → kein Remember.
- `TestCheck_ApproverError_Wrapped` — `gate: approver:`-Wrap, ≠ `ErrDenied`.
- `TestCheck_PausesTurnClockAroundAsk` — **[synctest]** — `agentkit.Pause` vor `Ask`, `resume` danach; Wall-Clock nicht verbraucht.

### Kritisch (wrap.go / grant.go / policy.go)
- `TestWrap_GatesOnToolName` — Deny-Policy für Kind → `ErrDenied`, inneres Tool nie gerufen.
- `TestWrap_Allowed_CallsUnderlying` / `TestWrapAll_WrapsEveryTool_OriginalsUnchanged`.
- `TestMemGrants_AllowedAndRemember` (set-dedup: wiederholtes Remember → kein Wachstum) / `KindWildcard` / `UsesSuppliedMatcher`.
- `TestExactMatch_StarAndEquality`.
- `TestRecall_Ordering_MinRestrictiveWins` (sp) — `Never<Session<Always` (iota load-bearing).
- `TestRuling_Constructors` — Zero-Recall == RecallNever (fail-closed).

### Edge
- `TestMemGrants_CustomMatcher_RunOutsideLock` (kein Deadlock, snapshot-then-match) / `NilMatcher_DefaultsExact` / `ConcurrentRememberAllowed_NoRace`.
- `TestWith_InheritedByNestedCtx` (sp) — fließt zu Sub-Agenten, kann nicht weiten.

## openai/ — go-openai-Adapter (`openai_test`; `httptest.Server` mit SSE, kein echtes Netz)

### Kritisch
- `TestNext_FinalAnswer_StreamsTokens` — Content-Deltas + Usage-Chunk → `Answer` reassembled, `Tokens` aus Usage, captureSink sah Token-Reihenfolge.
- `TestNext_ToolCalls_NativeIDPlumbing` — id im ersten Fragment → `ToolCalls[0].ID` == id, Tool+Args reassembled.
- `TestNext_ToolCalls_IDFallback` — ohne id → `agentkit_call_0`.
- `TestNext_ParallelToolCalls_AccumulatePerIndex` — index 0/1 nicht fusioniert, First-Seen-Reihenfolge.
- `TestNext_ReasoningDeltas_EmitThinking` — `Thinking{}` emittiert, nicht in Answer gefaltet.
- `TestBuildMessages_RoleMapping` (table: System/Assistant+ToolCalls/Tool+ToolCallID/User).
- `TestNext_EffortFromCtxOverridesDefault` / `TestNext_MaxTokensSet` (0→unset).
- `TestNext_StreamCreateError_Wrapped` (`openai: create stream:`) / `TestNext_RecvError_Wrapped`.
- `TestRenderSchema` (table: nil→object leer; props/required; array/items; enum; lowercase types; nested).
- `TestClient_ImplementsLLM` — `var _ agentkit.LLM = (*Client)(nil)`.

### Edge
- `TestNew_BaseURLV1Suffix` (`/v1` angehängt) / `TestNext_IncludeUsageRequested` / `TestNext_EmptyChoicesChunk_Skipped` / `TestBuildMessages_AssistantNoToolCalls`.

## runtime/ — Composition-Root (`runtime_test`)

### Kritisch
- `TestNew_GateWrapsTools_WhenPolicySet` — Tools `WrapAll`-gewrappt, default `MemGrants` wenn nil.
- `TestNew_NoGate_ToolsUngated` / `TestRuntime_Once_InstallsGateOnCtx` (Deny → ErrDenied als Tool-Result).
- `TestRuntime_SessionOpts_Order_ExtraOverridesBase` / `TestRuntime_Session_WiresToolsAndSkills` (Specs incl. `load_skill`).

### Edge
- `TestRuntime_Once_NoGate_NoInstall` / `TestNew_GrantsProvided_NotOverwritten`.

## Supporting Ports — schema/skill/store/tokenizer/diagnostic (`agentkit_test`)

### Kritisch
- `TestParseSchema_RoundTripSupportedSubset` (unsupported keys gedroppt) / `EmptyYieldsNil` / `MalformedError`.
- `TestObject_Prop_Require_Chaining` / `WithEnum`.
- `TestSkill_Validate` (table: bad name/`anthropic`/`claude`/leere/überlange Description) / `TestNewSkillSet_DuplicateName_Error`.
- `TestSkillSet_LoadTool_ReturnsBody` (unknown/malformed → Fehler) / `Specs_SortedBodiesOmitted`.
- `TestMemStore_SaveLoad_CopySemantics` (Store hält Kopie; Load gibt Kopie) / `Load_UnknownID_NilNoError` / `List_Sorted`.
- `TestApproxTokenizer_Count` (~(runes+3)/4; leer→0; multibyte=Runen; nie Fehler).

### Edge
- `TestDiagnostics_ConcurrentFeeders` (**-race**) / `HasErrors` / `TestDiagnose_NoCollector_NoOp` / `TestLevel_String` / `SlogLogger_NilYieldsNop` / `NopLogger_Discards`.
- Compile-time: `var _ Store = (*MemStore)(nil)`, `var _ Tokenizer = approxTokenizer{}`, `var _ Logger = nopLogger{}`, `var _ gate.Grants = (*MemGrants)(nil)`.
- `TestCore_NoHITL_WithoutGate` — eine Deny-würdige Aktion läuft frei, wenn keine gate-Machinery installiert ist (pinnt: agentkit-Core ist HITL-agnostisch).

## agentkit/tools — Helper-HTTP-Tool + Host-Glob (`tools_test`, `httptest`, gate-ctx)
### Kritisch
- `TestHostMatch_WildcardAny` (`"*"`→any) / `Exact` / `SubdomainWildcard` (`*.example.com` matcht base + a.example.com + b.a.example.com).
- `TestHostMatch_SubdomainWildcardRejectsSuffixTrick` — `notexample.com`/`evilexample.com`/`example.com.evil.com` → false (`"."+base`-Guard).
- `TestHostMatch_EmptyMatchesNothing` — `""` matcht nie (auch leerer Host).
- `TestHostSuggestions_WidensSubdomainToParent` (`a.example.com`→`[{net,*.example.com}]`) / `NoWidenForApex` (apex+single-label→nil).
- `TestCall_GatesHostBeforeRequest` — Deny → Fehler, HTTP-Client nie gerufen.
- `TestCall_LimitTruncatesBody` (`WithLimit(10)`) / `InvalidArgs` (kein JSON→"invalid arguments"; `{}`→"invalid url", kein Request).
### Edge
- `TestCall_URLWithoutHostRejected` (`/relativ`, `file:///…` → `u.Host==""` → "invalid url"; kein SSRF).
- `TestCall_HappyPath` (`"200 OK\n<body>"`) / `GateCheckTargetIsHost` (Action `Kind:NetAxis,Target:u.Host`, Suggestions weitergereicht) / `UngatedWhenNoGateMachinery`.
- `TestWithLimit_DefaultIs4000` / `TestSpec_Shape` (`http_get`, `url` required) / `TestParentDomain_Labels` / `TestCall_TransportErrorSurfaced` (`http_get:`-Wrap).

---

# 2 — `app/*` Security + Effekt-Capabilities

## app/sandbox — WASM Zero-Authority (WAT/wasm-Gast-Korpus als Fixture)

### Kritisch (Zero Authority)
- `TestNew_UnknownImport_InstantiateFails` — Gast importiert `nocturn.foo`, keine HostNames → Link-Fehler bei Run (Zero-Authority-Floor).
- `TestRun_NoFilesystemGrant_OpenTraps` — `Workspace==""`, Gast `path_open`/`fd_read` → Trap, keine Host-Datei geöffnet.
- `TestRun_NoNetGrant_NoHostFunc` — kein `nocturn`-Import erreichbar → Link-Fehler.
- `TestRun_HostNotRegistered_FailsLoud` — HostFunc-Name nicht in `hostNames` → `sandbox: host %q not registered` VOR Gast-Ausführung.
- `TestTrampoline_UngrantedDispatcher_FailsClosed` — Import registriert, aber kein Dispatcher diesen Run → Gast bekommt `host function "x" not granted`-String (kein silent allow, kein nil-map-Panic).
- `TestTrampoline_CopiesMemoryViewBeforeReturn` — Host-Fn recorded Bytes; nach Gast-Mutation/free unverändert (wazero-View-Pitfall: `append([]byte(nil), view...)` sofort kopieren).
- `TestRun_DeadlineExceeded_TrapsAndReportsCause` — **[synctest]** — Endlosschleife, kleines Timeout → Fehler wraps `context.DeadlineExceeded` via `context.Cause` (nicht opaker Exit-Code).
- `TestRun_MemoryCapEnforced` — kleine `MaxPages`; `memory.grow` überschreitet → Trap, Host nicht OOM.
- `TestRun_ConcurrentRuns_NoAuthorityCrossing` — **-race** — zwei Runs, je eigener Dispatcher; A's Calls treffen nur A's Dispatcher.

### Edge
- `TestRun_ContextCanceled_ReportsCanceled` (≠ DeadlineExceeded) / `ZeroTimeout_UsesDefault` (**[synctest]**) / `TestNew_ZeroMaxPages_UsesDefault`.
- `TestWriteToGuest_NoMalloc_ReturnsZero` / `EmptyResponse_ReturnsZero`.
- `TestFinish_NormalExitZero_NoError` / `NonZeroExit_Error` (`guest exited with code 3`).
- `TestRun_StdinStdoutStderr_Piped`.
- `TestPausableBudget_NotBurnedDuringHostPause` — **[synctest]** — Host-Fn ruft `agentkit.Pause` → Gast-Budget nicht verbraucht.
- `TestEngine_Reuse_AfterClose_Fails`.

## app/hitl — Out-of-band-Approval-Broker (fake `Sink`, fake `Pusher`; Timeouts **[synctest]**)

### Kritisch (fail-closed)
- `TestAsk_Timeout_FailsClosed` — **[synctest]** — nie beantwortet → nach `approvalTimeout` (2m) `(false, Grant{}, RecallNever, ErrApprovalTimeout)`.
- `TestAsk_NoActiveDevice_NoPusher_DeniesOnTimeout` — **[synctest]**.
- `TestAsk_NoActiveDevice_PushesThenAwaits` — `Push` einmal mit Intent; späteres attach+resolve löst dieselbe id.
- `TestAsk_HumanDeny_NilError` — Index -1/≥len → `(false,…,nil)` (bewusstes „nein" = nil-Fehler, ≠ Timeout; gate macht `ErrDenied`).
- `TestAsk_ApproveOnce_MapsToSessionGrant` (Index 0) / `ApproveAlways` (1, RecallAlways) / `ApproveSuggestion_ReturnsWidenedGrant` (2 → gewähltes Widening gemerkt).
- `TestAsk_CtxCanceled_ReturnsCtxErr` (≠ Timeout/Deny).
- `TestAsk_FirstAnswerWins` — **[synctest]**/-race — erste Antwort entscheidet, spätere Resolve gedroppt.
- `TestResolve_ForgesDenyCannotBecomeApprove` — unbekannte/abgeschlossene id → no-op, nie approve. **Falls HMAC-Token-Design landet:** `TestToken_TamperedOutcome_Rejected`, `TestToken_Replay_SingleUse`, `TestToken_Expired_Rejected` (Outcome im signierten Payload).

### Edge
- `TestAttach_RepresentsOpenApprovals_WithFrame` — re-present trägt denselben Frame.
- `TestSetActive_Foreground_Represents` / `Background_NotRouted`.
- `TestConclude_ClearsPromptOnAllSinks` / `RunsOnWithoutCancel` (deferred `Resolved` trotz ctx-Cancel).
- `TestAsk_FrameFromCtx_PropagatedToSink` / `TestIntentOf_TargetlessVsTargeted` / `TestNewID_UniqueHex12` / `TestDetach_RemovesSink` / `TestLogPusher_Push_NeverDelivers_ReturnsNil`.

## app/secret/store.go — Presence-only
- `TestStore_GuestViewExposesOnlyExists` (Guard-Test: `GuestView` hat nur `Exists`, keine wert-liefernde Methode).
- `TestStore_Exists_PresenceNotValue` / `ValueUnexported_HostOnly` (API-Surface-Guard: keine exportierte Methode gibt `[]byte`).
- Edge: `SetReplaces` / `ConcurrentSetExists_NoRace` / `Snapshot_IsCopy`.

## app/secret/vault.go — AES-256-GCM (tempdir, 32-Byte-Key)
### Kritisch
- `TestOpenVault_WrongKey_ErrWrongPassphrase` / `TamperedCiphertext_FailsClosed` (GCM-Tag) / `KeyWrongLength_Rejected`.
- `TestVault_RoundTrip` (Plaintext nie in on-disk-Bytes) / `TestSeal_UsesAAD_CrossVersionRejected` / `TestVault_SetPersistFailure_MemoryNotUpdated` (Disk+Memory divergieren nie).
### Edge
- `MissingFile_FreshEmptyPersisted` / `SetUnchangedValue_NoOp` (Nonce unverändert) / `UnknownVersion_Rejected` / `OversizeCiphertext_Rejected` / `BadMagic_Rejected` (`NOCTURNV`) / `TruncatedFrame_Rejected` / `Persist_AtomicAndPerms` (kein `.tmp`, 0600/0700) / `ConcurrentSet_NoRace` / `NonceUniquePerSeal`.

## app/secret/credential.go — Injector (fake `Resolver`)
### Kritisch (Secret nie im Gast)
- `TestInjectMatching_StampsBearerHostSide` — Header host-seitig gesetzt, Gast nannte Secret nie.
- `TestInjectMatching_HostMismatch_NoInjection` (destination-scoped).
- `TestHostMatches_EmptyOrStar_MatchesNothing` (fail-closed: kein „alle Hosts" durch Omission) / `WildcardSuffix` (Suffix selbst → false).
- `TestInjectMatching_MissingSource_FailClosed` / `ResolverError_FailClosed` (kein Teil-Header) / `OwnerScoping_NoCrossPluginBleed` / `StoreResolver_ManualCredMissing_ErrNotFound`.
### Edge
- `NilInjector_NoOp` / `RemoveBindingsFor_DropsBindingsAndPrivateResolver` / `SetResolver_OverridesWithDynamicSource` / `ResolverIOOutsideLock` (**-race**) / `ApplyTo_InitializesHeaderMap` / `MultipleMatches_AllStamped`.

## app/secret/master.go — scrypt→HKDF (`WithWorkFactor(10)` in Tests)
- `TestDeriveMaster_EmptyPassphraseOrSalt_Rejected` / `WorkspaceKey_DomainSeparated` / `Deterministic` / `Is32Bytes`.
- `TestCheckVerifier_WrongPassphrase_False`.
- Edge: `Verifier_RoundTripsThroughSaltFile` (missing→`fs.ErrNotExist`) / `ReadMasterSalt_InvalidFields_Rejected` / `WithWorkFactor_UsedOverDefault`.

## app/secret/leakscan.go — bidirektionaler Scanner
### Kritisch (Tier 1 exakt, bidirektional)
- `TestScanEgress_StoredSecretRaw_Blocked` (Fehlerstring enthält KEINE Secret-Bytes).
- `TestScanEgress_PercentEncodedSecret_Blocked` / `MultiLayerEncoded_Blocked` (≤`maxDecodePasses`).
- `TestRedactIngress_StoredSecret_Redacted` / `EncodedSecret_RedactedAtRawOffset`.
- `TestScanEgress_ShortValue_NotScanned` (<`minSecretLen`) / `TestNilScanner_Egress_NoError_Ingress_Passthrough`.
### Kritisch (Tier 2 Heuristik)
- `TestScanPatterns_BlockAction_Egress_Blocked` (Rule-id, nicht Wert) / `LowEntropy_NotMatched` / `KeywordPrefilter_Required` (Aho-Corasick).
- `TestRedactIngress_RedactAction_RedactedButEgressAllowed` / `ScanPatterns_WarnAction_LoggedNotRedactedNotBlocked`.
### Edge
- `ApplyRedactions_PartialOverlap_NoTailLeak` (Regression) / `NestedSpan_Skipped` / `DecodeVariants_BoundedPasses` / `LoadRules_BadRegexOrNoKeyword_SkippedNamed` / `Shannon_KnownValues` / `ScanExact_MultipleOccurrences_AllSpans` / `NewScanner_EmptyRuleset_Tier1StillWorks`.

## app/tools/net.go — `http_read`/`http_write` (`httptest`, Injector+Scanner, gate-ctx)
### Kritisch
- `TestNet_Read_RejectsNonSafeMethod` (nur GET/HEAD) / `Write_RejectsNonMutatingMethod`.
- `TestNet_Do_GateDeniesUnknownHost` — kein HTTP-Request (Handler nie getroffen) / `GateAllowsGrantedHost`.
- `TestNet_Egress_BlocksSmuggledSecret` — VOR Injection (eigener Bearer nie als Leak) / `InjectsBearerAtBorder_NotVisibleToCaller` / `Ingress_RedactsEchoedSecret`.
- `TestNet_InvalidURL_Rejected` (leer, `file://`, `ftp://`, kein Host) — kein Gate/Fetch.
### Edge
- `HostMatch_Semantics` / `Suggestions_ParentDomainWidening` / `Do_JSONEnvelopeShape` / `Do_BodyCappedAt64KiB` / `FirstHeaders_KeepsFirstValueOnly` / `Do_DefaultContentType` / `NilInjectorNilScanner_StillWorks` / `Do_CredentialInjectionError_Fails`.

## app/tools/file.go — Workspace-confined (Root=tempdir, gate-ctx)
### Kritisch (Pfad-Confinement)
- `TestFile_ResolvePath_DotDotEscape_Rejected` (VOR dem Gate) / `AbsoluteOutsideRoot_Rejected`.
- `TestFile_Read_ConfinedToRoot` (Symlink/`..` kann nicht raus).
- `TestFile_Write_GatedOnFileKind_TargetIsRelPath` (Deny → Datei nicht geschrieben).
- `TestFile_Read_Ungated` (read/list/stat/search trotz Deny-file-Policy) — read-vs-write-Split.
- `TestFile_Move_BothEndpointsConfined_GatedOnDest` / `Remove_GatedOnFileKind`.
### Edge
- `PathMatch_GlobDoesNotCrossSlash` / `DirSuggestions_ContainingDir` / `Search_SlashlessMatchesNameAnyDepth` / `Search_TruncationFlagged` (`truncated at 500`) / `Search_BadPattern_ClearError` / `Read_CappedAt1MiB` / `Stat_MissingPath_ExistsFalse` / `Write_CreatesParentsPerms` (0700/0600) / `List_DefaultRoot`.

## app/tools/dns.go — `dns_resolve`
- `TestDNS_GateDeniesUnknownHost` (kein Lookup) / `Egress_ScansQueriedName` / `Ingress_RedactsRecords` (TXT ist attacker-inbound).
- Edge: `NormalizeRecordType_DefaultA` / `RecordTypeNotAnAuthorityAxis` / `MissingHost_Rejected`.

## app/tools/ping.go — `ping`
- `TestPing_GateDeniesUnknownHost` (vor Socket) / `Egress_ScansHost` / `RawIP_NoDNS`.
- Edge: `MissingHost_Rejected` / `NoSocket_ClearError` (CI skip).

## app/tools/notify.go — `notify` (fake `Notifier`)
- `TestNotify_TargetIsHostOwnedUserChannel` (Modell liefert Text, nie Kanal; konstantes Target) / `Egress_BlocksSmuggledSecret` (Notifier NICHT gerufen) / `GateDeny_NotDelivered`.
- Edge: `MissingMessage_Rejected` / `NilScanner_Delivers` / `NotifierError_Wrapped`.

## app/tools/reminders.go — `remind`/`remind_list`/`remind_cancel` (fake Notifier; Timer **[synctest]**)
- `TestRemind_Egress_BlockedAtCreate` / `Fire_RescansAndDropsLeak` (**[synctest]** — Secret nach Erstellung → Fire droppt still) / `Fire_DeliversNotification` (**[synctest]**) / `GatedOnRemindKind`.
- Edge: `ParseWhen_InDuration_And_RFC3339` (neg/0 → Fehler) / `Persistence_RoundTrip` (0600 atomic) / `Restore_OverdueFiresPromptly` (**[synctest]**) / `Cancel_StopsTimer` (**[synctest]**) / `List_SortedSoonestFirst` / `Load_MalformedFile_EmptyStore`.

## app/tools/wake.go — Self-Continuation (fake `Sessions`; Timer **[synctest]**)
- `TestWake_ResumesSameChatByID` (**[synctest]** — `Open("c1")`+`Submit(note)`) / `NoChatID_Unavailable` / `PendingCap_Enforced` (`ErrTooManyPending`) / `DelayClamped` (**[synctest]** — min 1s/max 1h).
- Edge: `FiredAfterReap_ReopensChat` (id-basiert) / `NilSeam_SafeNoOp` / `UnresolvableChat_NoOp` / `Cancel_StopsAllPending` (**[synctest]**) / `MissingNote_Rejected` / `Pending_Count` / `WithChatID_RoundTrip`.

## app/tools/time.go — `time_now`
- `TestTimeNow_Ungated_JSONShape` (kein gate.Check; `{unix,iso,utc,timezone,offset_seconds}`; utc parst RFC3339).

## app/tools/tools.go — Registry (`Base`, `Compose`)
- `TestCompose_NoCodeRun_ReturnsCageUnchanged` (kein `code_run`) / `CodeRun_DispatchBoundToCage` (Dispatch-Set == cage, kann nicht weiten).
- Edge: `Base_TogglesByConfig` (nil Secrets/Scanner/Root/Notifier/Waker → exaktes Tool-Set) / `Base_AlwaysIncludesNetAndTime`.

## app/secret/oauth — host-managed OAuth2 (Loopback-PKCE + Refresh)
### Kritisch
- `TestAuthorize_LoopbackRedirectOnly` — `RedirectURL` == `http://127.0.0.1:<port>/callback` (nie 0.0.0.0/non-loopback).
- `TestAuthorize_StateCSRFGuard` — `?state=wrong` → 400 "bad state" + `state mismatch`, kein Token.
- `TestAuthorize_PKCEChallengeAndVerifier` — Prompt-URL trägt `code_challenge`+`S256`; Exchange sendet den passenden `code_verifier`.
- `TestAuthorize_HappyPath` (echter state+code → Token) / `OfflineConsentParams` (`access_type=offline`,`prompt=consent`).
- `TestSource_ValueReturnsAccessTokenBytes` — nur Access-Token-Bytes über die Grenze, nie der Refresh-Token.
- `TestSource_RefreshYieldsFreshTokenAndOnChange` — expired → neuer Token + `onChange` einmal; gleicher Token → kein Re-Fire (`last`-Detection).
- `TestSource_TokenNeverExposedToGuest` — nur `Value(ctx)→[]byte`; kein Getter liefert Refresh-Token/`*oauth2.Token` außer via `onChange`.
### Edge
- `TestAuthorize_Timeout` (**[synctest]**, `authTimeout` 3m → "authorization timed out") / `ProviderError` (`error=access_denied`) / `MissingCode` / `DuplicateCallbackNonBlocking` / `DefaultPromptPrintsURL` (kein Browser-exec).
- `TestRandomState_UniqueHighEntropy` / `TestSource_ValuePropagatesRefreshError` (kein onChange, `last` unverändert) / `TestNewSource_RefreshUsesBackgroundContext` (per-Request-ctx-Cancel bricht Refresh nicht ab).

## app/auth — Device-Pairing + Bearer (tempdir `devices.json`; Expiry **[synctest]**)
### Kritisch
- `TestVerify_UnknownBearerRejected` (`""`+random → false; macht `/ws` 401) / `PairedBearerAccepted` (1-char-mutiert → false, exakter constant-time-Compare).
- `TestBootstrap_OnlyWhenNoDevicePaired` — frisch → 6-stellig; nach 1 Device → `""`.
- `TestPair_FirstDeviceConsumesBootstrapCode` (2. Pair sameCode → `ErrPairing`, single-use) / `WrongCodeRejected` / `NoBootstrapArmed`.
- `TestJoin_NeverReturnsCode` — `Join` gibt nur joinId, NIE den Code; Code nur via `PendingJoins`.
- `TestPendingJoins_ExposesCodeForPairedRelay` / `TestConfirmJoin_RightCodeMintsBearer` (2. Confirm → `ErrPairing`, single-use).
- `TestConfirmJoin_WrongCodeIncrementsAndCaps` — `joinMaxTries` (5) falsche → Join gedroppt, danach auch korrekter Code → `ErrPairing` (Brute-Force-Cap) / `UnknownJoinID`.
- `TestPersistence_HashesOnlyNeverBearer` — Datei enthält nur `bearerHash`, nie den Bearer; Reload verifiziert Original / `TestSave_FilePermissions0600` (kein `.tmp`, atomic).
### Edge
- `TestOTPValid_Expired` (**[synctest]**) / `Join_ExpiresAfterTTL` (**[synctest]**, `joinTTL` 10m, prunt) / `PendingJoins_EmptyIsNonNilSlice` / `OTPCode_FormatAndRange` (6 Ziffern, zero-padded) / `Pair_EmptyNameDefaults` ("device").
- `TestRegisterPush_UnknownBearerNoOp` / `EmptyTokenClears` / `PushTargets_OnlyTokened` / `UpdateLastUsed_*` / `New_MissingFileIsEmpty` / `New_CorruptJSON` (Fehler, nicht still leer).

## app/push — APNs (`httptest`-Stub; `cfg.now`-Clock-Hook, kein synctest nötig)
### Kritisch
- `TestSend_DeliversToAllTokens` (N POST an `/3/device/<token>`) / `NilWhenAtLeastOneDelivered` (partial=success) / `ErrorWhenNoneDelivered` (`[]`→"apns: no device tokens").
- `TestSend_BadTokenInvokesOnBadTokenNonFatal` — 410/`BadDeviceToken`/`Unregistered` → `OnBadToken(token)`, `Send` trotzdem nil (Token gepruned).
- `TestPayload_NoSecretLeaked` — Body nur `aps`+`Data`; kein Bearer/JWT im Body (JWT im `authorization`-Header).
- `TestPayload_DataCannotShadowAPS` (`Data["aps"]` überschreibt nicht) / `CarriesDeepLinkData` (`chatId` überlebt → Deep-Link).
- `TestPush_HeadersAndAuth` (`authorization: bearer <jwt>`, `apns-topic`, `apns-push-type: alert`) / `ProviderToken_CachedUntilMaxAge` (`apnsTokenMaxAge` 50m Boundary) / `SignJWT_ES256Structure` (header/claims/64-Byte-sig verifiziert).
- `TestPusherFor_PlatformSelectsAPNs` (main.go) — nur `ios`+`""` bekommen Push, android exkludiert; `Data["type"]` `approval`/`notify`.
### Edge
- `TestAPNSFromEnv_UnsetKeyReturnsNilNil` (Push aus) / `ProductionFlag` (Host-Wahl) / `NewAPNS_KeyErrors` (nicht-PEM/nicht-PKCS8) / `LoadKey_InlineVsPath` / `NewAPNS_HostOverride`.
- `TestPush_UnknownStatusSurfacesReason` (429+reason, kein bad-token) / `Send_ContextCancelled` / `PushNotifier_NoTokensNoOp` / `NilSenderLogs`.

---

# 3 — `app/*` Orchestrierung + Persistenz + Wiring

## app/chat/manager.go — Lifecycle (fakeLLM wie manager_test; blockierende/scripted Variante)

### Kritisch
- `TestManager_Cancel_TurnScoped_SessionStays` — Cancel beendet Turn, `Open` gibt dieselbe Session, nächstes Submit läuft.
- `TestManager_Cancel_UnknownID_NoOp` / `TestManager_Open_UnknownID_LoadsFromStoreNotDuplicate` (eine Pump-Goroutine).
- `TestManager_Submit_RecordsInflightInput_BeforeTurnEnd` — `Inflight` sofort `Running`+`Input`.
- `TestManager_Submit_OpensIfNeeded`.
- `TestManager_ReopenMidTurn_HandedRunningTurn` — `Inflight` liefert Input/Answer/Thinking/Tools (offenes Tool `Running==true`).
- `TestManager_Inflight_ZeroWhenIdle` (clear-on-TurnEnd frame0) / `Inflight_UnknownOrReaped_Zero`.
- `TestManager_ReapIdle_UnloadsIdleSession` (**[synctest]**) / `ReapIdle_NeverReapsRunningTurn` (**[synctest]**).
- `TestManager_ReloadAfterReap_AppendsNewForestGroup_NeverRewrites` (**[synctest]**).
- `TestManager_CloseAll_Idempotent_AndWaitsForPumps` (doppeltes `close(m.stop)` kein Panic) / `CloseAll_StopsReaper` (**[synctest]**).
- `TestManager_Delete_StopsSessionAndRemovesTranscript` / `Start_MintsIDAndSubmits` (ValidID).

### Edge
- `TestManager_OnEvent_CapturedAtPumpStart` (read-once) / `observe_IgnoresNonZeroFrameForAnswerThinking` / `observe_ToolStart_AnyFrameCaptured` / `observe_ToolBeforeTurnStart_NilForestNoPanic`.
- `TestManager_Delete_UnknownID_DelegatesToStore` / `Cancel_ThenReap_RunningClearedAllowsReap` (**[synctest]**) / `TestNewID_ValidHexAndUnique` / `TestErrText_NilAndNonNil`.

## app/chat/manager.go — forest-Accumulator (Unit, ohne Manager)
- `TestForest_StartOrder_ParentsBeforeChildren` / `Start_Idempotent` / `End_FillsResult_MissingStartIgnored` / `Inflight_RunningFlagReflectsEnded` / `Snapshot_EmptyForest`.

## app/chat/manager.go — Inflight/Forest-Semantik (via Manager+observe)
- `TestManager_EmptyGroupAppendedForNoToolTurn` (Index-Alignment) / `ForestGroupsAlignedToTurns` (N Turns → N Gruppen) / `NestedAndSubAgentCallsCaptured` (korrekte Parent-Links).

## app/chat/store.go — File-backed Store
### Kritisch
- `TestValidID_Table` (akzeptiert lowercase-hex; lehnt "", uppercase, "g", "ab-cd", "a/b", "..", "a.json", spaces ab).
- `TestStore_Save_FirstSave_StampsMetaFromMessages` / `BumpsTurnsAndUpdated_PreservesCreated` / `DumbSerializer_PersistsGivenMetaVerbatim` / `PreservesExistingTools` / `WithSource_StampsSource`.
- `TestStore_MarkRead_AdvancesReadToUpdated` (feuert onSave) / `NoOpWhenAlreadyRead_NoWriteNoBroadcast` (Echo-Loop-Break) / `UnknownChat_NoOpNilErr`.
- `TestStore_Write_ThenRename_Atomic_0600` (kein `.tmp`) / `LoadTools_NilWhenNone` / `Load_MissingChat_NilNoError`.
- `TestStore_read_RejectsInvalidID_BeforeFS` (jeder Pfad: Load/LoadTools/Save/MarkRead/Metas/Delete) / `Delete_MissingNotError_InvalidRejected`.
- `TestStore_Metas_SortedByUpdatedDescending_NeverNil`.
### Edge
- `NameFrom_FirstUserLine_TrimmedToLimit` / `PreviewFrom_LastUserOrAssistantFirstLine` / `NameFrom_MultibyteRuneBoundary` / `Rename_SetsName_UnknownNoOp` / `OnSave_GuardedConcurrent` (**-race**) / `AppendTools_DoesNotBumpMetaOrFire` / `Corrupt_UnreadableFile_Metas_SkipsEntry`.

## app/serve/chat.go — Wire-Handler + Event-Mapping
### Kritisch
- `TestChatSubmit_InvalidID_SendsBadChatID_NoSubmit` (client-minted id validiert vor Nutzung) / `UnknownID_StartsChat` / `KnownID_Appends` / `UnknownWorkspace_ReturnsSilently`.
- `TestChatOpen_Snapshot_CarriesMessagesToolsInflight` (running → Inflight; idle → `[]`+nil Inflight) / `NilMessagesTools_WireEmptyArrays`.
- `TestChatEvent_TagsChatID_AllVariants` (ToolEnd trägt Result/Err/DurationMs, ToolStart nicht) / `UnknownEvent_SkippedOkFalse`.
- `TestChatCancel_DelegatesToManager` / `ChatMarkRead_DelegatesToWorkspace`.
### Edge
- `ChatDispatch_BadJSON_PerCommand` / `Chat_UnknownAction_Error` / `ChatEvent_ToolEnd_ErrTextFromError` / `ChatTurnEnd_TokensAndErr`.

## app/serve/approval.go
- `TestApprovalResolve_ForwardsChoiceToBroker` (fake broker; -1 denies) / `BadJSON_Error`.
- `TestConn_Approval_SendsRequestWithFrame` (Frame durchgereicht) / `Resolved_SendsResolved`.
- Edge: `Approval_UnknownAction_Error` / `ApprovalRequest_Frame0_OmittedNotToolScoped`.

## app/serve/serve.go + conn.go
- `TestServe_WiresOnChatUpdateAndOnEvent_Broadcast` (turn-end→ChatActivity; live-event→chat.*) / `CloseAllDeferredPerWorkspace` / `Conn_Stateless_NoSessionCleanupOnDisconnect` (Turn überlebt Disconnect) / `Hub_Broadcast_NonBlocking_DropsFullBuffer` (cap 64).
- Edge: `Conn_Send_RespectsCtxCancel` / `Dispatch_UnknownDomain_Error` / `Cors_OptionsPreflight204`.

## app/workspace/workspace.go
### Kritisch
- `TestWorkspace_Open_BuildsIsolatedStack` (chats/agent-runs/grants.json; user+agent Stores getrennt) / `Open_TwoWorkspaces_Isolated`.
- `TestBuildTools_IncludesAgentToolPerDeclaredAgent` (je Agent ein AgentTool, caged `base.Select(a.Matches)`).
- `TestResolvePersona_PersonaMdOverride_LayeredElseDefault` / `ReadError_WarnsAndDefaults` (Identität nicht still getauscht).
- `TestWorkspace_MarkRead_BothStores_WrongOneNoOps` / `OnChatUpdate_WiresBothStores`.
### Edge
- `InstallPlugins_NameCollision_Refused` / `BindsCredentialsUnderOwner` / `OpenAll_AlwaysIncludesMain` / `Policy_NetAndFileAsk_ElseAllowed`.

## app/workspace/agents.go — FireAgent
- `TestFireAgent_UnknownAgent_Error` / `RunsOwnSubscribeLoop_PersistsToAgentStore` (SourceAgent, nicht user) / `Unattended_NilApprover_FailsClosedOnAsk` / `CtxCancel_ClosesSessionAndReturnsPartialAnswer` (**-race**, `<-done` vor answer-Read).
- Edge: `CageIsAgentFilteredToolset`.

## app/workspace/grantstore.go
- `TestGrantStore_RecallAlways_PersistsAcrossReopen` / `RecallSession_NotPersisted`.
- Edge: `MissingFile_EmptySeed` / `CorruptFile_Error` (fail-closed).

## app/agent — Deklaration + Scheduler
### Kritisch
- `TestAgent_Matches_ExactAndGroupSeparators` (matcht http/http_read/http.read/http/get; nicht https_x/httpfoo) / `EmptyTools_MatchesNothing`.
- `TestDiscover_ReadsAgentMd_PerSubdir` / `MissingDir_EmptySet` / `TestLoadAgent_MissingName_Error`.
- `TestCronMatches_Wildcards_Ranges_Steps_Lists` (table; DOW Sunday==0; 4-Felder→false).
- `TestScheduler_Tick_FiresMatchingAgentsInGoroutine` (skip `When==""`; slow fire blockt nicht).
### Edge
- `SplitFrontmatter_NoLeadingDashes_AllBody` / `UnterminatedHead_AllHead` / `PartMatch_InvalidStepOrBounds_False` / `Scheduler_Start_AlignsToMinute_StopsOnCtxCancel` (**[synctest]**) / `Set_All_SortedByName` / `Set_Get` / `Discover_NonDirEntries_Skipped`.

## app/plugin (high-level)
- `TestManifest_Validate_RejectsMalformed` (table: bad name/version/tools/dup/params/credential/oauth-non-https) / `allows_UsesListAndStar` (leer=nichts) / `Load_ManifestPlusExactlyOneArtifact` (js XOR wasm; DisallowUnknownFields) / `Plugin_DispatchCall_RefusesCodeRunAndOwnTools_UnknownToolAbsent` / `Plugin_Run_ScopesCredentialOwner` (`secret.WithOwner("plugin:<name>")`).
- Edge: `Owner_Prefix` / `RawArgs_EmptyOrInvalid_DefaultsEmptyObject` / `Plugin_Close_NoOpForJS_ClosesWasmEngine`.

## app/script (high-level)
- `TestRunner_Dispatch_RefusesCodeRunReentry` / `RoutesThroughSharedToolset` (`nocturn.call`→`tools.Call`, empty args→"{}") / `Run_ReturnsStdout` (Trap/Timeout/nonzero→Fehler+stderr) / `Tool_ParsesSourceArg`.

---

# Querschnitt

- **[synctest]-Fälle**: alle Deadline-Feuer / Pause-Banking / Approval-Wall-Clock / reap-idleTTL / Reminder-/Wake-Timer / Scheduler-Minute-Alignment / sandbox-Deadline+Pausable-Budget / hitl-2-min-Timeout.
- **Kanal-koordiniert (kein sleep)**: `ParallelToolExecution` (Barrier), `Cancel_MidTurn`, `Submit_Serialized`, `FirstAnswerWins`, blockierende fakeApprover/fakeTool.
- **-race-Fälle**: Diagnostics-Feeders, Store.OnSave-concurrent, Injector-ResolverIOOutsideLock, sandbox-ConcurrentRuns, FireAgent-CtxCancel, MemGrants-concurrent.
- **Fakes**: fakeLLM (+blockierend), fakeStore, fakeTool/NewTool, captureSink, fakeApprover+fakePolicy+MemGrants, fake Notifier/Pusher/Resolver/Sessions, httptest.Server (net+openai-SSE), WAT/wasm-Gast-Korpus.
- **Reihenfolge-Empfehlung fürs Implementieren**: (1) agentkit session/guards/tool/gate — der Kern, den alles nutzt; (2) app/chat store+manager — Persistenz+Lifecycle; (3) app/tools + secret — Effekt+Leak; (4) app/sandbox + hitl; (5) serve/workspace/agent Wiring; (6) plugin/script.
