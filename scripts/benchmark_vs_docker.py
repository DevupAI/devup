#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import math
import os
import platform
import re
import shutil
import statistics
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
DEVUP_BIN = REPO_ROOT / "devup"
DEVUP_API = "http://127.0.0.1:7777"
DOCKER_START_TIMEOUT = 180.0

WORKLOADS = {
    "python-http": {
        "runtime": "python",
        "dir": REPO_ROOT / "benchmarks" / "workloads" / "python-http",
        "devup_run": ["python3", "-c", "print('ok')"],
        "devup_start": ["python3", "server.py"],
        "docker_image": "python:3.12-slim",
        "docker_run": ["python3", "-c", "print('ok')"],
        "docker_start": ["python3", "server.py"],
        "profile": "service",
    },
    "node-http": {
        "runtime": "node",
        "dir": REPO_ROOT / "benchmarks" / "workloads" / "node-http",
        "devup_run": ["node", "-e", "console.log('ok')"],
        "devup_start": ["node", "server.js"],
        "docker_image": "node:20-slim",
        "docker_run": ["node", "-e", "console.log('ok')"],
        "docker_start": ["node", "server.js"],
        "profile": "interactive",
    },
    "ruby-http": {
        "runtime": "ruby",
        "dir": REPO_ROOT / "benchmarks" / "workloads" / "ruby-http",
        "devup_run": ["ruby", "-e", "puts 'ok'"],
        "devup_start": ["ruby", "server.rb"],
        "docker_image": "ruby:3.3-slim",
        "docker_run": ["ruby", "-e", "puts 'ok'"],
        "docker_start": ["ruby", "server.rb"],
        "profile": "service",
    },
    "java-http": {
        "runtime": "java",
        "dir": REPO_ROOT / "benchmarks" / "workloads" / "java-http",
        "devup_run": ["java", "-version"],
        "devup_start": ["java", "Main.java"],
        "docker_image": "eclipse-temurin:21-jdk-jammy",
        "docker_run": ["java", "-version"],
        "docker_start": ["java", "Main.java"],
        "profile": "service",
    },
    "php-http": {
        "runtime": "php",
        "dir": REPO_ROOT / "benchmarks" / "workloads" / "php-http",
        "devup_run": ["php", "-r", "echo 'ok\\n';"],
        "devup_start": ["php", "server.php"],
        "docker_image": "php:8.3-cli",
        "docker_run": ["php", "-r", "echo 'ok\\n';"],
        "docker_start": ["php", "server.php"],
        "profile": "service",
    },
}

DEVUP_VARIANTS = (
    ("devup", False),
    ("devup-shadow", True),
)


class BenchError(RuntimeError):
    pass


def run(
    cmd: list[str],
    *,
    cwd: Path | None = None,
    capture: bool = True,
    check: bool = True,
    env: dict[str, str] | None = None,
) -> subprocess.CompletedProcess[str]:
    proc = subprocess.run(
        cmd,
        cwd=str(cwd or REPO_ROOT),
        env=env,
        text=True,
        capture_output=capture,
    )
    if check and proc.returncode != 0:
        raise BenchError(
            f"command failed ({proc.returncode}): {' '.join(cmd)}\n"
            f"stdout:\n{proc.stdout}\n"
            f"stderr:\n{proc.stderr}"
        )
    return proc


def try_run(cmd: list[str], *, cwd: Path | None = None) -> subprocess.CompletedProcess[str]:
    return run(cmd, cwd=cwd, check=False)


def percentile(values: list[float], pct: float) -> float:
    if not values:
        return 0.0
    if len(values) == 1:
        return values[0]
    ordered = sorted(values)
    rank = (len(ordered) - 1) * pct
    lo = math.floor(rank)
    hi = math.ceil(rank)
    if lo == hi:
        return ordered[lo]
    fraction = rank - lo
    return ordered[lo] + (ordered[hi] - ordered[lo]) * fraction


def format_ms(value: float) -> str:
    return f"{value:.1f} ms"


def format_mb(value: float | int) -> str:
    return f"{float(value):.1f} MiB"


def read_devup_token() -> str:
    config_path = Path.home() / ".devup" / "config.json"
    data = json.loads(config_path.read_text())
    token = data.get("token", "").strip()
    if not token:
        raise BenchError(f"missing token in {config_path}")
    return token


def devup_request(path: str) -> bytes:
    token = read_devup_token()
    req = urllib.request.Request(
        DEVUP_API + path,
        headers={
            "X-Devup-Token": token,
            "X-Devup-Version": run([str(DEVUP_BIN), "version"]).stdout.strip().split()[-1],
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return resp.read()
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", "replace")
        raise BenchError(f"devup API {path} failed: {exc.code} {body}") from exc


def devup_jobs() -> list[dict]:
    payload = json.loads(devup_request("/ps").decode("utf-8"))
    return payload.get("jobs", [])


def devup_logs(job_id: str) -> str:
    query = urllib.parse.urlencode({"id": job_id})
    return devup_request(f"/logs?{query}").decode("utf-8", "replace")


def devup_job(job_id: str) -> dict | None:
    for job in devup_jobs():
        if job.get("job_id") == job_id:
            return job
    return None


def devup_wait_for_log(job_id: str, needle: str, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        logs = devup_logs(job_id)
        if needle in logs:
            return
        job = devup_job(job_id)
        if job and job.get("status") in {"failed", "exited", "stopped"} and needle not in logs:
            raise BenchError(f"devup job {job_id} exited before log marker {needle!r}")
        time.sleep(0.2)
    raise BenchError(f"timed out waiting for devup job {job_id} log marker {needle!r}")


def docker_running() -> bool:
    proc = try_run(["docker", "version", "--format", "{{.Server.Version}}"])
    return proc.returncode == 0 and bool((proc.stdout or "").strip())


def ensure_docker(timeout: float) -> None:
    if docker_running():
        return
    if platform.system() == "Darwin":
        subprocess.run(["open", "-a", "Docker"], check=False)
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if docker_running():
            return
        time.sleep(2)
    raise BenchError("Docker daemon is not running")


def ensure_devup() -> None:
    run(["go", "build", "-o", str(DEVUP_BIN), "./cmd/devup"])
    try_run(["limactl", "shell", "devup", "--", "sudo", "systemctl", "stop", "devup-agent"])
    try_run(["limactl", "shell", "devup", "--", "sudo", "pkill", "-f", "/usr/local/bin/devup-agent"])
    run([str(DEVUP_BIN), "vm", "up"], capture=False)
    run([str(DEVUP_BIN), "vm", "provision"], capture=False)


def docker_pull(images: list[str]) -> None:
    seen = set()
    for image in images:
        if image in seen:
            continue
        seen.add(image)
        run(["docker", "pull", image], capture=False)


def devup_run_latency(workload: dict, iterations: int, *, shadow: bool) -> dict:
    mount = f"{workload['dir']}:/workspace"
    cmd = [
        str(DEVUP_BIN),
        "run",
    ]
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
    run(cmd)
    samples = []
    for _ in range(iterations):
        start = time.monotonic()
        run(cmd)
        samples.append((time.monotonic() - start) * 1000.0)
    return summarize_samples(samples)


def docker_run_latency(workload: dict, iterations: int) -> dict:
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
    run(cmd)
    samples = []
    for _ in range(iterations):
        start = time.monotonic()
        run(cmd)
        samples.append((time.monotonic() - start) * 1000.0)
    return summarize_samples(samples)


def devup_start_service(workload_name: str, workload: dict, *, shadow: bool) -> tuple[float, float]:
    mount = f"{workload['dir']}:/workspace"
    cmd = [
        str(DEVUP_BIN),
        "start",
        "--profile",
        workload["profile"],
    ]
    if shadow:
        cmd.append("--shadow")
    cmd.extend([
        "--mount",
        mount,
        "--workdir",
        "/workspace",
        "--",
        *workload["devup_start"],
    ])
    started = time.monotonic()
    proc = run(cmd)
    job_id = proc.stdout.strip().splitlines()[-1].strip()
    try:
        devup_wait_for_log(job_id, "READY", timeout=30.0)
        ready_ms = (time.monotonic() - started) * 1000.0
        memory_mb = wait_for_devup_memory(job_id, timeout=15.0)
        return ready_ms, memory_mb
    finally:
        try_run([str(DEVUP_BIN), "stop", job_id])


def docker_start_service(workload_name: str, workload: dict) -> tuple[float, float]:
    container_name = f"devup-bench-{workload_name}-{int(time.time() * 1000)}"
    cmd = [
        "docker",
        "run",
        "-d",
        "--rm",
        "--name",
        container_name,
        "-v",
        f"{workload['dir']}:/workspace",
        "-w",
        "/workspace",
        workload["docker_image"],
        *workload["docker_start"],
    ]
    started = time.monotonic()
    try:
        run(cmd)
        wait_for_docker_log(container_name, "READY", timeout=30.0)
        ready_ms = (time.monotonic() - started) * 1000.0
        memory_mb = docker_container_memory_mb(container_name)
        return ready_ms, memory_mb
    finally:
        try_run(["docker", "rm", "-f", container_name])


def wait_for_devup_memory(job_id: str, timeout: float) -> float:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        job = devup_job(job_id)
        if not job:
            raise BenchError(f"devup job {job_id} disappeared before memory sample")
        memory = job.get("memory") or {}
        current = float(memory.get("current_mb", 0))
        if current > 0:
            return current
        time.sleep(1.0)
    job = devup_job(job_id)
    memory = (job or {}).get("memory") or {}
    return float(memory.get("current_mb", 0))


def wait_for_docker_log(container_name: str, needle: str, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        proc = try_run(["docker", "logs", container_name])
        logs = (proc.stdout or "") + (proc.stderr or "")
        if needle in logs:
            return
        inspect = try_run(["docker", "inspect", "-f", "{{.State.Status}}", container_name])
        status = inspect.stdout.strip() if inspect.returncode == 0 else ""
        if status in {"exited", "dead"}:
            raise BenchError(f"Docker container {container_name} exited before log marker {needle!r}")
        time.sleep(0.2)
    raise BenchError(f"timed out waiting for Docker container {container_name} log marker {needle!r}")


def docker_container_memory_mb(container_name: str) -> float:
    proc = run(["docker", "stats", "--no-stream", "--format", "{{.MemUsage}}", container_name])
    return parse_docker_memory(proc.stdout.strip())


def parse_docker_memory(value: str) -> float:
    current = value.split("/", 1)[0].strip()
    match = re.match(r"(?P<num>[0-9.]+)\s*(?P<unit>[KMG]i?B|B)", current)
    if not match:
        raise BenchError(f"could not parse docker memory value {value!r}")
    num = float(match.group("num"))
    unit = match.group("unit")
    factors = {
        "B": 1 / (1024 * 1024),
        "KB": 1 / 1024,
        "KiB": 1 / 1024,
        "MB": 1,
        "MiB": 1,
        "GB": 1024,
        "GiB": 1024,
    }
    return num * factors[unit]


def summarize_samples(samples: list[float]) -> dict:
    return {
        "samples_ms": [round(value, 3) for value in samples],
        "mean_ms": round(statistics.mean(samples), 3),
        "p50_ms": round(percentile(samples, 0.50), 3),
        "p95_ms": round(percentile(samples, 0.95), 3),
    }


def benchmark(iterations: int, docker_timeout: float) -> dict:
    ensure_devup()
    ensure_docker(docker_timeout)
    docker_pull([workload["docker_image"] for workload in WORKLOADS.values()])

    results = {
        "timestamp": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        "host": {
            "platform": platform.platform(),
            "docker_version": run(["docker", "--version"]).stdout.strip(),
            "devup_version": run([str(DEVUP_BIN), "version"]).stdout.strip(),
        },
        "ephemeral": {},
        "service": {},
    }

    for name, workload in WORKLOADS.items():
        results["ephemeral"][name] = {}
        for runtime, shadow in DEVUP_VARIANTS:
            results["ephemeral"][name][runtime] = devup_run_latency(workload, iterations, shadow=shadow)
        results["ephemeral"][name]["docker"] = docker_run_latency(workload, iterations)
        results["service"][name] = {}
        for runtime, shadow in DEVUP_VARIANTS:
            devup_ready_ms, devup_memory_mb = devup_start_service(name, workload, shadow=shadow)
            results["service"][name][runtime] = {
                "ready_ms": round(devup_ready_ms, 3),
                "idle_memory_mb": round(devup_memory_mb, 3),
            }
        docker_ready_ms, docker_memory_mb = docker_start_service(name, workload)
        results["service"][name]["docker"] = {
            "ready_ms": round(docker_ready_ms, 3),
            "idle_memory_mb": round(docker_memory_mb, 3),
        }

    return results


def print_summary(results: dict) -> None:
    print("")
    print("Ephemeral command latency")
    print("workload          runtime  mean        p50         p95")
    for workload, payload in results["ephemeral"].items():
        for runtime in ("devup", "devup-shadow", "docker"):
            metrics = payload[runtime]
            print(
                f"{workload:<17} {runtime:<7} {format_ms(metrics['mean_ms']):<10} "
                f"{format_ms(metrics['p50_ms']):<11} {format_ms(metrics['p95_ms'])}"
            )

    print("")
    print("Service ready time and idle memory")
    print("workload          runtime  ready       idle_memory")
    for workload, payload in results["service"].items():
        for runtime in ("devup", "devup-shadow", "docker"):
            metrics = payload[runtime]
            print(
                f"{workload:<17} {runtime:<7} {format_ms(metrics['ready_ms']):<10} "
                f"{format_mb(metrics['idle_memory_mb'])}"
            )


def main() -> int:
    parser = argparse.ArgumentParser(description="Benchmark devup against Docker")
    parser.add_argument("--iterations", type=int, default=5, help="timed iterations per ephemeral workload")
    parser.add_argument("--out", type=Path, help="write benchmark JSON to this path")
    parser.add_argument(
        "--docker-timeout",
        type=float,
        default=DOCKER_START_TIMEOUT,
        help="seconds to wait for the Docker daemon",
    )
    args = parser.parse_args()

    try:
        results = benchmark(args.iterations, args.docker_timeout)
    except BenchError as exc:
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
