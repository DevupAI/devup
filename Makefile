.PHONY: test-v1 bench stress
test-v1:
	bash scripts/test_v1.sh

bench:
	python3 scripts/benchmark_vs_docker.py --out .devup-bench/latest.json

stress:
	python3 scripts/stress_vs_docker.py --out .devup-bench/stress-latest.json
