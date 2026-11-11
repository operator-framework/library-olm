#!/usr/bin/env python3
"""Reject reused E2E clusters with incompatible experimental revision schemas."""

import json
import sys


def require(condition, message):
    """Fail closed even when Python assertions are disabled."""
    if not condition:
        raise ValueError(message)


def validate(crd):
    """Require established v1 APIs accepting both typed revision object variants."""
    name = crd["metadata"]["name"]
    require(not crd["metadata"].get("deletionTimestamp"), f"{name} is deleting")
    require(any(c["type"] == "Established" and c["status"] == "True"
                for c in crd.get("status", {}).get("conditions", [])), f"{name} is not established")
    version = next(v for v in crd["spec"]["versions"] if v["name"] == "v1" and v["served"])
    spec = version["schema"]["openAPIV3Schema"]["properties"]["spec"]
    if name.startswith("clusterobjectdeployments."):
        spec = spec["properties"]["template"]["properties"]["spec"]
    fields = spec["properties"]["phases"]["items"]["properties"]["objects"]["items"]["properties"]
    require(fields["type"]["type"] == "string", f"{name} lacks the type discriminator")
    for variant in ("InlineManifest", "ObjectReference"):
        require(not fields["type"].get("enum") or variant in fields["type"]["enum"], f"{name} rejects {variant}")
    for field in ("inlineManifest", "objectRef"):
        require(fields[field]["type"] == "object", f"{name} lacks {field}")
    inline = fields["inlineManifest"]
    require(inline.get("x-kubernetes-embedded-resource") is True and
            inline.get("x-kubernetes-preserve-unknown-fields") is True and
            not inline.get("properties") and "additionalProperties" not in inline,
            f"{name} must preserve opaque inline manifests")
    reference = fields["objectRef"]
    for field in ("name", "namespace", "key"):
        property_schema = reference.get("properties", {}).get(field)
        require(property_schema.get("type") == "string" if property_schema is not None else
                (reference.get("x-kubernetes-preserve-unknown-fields") is True and
                 "additionalProperties" not in reference),
                f"{name} must preserve objectRef.{field} as a string")


if __name__ == "__main__":
    try:
        for resource in json.load(sys.stdin)["items"]:
            validate(resource)
    except (KeyError, StopIteration, TypeError, ValueError) as error:
        sys.exit(f"Incompatible experimental COS/COD definitions ({error}). "
                 "Tear down this E2E cluster and rerun setup to install the pinned controller.")
