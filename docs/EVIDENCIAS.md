# Evidências

> Gerado por `make evidence` a partir de `go test -json`. Não editar à mão.

Commit `82a8616` · go1.27.1 · 2026-09-24T22:01Z

| Req. | Requisito | Situação | Testes |
|---|---|---|---|
| 5.1 | Dinheiro sem ponto flutuante | ✅ | TestNoFloatInMoneyPaths (0.46s)<br>TestParseAcceptsCanonicalAmounts (0s)<br>TestParseRejectsNonCanonicalInput (0s) |
| 5.2 | Idempotência persistente após reinício | ✅ | TestRestartPreservesIdempotencyAndPendingOperations (0.75s)<br>TestBetDebitsAndReplayReturnsOriginalBalance (0.17s) |
| 5.3 | Invariantes financeiras no banco | ✅ | TestBalanceCannotGoNegative (0.03s)<br>TestBalanceChangeWithoutLedgerFailsAtCommit (0.03s)<br>TestLedgerDirectionMustMatchKind (0.04s)<br>TestLedgerChainMustBeContinuous (0.03s) |
| 5.4 | Eventos só após o commit | ✅ | TestOpenWalletCommitsOpeningLedgerAndEvents (0.12s)<br>TestRelayCrashAfterPublishIsRepublishedWithTheSameEventID (2.52s) |
| 5.5 | Ledger append-only | ✅ | TestRuntimeRoleCannotRewriteLedger (0.04s)<br>TestLedgerIsAppendOnlyEvenForOwner (0.06s) |
| 5.6 | Carteiras independentes em paralelo, sem lock global | ✅ | TestDistinctWalletsProgressWhileOneIsLocked (2.15s)<br>TestDistinctWalletsAcrossProcesses (0.34s) |
| 5.7 | Sem lost update | ✅ | TestTwoConcurrentBetsOnSameWallet (0.16s)<br>TestVersionMustAdvanceByExactlyOne (0.03s) |
| 7 | Reversão única e coerência REFUND/ROLLBACK | ✅ | TestRefundAndRollbackShareOneReversalSlot (0.31s)<br>TestConcurrentReversalsOnlyOneSucceeds (0.16s)<br>TestRollbackOfWinWithoutFundsIsItsOwnRejection (0.14s) |
| 7.ref | Referência indisponível: pendência, retomada e expiração | ✅ | TestPendingRefundIsResolvedAfterTheBet (0.15s)<br>TestPendingExpiresAsReferenceNotFound (0.47s)<br>TestResolverCrashAfterClaimIsResumedByAnotherInstance (2.6s) |
| 8 | 80+80 sobre 100 com três processos | ✅ | TestTwoConcurrentBetsAcrossProcesses (0.12s) |
| 13.1 | Mesma aposta 50 vezes em paralelo | ✅ | TestFiftyIdenticalBetsDebitOnce (0.4s)<br>TestFiftyIdenticalBetsAcrossProcesses (0.16s)<br>TestFiftyMixedHTTPAndSQSDuplicates (0.34s) |
| 13.5 | Queda do consumidor após commit e antes da remoção | ✅ | TestConsumerCrashAfterCommitIsRedeliveredWithoutDoubleDebit (31.84s)<br>TestConsumerAppliesAMessageOnceEvenWhenRedelivered (3.33s)<br>TestRedeliveredMessageIsAnsweredFromTheInbox (0.12s) |
| 13.6 | Dois publishers disputando a outbox | ✅ | TestTwoRelaysPublishEachWalletInCommitOrder (0.42s)<br>TestExpiredClaimIsRepublishedWithTheSameEventID (5.66s)<br>TestRelayCrashAfterPublishIsRepublishedWithTheSameEventID (2.52s) |
| 13.http-sqs | Mesma operação por HTTP e SQS | ✅ | TestHTTPThenSQSForTheSameOperationDebitsOnce (0.25s)<br>TestSameOperationViaHTTPAndSQSDebitsOnce (0.56s)<br>TestFiftyMixedHTTPAndSQSDuplicates (0.34s)<br>TestNewMessageForAnAppliedOperationIsAReplay (0.13s) |
| 2 | Autenticação real e isolamento entre provedores | ✅ | TestKeycloakTokensMapToPrincipals (0.06s)<br>TestKeycloakExpiredTokenIsRejected (3.05s)<br>TestTransactionReadsRespectProviderScope (0.12s)<br>TestAuthorizationByRoute (0s)<br>TestAuthorizationRefusalsHaveNoFinancialEffect (5.12s)<br>TestExternalReadRefusesAnotherProvidersPath (0s)<br>TestGetTransactionByIDScopesToProvider (0s) |
| 10 | DLQ para mensagens inválidas e remetente não autorizado | ✅ | TestConsumerDeadLettersUnauthorizedAndInvalidMessages (1.19s)<br>TestTransientFailureAtMaxReceivesGoesToDLQAsRetriesExhausted (0s)<br>TestSenderBoundToAnotherProviderIsRejected (0s) |
| 4 | Lifecycle Fx e shutdown | ✅ | TestFxLifecycleStartsServesAndStopsCleanly (0.12s)<br>TestSIGTERMDrainsAndExitsCleanly (0.74s) |
| 9.rec | Reconciliação | ✅ | TestReconciliationMatchesTheLedger (0.13s)<br>TestReconcileReportsDifferenceAsStoredMinusCalculated (0s) |
| 13.8 | Reinício preserva idempotência, pendências e consistência; outra instância retoma o que ficou em PENDING | ✅ | TestRestartPreservesIdempotencyAndPendingOperations (0.75s)<br>TestResolverCrashAfterClaimIsResumedByAnotherInstance (2.6s) |
| 3.transitoria | Indisponibilidade temporária do PostgreSQL ou do SQS sem efeito duplicado | ✅ | TestClassify (0s)<br>TestTransientFailureBacksOffAndReleasesTheRestOfItsGroup (0s)<br>TestTransientFailureDuringShutdownIsReleasedWithoutCanceledInTheChain (0s)<br>TestPublishTreatsConfigurationErrorsAsTransient (0s) |
| 12 | Métricas sem identificadores de alta cardinalidade e health checks | ✅ | TestEveryMetricIsExposedWithItsLabelsOnly (0.01s)<br>TestAdminAndReadinessDelay (0s) |
| 13.unit.zero | Política de valor zero por tipo e abertura interna | ✅ | TestNewExternalZeroPolicy (0s)<br>TestZeroValueIsRejected (0s)<br>TestOpenWalletWithZeroBalanceHasNoFinancialRecords (0.12s) |
| sec.iam | Privilégio mínimo na AWS por papel de processo (ADR 0016) | ✅ | TestIAMPolicyActionsMatchCode (0s)<br>TestIAMPolicyHasNoWildcards (0s)<br>TestIAMPolicyFilesCoverEveryRole (0s) |
