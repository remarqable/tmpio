-- Run once as doadmin against defaultdb, then the second block against the tmp database.
-- Passwords: replace the two placeholders (scripts/deploy.sh does this automatically).
--
-- psql "<doadmin url to defaultdb>" -v ON_ERROR_STOP=1 -v owner_pw='...' -v user_pw='...' -f scripts/deploy/prod-db.sql
CREATE ROLE app_owner LOGIN PASSWORD :'owner_pw' NOBYPASSRLS;
CREATE ROLE app_user  LOGIN PASSWORD :'user_pw'  NOBYPASSRLS;
GRANT app_owner TO doadmin;
CREATE DATABASE tmp OWNER app_owner;
