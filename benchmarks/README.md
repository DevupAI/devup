# Devup vs Docker Benchmarks

This directory contains small, repeatable workloads for comparing plain
`devup`, `devup --shadow`, and Docker on the same machine.

Current metrics:

- Ephemeral command latency
- Background service ready time
- Idle service memory usage

Current workloads:

- `python-http`: Python standard-library HTTP server
- `node-http`: Node standard-library HTTP server
- `ruby-http`: Ruby standard-library TCP HTTP server
- `java-http`: Java single-file `HttpServer`
- `php-http`: PHP socket-based HTTP server

Run the benchmark harness with:

```bash
python3 scripts/benchmark_vs_docker.py --iterations 5 --out .devup-bench/latest.json
python3 scripts/stress_vs_docker.py --cycles 3 --rounds 3 --parallelism 4 --out .devup-bench/stress-latest.json
```

The harness will:

1. Build `devup`
2. Ensure the Lima VM is up
3. Ensure Docker is running
4. Warm Docker images outside the measured timings
5. Run the same workloads in all three runtime modes
6. Print a side-by-side summary and optionally write JSON with `--out`

Notes:

- Docker image pull time is intentionally excluded from measured results.
- `devup` service memory comes from the agent `/ps` API.
- Docker memory comes from `docker stats --no-stream`.
- `devup --shadow` materializes the mounted workspace onto native Linux storage
  inside the VM before launch. It is meant for filesystem-sensitive services
  rather than tiny one-shot commands.
- `scripts/stress_vs_docker.py` adds two heavier checks: repeated service churn
  and parallel ephemeral bursts.
