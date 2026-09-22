#!/usr/bin/env python3
"""Group exported JSONL by connected conversations/tasks before train/eval split."""
import argparse
import hashlib
import json
from pathlib import Path


def split_samples(samples, evaluation_percent=20):
    parent = {}
    def root(key):
        parent.setdefault(key, key)
        if parent[key] != key:
            parent[key] = root(parent[key])
        return parent[key]
    groups = []
    for sample in samples:
        links = ["conversation:" + str(sample["split_group"])]
        # Use all recalled tasks, not just the chosen one, so the same task
        # cannot enter both partitions through an alternative shown to a user.
        snap = sample.get("snapshot") or {}
        links += ["task:" + task for task in (snap.get("recalled_ids") or [])]
        execution = sample.get("execution_result") or {}
        links += ["task:" + task["issue_id"] for task in (execution.get("tasks") or []) if task.get("issue_id")]
        for link in links[1:]:
            parent[root(link)] = root(links[0])
        root(links[0])
        groups.append(links[0])
    stable = {}
    for key in sorted(parent):
        stable.setdefault(root(key), key)
    result = {"train": [], "evaluation": []}
    for sample, group in zip(samples, groups):
        key = stable[root(group)]
        partition = "evaluation" if int(hashlib.sha256(key.encode()).hexdigest()[:8], 16) % 100 < evaluation_percent else "train"
        result[partition].append(dict(sample, split_component=key))
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("--evaluation-percent", type=int, default=20)
    args = parser.parse_args()
    if not 0 <= args.evaluation_percent <= 100:
        parser.error("evaluation-percent must be 0..100")
    samples = [json.loads(line) for line in args.input.read_text().splitlines() if line.strip()]
    args.output.mkdir(parents=True, exist_ok=True)
    for name, rows in split_samples(samples, args.evaluation_percent).items():
        (args.output / (name + ".jsonl")).write_text("".join(json.dumps(row, ensure_ascii=False) + "\n" for row in rows))
        print(name, len(rows))


if __name__ == "__main__":
    main()
