# Notification worker throughput, 20261023T073545Z

- Command: `JOBS=5000 scripts/notify_throughput.sh`
- Each row: enqueue 5000 jobs, start the worker(s), time until every job is marked done in Postgres
- Handler = sleep(send latency), standing in for an email API call; 0s measures pure queue overhead
- pgxpool max 20 connections per instance; claim batch <= 32; lease 2m
- Machine: 8 CPUs, 16770519040 bytes RAM available to Docker (Docker Desktop); Postgres and workers in Docker on the same machine

| send latency | instances | goroutines per instance | jobs | seconds | jobs/sec | handled twice |
|---:|---:|---:|---:|---:|---:|---:|
| 20ms | 1 | 1 | 5000 | 129.56 | 39 | 0 |
| 20ms | 1 | 4 | 5000 | 31.99 | 156 | 0 |
| 20ms | 1 | 16 | 5000 | 8.52 | 587 | 0 |
| 20ms | 1 | 64 | 5000 | 2.21 | 2260 | 0 |
| 20ms | 2 | 16 | 5000 | 5.65 | 885 | 0 |
| 0s | 1 | 16 | 5000 | 4.92 | 1017 | 0 |
| 0s | 1 | 64 | 5000 | 2.62 | 1909 | 0 |
