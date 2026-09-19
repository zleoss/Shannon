"""=============================================================================
文件: python/llm-service/llm_service/tools/builtin/x_search.py
-------------------------------------------------------------------------------
【一句话功能】
  X/Twitter 搜索工具：通过 xAI Responses API 调 Grok 服务端 x_search。
【关键内容】
  XSearchTool :123
  provider 无关 tool-call 接口；内部委托 xAI Grok
【协作关系】
  被 agent 调用获取推文与 citation；底层走 xAI provider。
=============================================================================
-------------------------------------------------------------------------------
【原 docstring】
  X Search Tool — search X/Twitter via xAI Responses API.

  Uses Grok's server-side ``x_search`` tool to retrieve posts and citations.
  The tool is provider-agnostic: any Shannon agent (Claude, GPT, etc.) can
  invoke it via a standard tool call; internally it delegates to xAI.
=============================================================================
"""

from __future__ import annotations

import logging
import os
import time
from typing import Any, Dict, List, Optional, Tuple

import httpx
import yaml

from llm_provider.base import compute_token_cost

from ..base import Tool, ToolMetadata, ToolParameter, ToolParameterType, ToolResult

logger = logging.getLogger(__name__)

# Defaults (overridable via env)
_DEFAULT_MODEL = "grok-4-1-fast-non-reasoning"
_DEFAULT_RATE_LIMIT = 30  # req/min
_DEFAULT_TIMEOUT = 60  # seconds

# xAI Responses API x_search pricing: $5 / 1K calls = $0.005 per call
_X_SEARCH_COST_PER_CALL = 0.005

# Fallback token prices (per 1K) if models.yaml is unavailable.
# Source of truth is config/models.yaml; these mirror grok-4-1-fast as of 2026-04.
_FALLBACK_INPUT_PER_1K = 0.0002   # $0.20 / 1M
_FALLBACK_OUTPUT_PER_1K = 0.0005  # $0.50 / 1M

_XAI_PRICING_CACHE: Optional[Dict[str, Dict[str, float]]] = None


def _load_xai_pricing() -> Dict[str, Dict[str, float]]:
    """Load xAI model pricing from config/models.yaml. Cached after first call.

    Mirrors the path-resolution used by llm_provider.manager._load_and_apply_pricing_overrides.
    Returns an empty dict when config is unavailable; callers fall back to constants.
    """
    global _XAI_PRICING_CACHE
    if _XAI_PRICING_CACHE is not None:
        return _XAI_PRICING_CACHE

    config_path = os.getenv("MODELS_CONFIG_PATH", "/app/config/models.yaml")
    if not os.path.exists(config_path):
        # Local-dev fallback: walk up from this file to repo root (.../python/llm-service/...)
        alt = "./config/models.yaml"
        config_path = alt if os.path.exists(alt) else config_path

    try:
        with open(config_path, "r") as f:
            cfg = yaml.safe_load(f) or {}
        xai_models = (cfg.get("pricing") or {}).get("models", {}).get("xai") or {}
        result: Dict[str, Dict[str, float]] = dict(xai_models)
    except Exception as exc:  # noqa: BLE001
        logger.warning("x_search: failed to load pricing from %s (%s); using fallback", config_path, exc)
        result = {}
    _XAI_PRICING_CACHE = result
    return result


_XAI_BASE_URL_CACHE: Optional[str] = None


def _resolve_xai_base_url() -> str:
    """Resolve the xAI Responses API base URL for x_search.

    Precedence:
    1. ``XAI_BASE_URL`` env var (if non-empty) — explicit opt-in.
    2. Public default ``https://api.x.ai/v1``.

    We intentionally do NOT auto-inherit ``provider_settings.xai.base_url``
    from ``config/models.yaml``: that URL is set for the chat-completions
    path and may point at an OpenAI-compatible proxy that does not expose
    the ``/responses`` endpoint or the server-side ``x_search`` tool.
    Operators who want x_search routed through a proxy must set
    ``XAI_BASE_URL`` explicitly.

    Empty-string env values are treated as unset, so a misconfigured
    deployment template won't produce an invalid ``/responses`` URL.
    """
    global _XAI_BASE_URL_CACHE
    if _XAI_BASE_URL_CACHE is not None:
        return _XAI_BASE_URL_CACHE

    env_url = os.getenv("XAI_BASE_URL", "").strip()
    if env_url:
        _XAI_BASE_URL_CACHE = env_url.rstrip("/")
    else:
        _XAI_BASE_URL_CACHE = "https://api.x.ai/v1"
    return _XAI_BASE_URL_CACHE


def _reload_caches() -> None:
    """Drop module-level pricing/base-url caches so the next call re-reads env
    and ``config/models.yaml``. Intended for hot-reload hooks and tests; no
    watcher is wired up yet (caches remain process-lifetime by default).
    """
    global _XAI_PRICING_CACHE, _XAI_BASE_URL_CACHE
    _XAI_PRICING_CACHE = None
    _XAI_BASE_URL_CACHE = None


def _get_token_prices(model: str) -> Tuple[float, float]:
    """Return (input_per_1k, output_per_1k) for an xAI model, with safe fallback."""
    pricing = _load_xai_pricing()
    entry = pricing.get(model) or {}
    ip = entry.get("input_per_1k")
    op = entry.get("output_per_1k")
    return (
        float(ip) if isinstance(ip, (int, float)) else _FALLBACK_INPUT_PER_1K,
        float(op) if isinstance(op, (int, float)) else _FALLBACK_OUTPUT_PER_1K,
    )


class XSearchTool(Tool):
    """Search X/Twitter content via xAI Responses API."""

    def _get_metadata(self) -> ToolMetadata:
        return ToolMetadata(
            name="x_search",
            version="1.0.0",
            description=(
                "Search X/Twitter posts, profiles, and activity. "
                "Supports filtering by user handle (e.g. elonmusk) and date range. "
                "Returns full post text, engagement metrics, and citation links to original posts. "
                "Covers profile research, sentiment analysis, and topic tracking."
            ),
            category="search",
            author="Shannon",
            requires_auth=True,
            rate_limit=int(os.getenv("X_SEARCH_RATE_LIMIT", str(_DEFAULT_RATE_LIMIT))),
            timeout_seconds=int(os.getenv("X_SEARCH_TIMEOUT", str(_DEFAULT_TIMEOUT))),
            # Real cost is per-call fee × N calls + token cost; reported via
            # ToolResult.cost_usd. cost_per_use stays 0 so callers that fall
            # back to the static metadata field don't systematically undercount.
            cost_per_use=0.0,
        )

    def _get_parameters(self) -> List[ToolParameter]:
        return [
            ToolParameter(
                name="query",
                type=ToolParameterType.STRING,
                description="What to search for on X/Twitter (e.g. 'AI safety', 'product launch')",
                required=True,
            ),
            ToolParameter(
                name="allowed_handles",
                type=ToolParameterType.STRING,
                description=(
                    "Only search posts from these users, comma-separated without @ "
                    "(e.g. 'elonmusk,OpenAI'). Max 10. Mutually exclusive with excluded_handles."
                ),
                required=False,
            ),
            ToolParameter(
                name="excluded_handles",
                type=ToolParameterType.STRING,
                description=(
                    "Exclude posts from these users, comma-separated without @ "
                    "(e.g. 'spambot1,spambot2'). Max 10. Mutually exclusive with allowed_handles."
                ),
                required=False,
            ),
            ToolParameter(
                name="from_date",
                type=ToolParameterType.STRING,
                description="Search start date in YYYY-MM-DD format",
                required=False,
            ),
            ToolParameter(
                name="to_date",
                type=ToolParameterType.STRING,
                description="Search end date in YYYY-MM-DD format",
                required=False,
            ),
            ToolParameter(
                name="result_instructions",
                type=ToolParameterType.STRING,
                description=(
                    "Tell the search engine what data you need and how to format results. "
                    "E.g. 'list each post with full text and date' or "
                    "'give me a profile summary with follower count and bio' or "
                    "'summarize the overall sentiment'. Tailor this to your task."
                ),
                required=False,
            ),
        ]

    async def _execute_impl(
        self, session_context: Optional[Dict] = None, **kwargs: Any
    ) -> ToolResult:
        query: str = kwargs.get("query", "").strip()
        if not query:
            return ToolResult(success=False, output=None, error="query is required")

        api_key = os.getenv("XAI_API_KEY", "").strip()
        if not api_key:
            return ToolResult(
                success=False, output=None, error="XAI_API_KEY not configured"
            )

        model = os.getenv("X_SEARCH_MODEL", _DEFAULT_MODEL)
        timeout = int(os.getenv("X_SEARCH_TIMEOUT", str(_DEFAULT_TIMEOUT)))
        # Honor xAI base URL from (in order): XAI_BASE_URL env, provider_settings.xai.base_url
        # in config/models.yaml, or public xAI. Matches the resolution XAIProvider uses.
        responses_url = f"{_resolve_xai_base_url()}/responses"

        # Build x_search tool parameters
        x_search_params: Dict[str, Any] = {}

        allowed = kwargs.get("allowed_handles", "")
        excluded = kwargs.get("excluded_handles", "")
        if allowed and excluded:
            return ToolResult(
                success=False,
                output=None,
                error="allowed_handles and excluded_handles are mutually exclusive",
            )

        if allowed:
            handles = [h.strip().lstrip("@") for h in str(allowed).split(",") if h.strip()]
            if len(handles) > 10:
                return ToolResult(
                    success=False, output=None, error="max 10 allowed_handles"
                )
            x_search_params["allowed_x_handles"] = handles

        if excluded:
            handles = [h.strip().lstrip("@") for h in str(excluded).split(",") if h.strip()]
            if len(handles) > 10:
                return ToolResult(
                    success=False, output=None, error="max 10 excluded_handles"
                )
            x_search_params["excluded_x_handles"] = handles

        from_date = kwargs.get("from_date", "")
        if from_date:
            x_search_params["from_date"] = str(from_date).strip()

        to_date = kwargs.get("to_date", "")
        if to_date:
            x_search_params["to_date"] = str(to_date).strip()

        # Build Responses API payload
        tool_def: Dict[str, Any] = {"type": "x_search"}
        if x_search_params:
            tool_def.update(x_search_params)

        # Build prompt: let the calling LLM's instructions drive the output format
        instructions = kwargs.get("result_instructions", "")
        if instructions:
            prompt = (
                f"Search X/Twitter for: {query}\n\n"
                f"Instructions for results: {instructions}\n\n"
                "Include citation links for each post found."
            )
        else:
            # Minimal default: detailed per-post listing
            prompt = (
                f"Search X/Twitter for: {query}\n\n"
                "List each post individually with: author, date, full text, "
                "engagement metrics, and citation link."
            )

        payload: Dict[str, Any] = {
            "model": model,
            "tools": [tool_def],
            "input": prompt,
        }

        start = time.time()
        try:
            async with httpx.AsyncClient(timeout=timeout) as client:
                resp = await client.post(
                    responses_url,
                    headers={
                        "Authorization": f"Bearer {api_key}",
                        "Content-Type": "application/json",
                    },
                    json=payload,
                )

            if resp.status_code != 200:
                return ToolResult(
                    success=False,
                    output=None,
                    error=f"xAI API returned {resp.status_code}: {resp.text[:500]}",
                )

            data = resp.json()
        except httpx.TimeoutException:
            return ToolResult(
                success=False, output=None, error=f"xAI API timeout ({timeout}s)"
            )
        except Exception as exc:
            return ToolResult(
                success=False, output=None, error=f"xAI API error: {exc}"
            )

        latency_ms = int((time.time() - start) * 1000)

        # Extract summary text and citations from response
        summary = self._extract_text(data)
        citations = self._extract_citations(data)

        # Extract x_search call count from usage.server_side_tool_usage_details
        usage = data.get("usage") or {}
        tool_details = usage.get("server_side_tool_usage_details") or {}
        x_search_calls = int(tool_details.get("x_search_calls", 0) or 0)

        # Token usage
        input_tokens = int(usage.get("input_tokens", 0) or 0)
        output_tokens = int(usage.get("output_tokens", 0) or 0)
        # xAI Responses API: cached_tokens nested under input_tokens_details
        input_details = usage.get("input_tokens_details") or {}
        cached_tokens = int(input_details.get("cached_tokens", 0) or 0)

        # Cost: x_search per-call fee + token cost (centralized pricing + cache-aware).
        # Delegate token math to the shared helper so xAI/Kimi/Anthropic/OpenAI
        # cache semantics stay in one place (llm_provider.base.compute_token_cost).
        input_per_1k, output_per_1k = _get_token_prices(model)
        token_cost = compute_token_cost(
            input_per_1k=input_per_1k,
            output_per_1k=output_per_1k,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            cache_read_tokens=cached_tokens,
            provider="xai",
        )
        search_cost = x_search_calls * _X_SEARCH_COST_PER_CALL
        total_cost = token_cost + search_cost

        output = {
            "summary": summary,
            "citations": citations,
            "query": query,
            "x_search_calls": x_search_calls,
            "note": (
                "This search has full access to all public X/Twitter data. "
                "If few results were found, it means few posts match the query, "
                "not a data access limitation. You can refine the query and search again."
            ),
        }

        return ToolResult(
            success=True,
            output=output,
            metadata={
                "latency_ms": latency_ms,
                "input_tokens": input_tokens,
                "output_tokens": output_tokens,
                "x_search_calls": x_search_calls,
            },
            cost_usd=total_cost,
            cost_model=model,
        )

    @staticmethod
    def _extract_text(data: Dict[str, Any]) -> str:
        """Extract text content from Responses API output."""
        parts: List[str] = []
        output = data.get("output") or []
        if not isinstance(output, list):
            return str(output)

        for item in output:
            if not isinstance(item, dict):
                continue
            item_type = item.get("type", "")
            if item_type in ("output_text", "text"):
                val = item.get("text") or item.get("content") or ""
                if isinstance(val, str) and val.strip():
                    parts.append(val.strip())
            elif item_type == "message":
                for block in item.get("content", []) or []:
                    if isinstance(block, dict) and block.get("type") in (
                        "output_text",
                        "text",
                    ):
                        val = block.get("text") or ""
                        if isinstance(val, str) and val.strip():
                            parts.append(val.strip())

        return "\n\n".join(parts)

    @staticmethod
    def _extract_citations(data: Dict[str, Any]) -> List[str]:
        """Extract citation URLs from Responses API output annotations."""
        urls: List[str] = []
        seen: set = set()
        output = data.get("output") or []
        if not isinstance(output, list):
            return urls

        for item in output:
            if not isinstance(item, dict):
                continue
            # Citations live in message.content[].annotations[]
            content_blocks = []
            if item.get("type") == "message":
                content_blocks = item.get("content", []) or []
            elif item.get("type") in ("output_text", "text"):
                content_blocks = [item]

            for block in content_blocks:
                if not isinstance(block, dict):
                    continue
                for ann in block.get("annotations", []) or []:
                    if isinstance(ann, dict) and ann.get("type") == "url_citation":
                        url = ann.get("url", "")
                        if url and url not in seen:
                            seen.add(url)
                            urls.append(url)

        return urls
