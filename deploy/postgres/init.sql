-- Development-only credentials. The application never connects as superuser.
CREATE ROLE wager_migrator LOGIN PASSWORD 'migrator-dev-only';
CREATE ROLE wager_app LOGIN PASSWORD 'app-dev-only';
CREATE DATABASE wagering OWNER wager_migrator;
