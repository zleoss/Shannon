"""=============================================================================
文件: python/llm-service/llm_service/providers/base.py
-------------------------------------------------------------------------------
【一句话功能】
  历史兼容层：re-export ModelTier 与 ModelInfo dataclass。
【关键内容】
  re-export ModelTier :7
  ModelInfo dataclass（旧版定义）
【协作关系】
  供 api/providers、api/complexity 等旧调用点引用，避免重复枚举。
=============================================================================
-------------------------------------------------------------------------------
【原 docstring】
  Legacy provider base definitions - re-exports from core for backward compatibility.
=============================================================================
"""

from dataclasses import dataclass
from typing import Any

# Re-export ModelTier from canonical source to eliminate duplicate enum
from llm_provider.base import ModelTier

__all__ = ["ModelTier", "ModelInfo"]


@dataclass
class ModelInfo:
    """Model information for legacy API."""

    id: str
    name: str
    provider: Any  # Can be ProviderType enum or string
    tier: ModelTier
    context_window: int
    cost_per_1k_prompt_tokens: float
    cost_per_1k_completion_tokens: float
    supports_tools: bool = True
    supports_streaming: bool = True
    available: bool = True
