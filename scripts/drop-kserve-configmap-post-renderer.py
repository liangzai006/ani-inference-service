#!/usr/bin/env python3
"""Remove KServe's built-in sample resources from platform chart output.

The default mode is for the LLMI chart. The ``core`` mode removes sample
ClusterServingRuntime and ClusterStorageContainer resources, whose engine
arguments are platform examples rather than ANI request data. It also leaves
the LLMI webhook configurations to the dedicated LLMI release, avoiding
cluster-scoped Helm ownership conflicts between the two KServe charts.
"""

from __future__ import annotations

import re
import sys


def kind(document: str) -> str:
    match = re.search(r"(?m)^kind:\s*([^\s#]+)\s*$", document)
    return match.group(1) if match else ""


def metadata_name(document: str) -> str:
    match = re.search(r"(?m)^\s{2}name:\s*([^\s#]+)\s*$", document)
    return match.group(1) if match else ""


def remove_document(document: str, mode: str) -> bool:
    resource_kind = kind(document)
    name = metadata_name(document)
    if mode == "core":
        return resource_kind in {"ClusterServingRuntime", "ClusterStorageContainer"} or (
            resource_kind == "ValidatingWebhookConfiguration"
            and name in {
                "llminferenceservice.serving.kserve.io",
                "llminferenceserviceconfig.serving.kserve.io",
            }
        )
    if mode != "llmisvc":
        raise ValueError(f"unsupported post-renderer mode: {mode}")
    return (
        resource_kind == "ConfigMap" and name == "inferenceservice-config"
    ) or resource_kind == "LLMInferenceServiceConfig"


def render(payload: str, mode: str = "llmisvc") -> str:
    documents = payload.split("\n---")
    kept = [document for document in documents if not remove_document(document, mode)]
    return "\n---".join(kept)


def main() -> None:
    mode = "llmisvc"
    for argument in sys.argv[1:]:
        if argument.startswith("--mode="):
            mode = argument.split("=", 1)[1]
        else:
            raise SystemExit(f"unknown argument: {argument}")
    try:
        sys.stdout.write(render(sys.stdin.read(), mode))
    except ValueError as error:
        raise SystemExit(str(error)) from error


if __name__ == "__main__":
    main()
