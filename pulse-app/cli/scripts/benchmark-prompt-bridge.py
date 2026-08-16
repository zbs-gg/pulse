#!/usr/bin/env python3

import importlib.util
import json
import pathlib
import sys


def load_module(path: pathlib.Path):
    spec = importlib.util.spec_from_file_location("pulse_benchmark_reference_prompts", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("benchmark_reference_prompt_invalid")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def main():
    if len(sys.argv) != 3 or sys.argv[1] not in {"locomo", "longmemeval"}:
        raise RuntimeError("benchmark_prompt_bridge_arguments_invalid")
    suite = sys.argv[1]
    module = load_module(pathlib.Path(sys.argv[2]))
    for line in sys.stdin:
        request = json.loads(line)
        case = request["case"]
        if request["op"] == "answer":
            memories = [{"memory": value, "created_at": ""} for value in case["memories"]]
            if suite == "locomo":
                prompt = module.get_answer_generation_prompt(
                    case["question"], memories, reference_date=case.get("reference_date") or "2023"
                )
            else:
                date = module._to_human_date(case.get("question_date") or "")
                prompt = module.get_answer_generation_prompt(
                    case["question"], memories, question_date=date
                )
        elif request["op"] == "judge":
            if suite == "locomo":
                answer = module.preprocess_answer(int(case["category"]), case["gold_answer"])
                prompt = module.get_judge_prompt(
                    int(case["category"]), case["question"], answer, request["response"]
                )
            else:
                date = module._to_human_date(case.get("question_date") or "")
                prompt = module.get_judge_prompt(
                    case["category"], case["question_id"], case["question"],
                    case["gold_answer"], request["response"], date
                )
        else:
            raise RuntimeError("benchmark_prompt_bridge_operation_invalid")
        print(json.dumps({"id": request["id"], "prompt": prompt}), flush=True)


if __name__ == "__main__":
    main()
