-- Local development only. Production roles are created by the bootstrap
-- runbook (docs/runbooks/database-bootstrap.md) with generated passwords.
--
-- hedvi_owner  owns the schema and runs migrations.
-- hedvi_api    is used by the public/console API; row level security applies.
-- hedvi_worker is used by background workers; policies grant explicit access.
CREATE ROLE hedvi_owner LOGIN PASSWORD 'hedvi_owner_dev' CREATEDB CREATEROLE;
CREATE ROLE hedvi_api LOGIN PASSWORD 'hedvi_api_dev';
CREATE ROLE hedvi_worker LOGIN PASSWORD 'hedvi_worker_dev';
GRANT hedvi_api, hedvi_worker TO hedvi_owner WITH ADMIN OPTION;
CREATE DATABASE hedvi OWNER hedvi_owner;
\connect hedvi
-- Local AWS mock (queues and captured SES output). Never created in AWS envs.
CREATE SCHEMA localmock AUTHORIZATION hedvi_worker;
