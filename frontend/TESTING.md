# Frontend test execution

`vitest` runs with a single forked worker and `fileParallelism: false`.

Reason: the jsdom React suite (16 files, 109 tests at R0-07) intermittently
crashed a parallel worker with exit code 134 (SIGABRT) on the Windows 10 test
host. The crash happened between test files, not in an assertion, so a
parallel run could make otherwise-green cases fail nondeterministically.
Serialising files keeps the same tests and assertions while bounding memory
and worker concurrency.

Do not raise `maxWorkers` or re-enable file parallelism without a fresh
five-run stability check on the target host.
