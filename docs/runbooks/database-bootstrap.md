# Database bootstrap (staging/production)

Create three login roles with generated passwords stored in Secrets Manager:

```sql
CREATE ROLE hedvi_owner LOGIN PASSWORD '…' CREATEROLE;   -- migrations only
CREATE ROLE hedvi_api LOGIN PASSWORD '…';                -- API, row level security applies
CREATE ROLE hedvi_worker LOGIN PASSWORD '…';             -- workers, explicit policies
GRANT hedvi_api, hedvi_worker TO hedvi_owner WITH ADMIN OPTION;
CREATE DATABASE hedvi OWNER hedvi_owner;
```

Do **not** create the `localmock` schema outside local development. Run `HEDVI_DATABASE_URL_MIGRATE=… hedvi migrate` from the deploy pipeline before rolling out new code. Migrations are forward-only and take an advisory lock.
