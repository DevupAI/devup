#!/usr/bin/env python3

from __future__ import annotations

import argparse
import concurrent.futures
import json
import sys
import time
from pathlib import Path

import benchmark_vs_docker as bench


def devup_ephemeral_once(workload: dict, shadow: bool) -> None:
    mount = f"{workload['dir']}:/workspace"
    cmd = [str(bench.DEVUP_BIN), "run"]
    if shadow:
        cmd.append("--shadow")
    cmd.extend([
        "--mount",
        mount,
        "--workdir",
        "/workspace",
        "--",
        *workload["devup_run"],
    ])
    bench.run(cmd)


def docker_ephemeral_once(workload: dict) -> None:
    cmd = [
        "docker",
        "run",
        "--rm",
        "-v",
        f"{workload['dir']}:/workspace",
        "-w",
        "/workspace",
        workload["docker_image"],
        *workload["docker_run"],
    ]
    bench.run(cmd)


def warm_variants() -> None:
    for workload in bench.WORKLOADS.values():
        for _, shadow in bench.DEVUP_VARIANTS:
            devup_ephemeral_once(workload, shadow)
        docker_ephemeral_once(workload)


def service_churn(cycles: int) -> dict:
    results: dict[str, dict[str, dict[str, float | int]]] = {}
    for workload_name, workload in bench.WORKLOADS.items():
        workload_results: dict[str, dict[str, float | int]] = {}
        for runtime, shadow in bench.DEVUP_VARIANTS:
            samples = []
            failures = 0
            for _ in range(cycles):
                try:
                    ready_ms, _ = bench.devup_start_service(workload_name, workload, shadow=shadow)
                    samples.append(ready_ms)
                except bench.BenchError:
                    failures += 1
            workload_results[runtime] = summarize(samples, failures, cycles)
        samples = []
        failures = 0
        for _ in range(cycles):
            try:
                ready_ms, _ = bench.docker_start_service(workload_name, workload)
                samples.append(ready_ms)
            except bench.BenchError:
                failures += 1
        workload_results["docker"] = summarize(samples, failures, cycles)
        results[workload_name] = workload_results
    return results


def ephemeral_burst(rounds: int, parallelism: int) -> dict:
    results: dict[str, dict[str, dict[str, float | int]]] = {}
    for workload_name, workload in bench.WORKLOADS.items():
        workload_results: dict[str, dict[str, float | int]] = {}
        for runtime, shadow in bench.DEVUP_VARIANTS:
            workload_results[runtime] = run_burst(
                rounds,
                parallelism,
                lambda shadow=shadow, workload=workload: devup_ephemeral_once(workload, shadow),
            )
        workload_results["docker"] = run_burst(
            rounds,
            parallelism,
            lambda workload=workload: docker_ephemeral_once(workload),
        )
        results[workload_name] = workload_results
    return results


def run_burst(rounds: int, parallelism: int, fn) -> dict[str, float | int]:
    failures = 0
    samples = []
    for _ in range(rounds):
        started = time.monotonic()
        with concurrent.futures.ThreadPoolExecutor(max_workers=parallelism) as pool:
            futures = [pool.submit(fn) for _ in range(parallelism)]
            for future in concurrent.futures.as_completed(futures):
                try:
                    future.result()
                except Exception:
                    failures += 1
        samples.append((time.monotonic() - started) * 1000.0)
    return {
        "rounds": rounds,
        "parallelism": parallelism,
        "failures": failures,
        "mean_ms": round(sum(samples) / len(samples), 3) if samples else 0.0,
        "p95_ms": round(bench.percentile(samples, 0.95), 3) if samples else 0.0,
    }


def summarize(samples: list[float], failures: int, cycles: int) -> dict[str, float | int]:
    return {
        "cycles": cycles,
        "failures": failures,
        "mean_ready_ms": round(sum(samples) / len(samples), 3) if samples else 0.0,
        "p95_ready_ms": round(bench.percentile(samples, 0.95), 3) if samples else 0.0,
    }


def print_summary(results: dict) -> None:
    print("")
    print("Service churn")
    print("workload          runtime       mean_ready   p95_ready    failures")
    for workload, payload in results["service_churn"].items():
        for runtime in ("devup", "devup-shadow", "docker"):
            metrics = payload[runtime]
            print(
                f"{workload:<17} {runtime:<12} {bench.format_ms(metrics['mean_ready_ms']):<12} "
                f"{bench.format_ms(metrics['p95_ready_ms']):<12} {metrics['failures']}"
            )
    print("")
    print("Ephemeral burst")
    print("workload          runtime       mean_round   p95_round    failures")
    for workload, payload in results["ephemeral_burst"].items():
        for runtime in ("devup", "devup-shadow", "docker"):
            metrics = payload[runtime]
            print(
                f"{workload:<17} {runtime:<12} {bench.format_ms(metrics['mean_ms']):<12} "
                f"{bench.format_ms(metrics['p95_ms']):<12} {metrics['failures']}"
            )


def main() -> int:
    parser = argparse.ArgumentParser(description="Stress-test devup against Docker")
    parser.add_argument("--cycles", type=int, default=3, help="service churn cycles per workload/runtime")
    parser.add_argument("--rounds", type=int, default=3, help="parallel ephemeral rounds per workload/runtime")
    parser.add_argument("--parallelism", type=int, default=4, help="parallel ephemeral commands per round")
    parser.add_argument("--out", type=Path, help="write stress JSON to this path")
    parser.add_argument(
        "--docker-timeout",
        type=float,
        default=bench.DOCKER_START_TIMEOUT,
        help="seconds to wait for the Docker daemon",
    )
    args = parser.parse_args()

    try:
        bench.ensure_devup()
        bench.ensure_docker(args.docker_timeout)
        bench.docker_pull([workload["docker_image"] for workload in bench.WORKLOADS.values()])
        warm_variants()
        results = {
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
            "service_churn": service_churn(args.cycles),
            "ephemeral_burst": ephemeral_burst(args.rounds, args.parallelism),
        }
    except bench.BenchError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1

    print_summary(results)
    if args.out:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(json.dumps(results, indent=2) + "\n")
        print("")
        print(f"Wrote {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
