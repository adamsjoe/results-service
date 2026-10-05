-- Fills the database with 20,000 runs and 1,000,000 results, so load tests
-- can show how queries behave on a large table rather than an empty one.
--   make seed
INSERT INTO runs (suite, branch, commit_sha, started_at)
SELECT 'seed-' || (g % 5), 'main', 'seed' || g, now() - (g || ' seconds')::interval
FROM generate_series(1, 20000) AS g;

INSERT INTO results (run_id, name, status, duration_ms)
SELECT r.id, 'test ' || i, (ARRAY['PASSED', 'FAILED', 'SKIPPED'])[1 + i % 3], i
FROM runs r CROSS JOIN generate_series(1, 50) AS i
WHERE r.commit_sha LIKE 'seed%';

ANALYZE;

SELECT (SELECT count(*) FROM runs) AS runs, (SELECT count(*) FROM results) AS results;
