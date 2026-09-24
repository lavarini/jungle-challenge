# Evidências

> Gerado por `make evidence` a partir de `go test -json`. Não editar à mão.

Commit `353ce83` · go1.27.1 · 2026-09-24T22:49Z

| Req. | Requisito | Situação | Testes |
|---|---|---|---|
| 5.1 | Dinheiro sem ponto flutuante | ✅ | TestNoFloatInMoneyPaths (0.52s)<br>TestParseAcceptsCanonicalAmounts (0s)<br>TestParseRejectsNonCanonicalInput (0s) |
| 5.2 | Idempotência persistente após reinício | ✅ | TestRestartPreservesIdempotencyAndPendingOperations (0.73s)<br>TestBetDebitsAndReplayReturnsOriginalBalance (0.13s) |
| 5.3 | Invariantes financeiras no banco | ✅ | TestBalanceCannotGoNegative (0.03s)<br>TestBalanceChangeWithoutLedgerFailsAtCommit (0.03s)<br>TestLedgerDirectionMustMatchKind (0.04s)<br>TestLedgerChainMustBeContinuous (0.03s) |
| 5.4 | Eventos só após o commit | ✅ | TestOpenWalletCommitsOpeningLedgerAndEvents (0.11s)<br>TestRelayCrashAfterPublishIsRepublishedWithTheSameEventID (2.76s) |
| 5.5 | Ledger append-only | ✅ | TestRuntimeRoleCannotRewriteLedger (0.03s)<br>TestLedgerIsAppendOnlyEvenForOwner (0.05s) |
| 5.6 | Carteiras independentes em paralelo, sem lock global | ✅ | TestDistinctWalletsProgressWhileOneIsLocked (2.16s)<br>TestDistinctWalletsAcrossProcesses (0.33s) |
| 5.7 | Sem lost update | ✅ | TestTwoConcurrentBetsOnSameWallet (0.15s)<br>TestVersionMustAdvanceByExactlyOne (0.03s) |
| 7 | Reversão única e coerência REFUND/ROLLBACK | ✅ | TestRefundAndRollbackShareOneReversalSlot (0.29s)<br>TestConcurrentReversalsOnlyOneSucceeds (0.16s)<br>TestRollbackOfWinWithoutFundsIsItsOwnRejection (0.14s) |
| 7.ref | Referência indisponível: pendência, retomada e expiração | ✅ | TestPendingRefundIsResolvedAfterTheBet (0.15s)<br>TestPendingExpiresAsReferenceNotFound (0.47s)<br>TestResolverCrashAfterClaimIsResumedByAnotherInstance (2.36s) |
| 8 | 80+80 sobre 100 com três processos | ✅ | TestTwoConcurrentBetsAcrossProcesses (0.07s) |
| 13.1 | Mesma aposta 50 vezes em paralelo | ✅ | TestFiftyIdenticalBetsDebitOnce (0.37s)<br>TestFiftyIdenticalBetsAcrossProcesses (0.14s)<br>TestFiftyMixedHTTPAndSQSDuplicates (1.54s) |
| 13.5 | Queda do consumidor após commit e antes da remoção | ✅ | TestConsumerCrashAfterCommitIsRedeliveredWithoutDoubleDebit (31.99s)<br>TestConsumerAppliesAMessageOnceEvenWhenRedelivered (3.31s)<br>TestRedeliveredMessageIsAnsweredFromTheInbox (0.14s) |
| 13.6 | Dois publishers disputando a outbox | ✅ | TestTwoRelaysPublishEachWalletInCommitOrder (0.39s)<br>TestExpiredClaimIsRepublishedWithTheSameEventID (5.57s)<br>TestRelayCrashAfterPublishIsRepublishedWithTheSameEventID (2.76s) |
| 13.http-sqs | Mesma operação por HTTP e SQS | ✅ | TestHTTPThenSQSForTheSameOperationDebitsOnce (0.26s)<br>TestSameOperationViaHTTPAndSQSDebitsOnce (0.49s)<br>TestFiftyMixedHTTPAndSQSDuplicates (1.54s)<br>TestNewMessageForAnAppliedOperationIsAReplay (0.12s) |
| 2 | Autenticação real e isolamento entre provedores | ✅ | TestKeycloakTokensMapToPrincipals (0.05s)<br>TestKeycloakExpiredTokenIsRejected (3.05s)<br>TestTransactionReadsRespectProviderScope (0.12s)<br>TestAuthorizationByRoute (0s)<br>TestAuthorizationRefusalsHaveNoFinancialEffect (5.12s)<br>TestExternalReadRefusesAnotherProvidersPath (0s)<br>TestGetTransactionByIDScopesToProvider (0s) |
| 10 | DLQ para mensagens inválidas e remetente não autorizado | ✅ | TestConsumerDeadLettersUnauthorizedAndInvalidMessages (0.19s)<br>TestTransientFailureAtMaxReceivesGoesToDLQAsRetriesExhausted (0s)<br>TestSenderBoundToAnotherProviderIsRejected (0s) |
| 4 | Lifecycle Fx e shutdown | ✅ | TestFxLifecycleStartsServesAndStopsCleanly (0.13s)<br>TestSIGTERMDrainsAndExitsCleanly (2.72s) |
| 9.rec | Reconciliação | ✅ | TestReconciliationMatchesTheLedger (0.12s)<br>TestReconcileReportsDifferenceAsStoredMinusCalculated (0s) |
| 13.8 | Reinício preserva idempotência, pendências e consistência; outra instância retoma o que ficou em PENDING | ✅ | TestRestartPreservesIdempotencyAndPendingOperations (0.73s)<br>TestResolverCrashAfterClaimIsResumedByAnotherInstance (2.36s) |
| 3.transitoria | Falha transitória do PostgreSQL ou do SQS classificada como retentável, sem DLQ nem efeito duplicado (classificação e consumidor; não derruba a dependência) | ✅ | TestClassify (0s)<br>TestTransientFailureBacksOffAndReleasesTheRestOfItsGroup (0s)<br>TestTransientFailureDuringShutdownIsReleasedWithoutCanceledInTheChain (0s)<br>TestPublishTreatsConfigurationErrorsAsTransient (0s) |
| 12 | Métricas sem identificadores de alta cardinalidade e health checks | ✅ | TestEveryMetricIsExposedWithItsLabelsOnly (0.01s)<br>TestHealth (0s)<br>TestFxLifecycleStartsServesAndStopsCleanly (0.13s) |
| 13.unit.zero | Política de valor zero por tipo e abertura interna | ✅ | TestNewExternalZeroPolicy (0s)<br>TestZeroValueIsRejected (0s)<br>TestOpenWalletWithZeroBalanceHasNoFinancialRecords (0.1s) |
| sec.iam | Privilégio mínimo na AWS por papel de processo (ADR 0016): políticas conferidas contra a tabela de ações por papel, sem curinga | ✅ | TestIAMPolicyActionsMatchCode (0s)<br>TestIAMPolicyHasNoWildcards (0s)<br>TestIAMPolicyFilesCoverEveryRole (0s) |
