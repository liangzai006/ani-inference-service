#!/usr/bin/env bash
set -Eeuo pipefail

# Creates and migrates only the independent Inference database. Credentials
# are supplied by the caller; they are never stored in this repository.
: "${PGUSER:=recycling_user}"
: "${PGPASSWORD:=recycling_pass}"
: "${PGHOST:=127.0.0.1}"
: "${PGPORT:=5432}"
: "${PGDATABASE:=ani_inference}"

command -v psql >/dev/null 2>&1 || { echo "psql is required" >&2; exit 1; }
export PGPASSWORD

psql --host="$PGHOST" --port="$PGPORT" --username="$PGUSER" --dbname=postgres \
  --set=ON_ERROR_STOP=1 \
  --command="SELECT 'CREATE DATABASE ' || quote_ident('$PGDATABASE') WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '$PGDATABASE')" \
  --tuples-only --no-align | while IFS= read -r statement; do
    if [[ -n "$statement" ]]; then
      psql --host="$PGHOST" --port="$PGPORT" --username="$PGUSER" --dbname=postgres --set=ON_ERROR_STOP=1 --command="$statement"
    fi
  done

for migration in migrations/*.sql; do
  psql --host="$PGHOST" --port="$PGPORT" --username="$PGUSER" --dbname="$PGDATABASE" \
    --set=ON_ERROR_STOP=1 --file="$migration"
done
