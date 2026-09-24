-- Development rollback only: drops every table and its data.
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM wager_app;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM wager_app;
REVOKE USAGE ON SCHEMA public FROM wager_app;

DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS inbox_messages;
DROP TABLE IF EXISTS wallet_ledger_entries;
DROP TABLE IF EXISTS wager_transactions;
DROP TABLE IF EXISTS wallets;

DROP FUNCTION IF EXISTS outbox_events_guard();
DROP FUNCTION IF EXISTS wallet_balance_guard();
DROP FUNCTION IF EXISTS ledger_entry_guard();
DROP FUNCTION IF EXISTS wager_transactions_guard();
DROP FUNCTION IF EXISTS reject_mutation();
