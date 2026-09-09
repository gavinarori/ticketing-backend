-- Leave the login roles in place: dropping them would break any
-- running API/worker still connected as app_user/worker_user, and
-- REVOKE is enough to undo the grants this migration added.
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM app_user, worker_user;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM app_user, worker_user;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM app_user, worker_user;
REVOKE USAGE ON SCHEMA public FROM app_user, worker_user;
