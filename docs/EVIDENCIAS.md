# Evidências

> Gerado por `make evidence` a partir de `go test -json`. Não editar à mão.

Commit `707302b` · go1.27.1 · 2026-09-24T22:14Z

| Req. | Requisito | Situação | Testes |
|---|---|---|---|
| 5.1 | Dinheiro sem ponto flutuante | ✅ | TestNoFloatInMoneyPaths (0.44s)<br>TestParseAcceptsCanonicalAmounts (0s)<br>TestParseRejectsNonCanonicalInput (0s) |
| 5.2 | Idempotência persistente após reinício | ✅ | TestRestartPreservesIdempotencyAndPendingOperations (0.72s)<br>TestBetDebitsAndReplayReturnsOriginalBalance (0.13s) |
| 5.3 | Invariantes financeiras no banco | ✅ | TestBalanceCannotGoNegative (0.03s)<br>TestBalanceChangeWithoutLedgerFailsAtCommit (0.03s)<br>TestLedgerDirectionMustMatchKind (0.03s)<br>TestLedgerChainMustBeContinuous (0.04s) |
| 5.4 | Eventos só após o commit | ✅ | TestOpenWalletCommitsOpeningLedgerAndEvents (0.12s)<br>TestRelayCrashAfterPublishIsRepublishedWithTheSameEventID (2.52s) |
| 5.5 | Ledger append-only | ✅ | TestRuntimeRoleCannotRewriteLedger (0.04s)<br>TestLedgerIsAppendOnlyEvenForOwner (0.05s) |
| 5.6 | Carteiras independentes em paralelo, sem lock global | ✅ | TestDistinctWalletsProgressWhileOneIsLocked (2.15s)<br>TestDistinctWalletsAcrossProcesses (0.34s) |
| 5.7 | Sem lost update | ✅ | TestTwoConcurrentBetsOnSameWallet (0.24s)<br>TestVersionMustAdvanceByExactlyOne (0.03s) |
| 7 | Reversão única e coerência REFUND/ROLLBACK | ✅ | TestRefundAndRollbackShareOneReversalSlot (0.29s)<br>TestConcurrentReversalsOnlyOneSucceeds (0.16s)<br>TestRollbackOfWinWithoutFundsIsItsOwnRejection (0.15s) |
| 7.ref | Referência indisponível: pendência, retomada e expiração | ✅ | TestPendingRefundIsResolvedAfterTheBet (0.15s)<br>TestPendingExpiresAsReferenceNotFound (0.46s)<br>TestResolverCrashAfterClaimIsResumedByAnotherInstance (2.38s) |
| 8 | 80+80 sobre 100 com três processos | ✅ | TestTwoConcurrentBetsAcrossProcesses (0.07s) |
| 13.1 | Mesma aposta 50 vezes em paralelo | ✅ | TestFiftyIdenticalBetsDebitOnce (0.45s)<br>TestFiftyIdenticalBetsAcrossProcesses (0.15s)<br>TestFiftyMixedHTTPAndSQSDuplicates (1.39s) |
| 13.5 | Queda do consumidor após commit e antes da remoção | ✅ | TestConsumerCrashAfterCommitIsRedeliveredWithoutDoubleDebit (31.81s)<br>TestConsumerAppliesAMessageOnceEvenWhenRedelivered (3.31s)<br>TestRedeliveredMessageIsAnsweredFromTheInbox (0.14s) |
| 13.6 | Dois publishers disputando a outbox | ✅ | TestTwoRelaysPublishEachWalletInCommitOrder (0.32s)<br>TestExpiredClaimIsRepublishedWithTheSameEventID (5.59s)<br>TestRelayCrashAfterPublishIsRepublishedWithTheSameEventID (2.52s) |
| 13.http-sqs | Mesma operação por HTTP e SQS | ✅ | TestHTTPThenSQSForTheSameOperationDebitsOnce (0.26s)<br>TestSameOperationViaHTTPAndSQSDebitsOnce (0.52s)<br>TestFiftyMixedHTTPAndSQSDuplicates (1.39s)<br>TestNewMessageForAnAppliedOperationIsAReplay (0.12s) |
| 2 | Autenticação real e isolamento entre provedores | ✅ | TestKeycloakTokensMapToPrincipals (0.05s)<br>TestKeycloakExpiredTokenIsRejected (3.05s)<br>TestTransactionReadsRespectProviderScope (0.13s)<br>TestAuthorizationByRoute (0s)<br>TestAuthorizationRefusalsHaveNoFinancialEffect (5.13s)<br>TestExternalReadRefusesAnotherProvidersPath (0s)<br>TestGetTransactionByIDScopesToProvider (0s) |
| 10 | DLQ para mensagens inválidas e remetente não autorizado | ✅ | TestConsumerDeadLettersUnauthorizedAndInvalidMessages (0.19s)<br>TestTransientFailureAtMaxReceivesGoesToDLQAsRetriesExhausted (0s)<br>TestSenderBoundToAnotherProviderIsRejected (0s) |
| 4 | Lifecycle Fx e shutdown | ✅ | TestFxLifecycleStartsServesAndStopsCleanly (0.13s)<br>TestSIGTERMDrainsAndExitsCleanly (2.65s) |
| 9.rec | Reconciliação | ✅ | TestReconciliationMatchesTheLedger (0.13s)<br>TestReconcileReportsDifferenceAsStoredMinusCalculated (0s) |
| 13.8 | Reinício preserva idempotência, pendências e consistência; outra instância retoma o que ficou em PENDING | ✅ | TestRestartPreservesIdempotencyAndPendingOperations (0.72s)<br>TestResolverCrashAfterClaimIsResumedByAnotherInstance (2.38s) |
| 3.transitoria | Indisponibilidade temporária do PostgreSQL ou do SQS sem efeito duplicado | ✅ | TestClassify (0s)<br>TestTransientFailureBacksOffAndReleasesTheRestOfItsGroup (0s)<br>TestTransientFailureDuringShutdownIsReleasedWithoutCanceledInTheChain (0s)<br>TestPublishTreatsConfigurationErrorsAsTransient (0s) |
| 12 | Métricas sem identificadores de alta cardinalidade e health checks | ✅ | TestEveryMetricIsExposedWithItsLabelsOnly (0.01s)<br>TestAdminAndReadinessDelay (0s) |
| 13.unit.zero | Política de valor zero por tipo e abertura interna | ✅ | TestNewExternalZeroPolicy (0s)<br>TestZeroValueIsRejected (0s)<br>TestOpenWalletWithZeroBalanceHasNoFinancialRecords (0.1s) |
| sec.iam | Privilégio mínimo na AWS por papel de processo (ADR 0016) | ✅ | TestIAMPolicyActionsMatchCode (0s)<br>TestIAMPolicyHasNoWildcards (0s)<br>TestIAMPolicyFilesCoverEveryRole (0s) |
