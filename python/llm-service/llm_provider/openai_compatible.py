"""=============================================================================
文件: python/llm-service/llm_provider/openai_compatible.py
-------------------------------------------------------------------------------
【一句话功能】
  OpenAI 兼容通用 provider（DeepSeek/Qwen/本地模型等）。
【关键内容】
  complete :229 / stream_complete :426 / count_tokens :181
  使用 AsyncOpenAI 客户端统一对接
【协作关系】
  作为兼容 API 的兜底/默认 provider，被 LLMManager 动态加载。
=============================================================================
-------------------------------------------------------------------------------
【原 docstring】
  OpenAI-Compatible Provider Implementation
  For providers that implement OpenAI's API (DeepSeek, Qwen, local models, etc.)
=============================================================================
"""

import logging
from typing import Dict, List, Any, AsyncIterator
from openai import AsyncOpenAI

logger = logging.getLogger(__name__)

from .base import (
    LLMProvider,
    ModelConfig,
    ModelTier,
    CompletionRequest,
    CompletionResponse,
    TokenUsage,
    TokenCounter,
    prepare_openai_messages,
)


class OpenAICompatibleProvider(LLMProvider):
    """Provider for OpenAI-compatible APIs"""

    def __init__(self, config: Dict[str, Any]):
        # Get API configuration
        self.api_key = config.get("api_key", "dummy")  # Some providers don't need keys
        self.base_url = config.get(
            "base_url", "http://localhost:11434/v1"
        )  # Default for Ollama

        # Initialize OpenAI client with custom base URL
        self.client = AsyncOpenAI(
            api_key=self.api_key,
            base_url=self.base_url,
            max_retries=config["max_retries"] if "max_retries" in config else 2,
        )

        super().__init__(config)

    def _initialize_models(self):
        """Initialize models from configuration"""

        self._load_models_from_config(allow_empty=True)

        # If no models configured, add some defaults for developer convenience
        if not self.models:
            self._add_default_models()

    def _add_default_models(self):
        """Add default model configurations for common providers"""

        # Detect provider type from base URL
        if "deepseek" in self.base_url.lower():
            self._add_deepseek_models()
        elif "dashscope" in self.base_url.lower() or "qwen" in self.base_url.lower():
            self._add_qwen_models()
        elif "localhost" in self.base_url or "ollama" in self.base_url.lower():
            self._add_ollama_models()
        else:
            # Generic OpenAI-compatible model
            self.models["default"] = ModelConfig(
                provider="openai_compatible",
                model_id="default",
                tier=ModelTier.MEDIUM,
                max_tokens=4096,
                context_window=8192,
                input_price_per_1k=0.001,
                output_price_per_1k=0.002,
            )

    def _add_deepseek_models(self):
        """Add DeepSeek model configurations"""

        self.models["deepseek-chat"] = ModelConfig(
            provider="deepseek",
            model_id="deepseek-chat",
            tier=ModelTier.SMALL,
            max_tokens=4096,
            context_window=32768,
            input_price_per_1k=0.0001,
            output_price_per_1k=0.0002,
        )

        self.models["deepseek-coder"] = ModelConfig(
            provider="deepseek",
            model_id="deepseek-coder",
            tier=ModelTier.MEDIUM,
            max_tokens=4096,
            context_window=16384,
            input_price_per_1k=0.0001,
            output_price_per_1k=0.0002,
        )

        self.models["deepseek-v3"] = ModelConfig(
            provider="deepseek",
            model_id="deepseek-v3",
            tier=ModelTier.MEDIUM,
            max_tokens=8192,
            context_window=64000,
            input_price_per_1k=0.001,
            output_price_per_1k=0.002,
        )

    def _add_qwen_models(self):
        """Add Qwen model configurations"""

        self.models["qwen-turbo"] = ModelConfig(
            provider="qwen",
            model_id="qwen-turbo",
            tier=ModelTier.SMALL,
            max_tokens=4096,
            context_window=8192,
            input_price_per_1k=0.0003,
            output_price_per_1k=0.0006,
        )

        self.models["qwen-plus"] = ModelConfig(
            provider="qwen",
            model_id="qwen-plus",
            tier=ModelTier.MEDIUM,
            max_tokens=8192,
            context_window=32768,
            input_price_per_1k=0.0008,
            output_price_per_1k=0.002,
        )

        self.models["qwen-max"] = ModelConfig(
            provider="qwen",
            model_id="qwen-max",
            tier=ModelTier.LARGE,
            max_tokens=8192,
            context_window=32768,
            input_price_per_1k=0.002,
            output_price_per_1k=0.006,
        )

        self.models["qwq-32b"] = ModelConfig(
            provider="qwen",
            model_id="qwq-32b-preview",
            tier=ModelTier.LARGE,
            max_tokens=32768,
            context_window=32768,
            input_price_per_1k=0.001,
            output_price_per_1k=0.003,
        )

    def _add_ollama_models(self):
        """Add Ollama model configurations"""

        # Common Ollama models
        self.models["llama2"] = ModelConfig(
            provider="ollama",
            model_id="llama2",
            tier=ModelTier.SMALL,
            max_tokens=4096,
            context_window=4096,
            input_price_per_1k=0.0,  # Local models have no cost
            output_price_per_1k=0.0,
        )

        self.models["codellama"] = ModelConfig(
            provider="ollama",
            model_id="codellama",
            tier=ModelTier.MEDIUM,
            max_tokens=4096,
            context_window=4096,
            input_price_per_1k=0.0,
            output_price_per_1k=0.0,
        )

    def _resolve_alias(self, model_id: str) -> str:
        """Return the configured alias for a given vendor model_id, if any."""
        for alias, cfg in self.models.items():
            if cfg.model_id == model_id:
                return alias
        return model_id

    def count_tokens(self, messages: List[Dict[str, Any]], model: str) -> int:
        """
        Count tokens for the model.
        Uses generic estimation since tokenizers vary by provider.
        """
        return TokenCounter.count_messages_tokens(messages, model)

    def _apply_provider_defaults(self, api_request: dict) -> None:
        """Inject provider-specific parameters and strip unsupported ones.

        Chinese LLM providers are nominally OpenAI-compatible but each has
        quirks that require per-provider adjustments:

        **Reasoning/thinking control:**
        - Z.AI GLM-4.7/5: ``thinking.type = "disabled"``
        - Kimi K2.5:       ``thinking.type = "disabled"``
          (kimi-k2-thinking is a dedicated reasoning model — no override)

        **Parameter restrictions:**
        - Kimi K2.5: temperature/top_p/frequency_penalty/presence_penalty
          are locked by the API and cannot be modified — strip them

        Note: MiniMax now uses Anthropic-compatible provider and is no longer
        routed through this class.
        """
        url = self.base_url.lower()
        model = api_request.get("model", "").lower()

        if "z.ai" in url:
            # Z.AI: disable thinking for non-reasoning models.
            if "thinking" not in model:
                api_request.setdefault("extra_body", {})
                api_request["extra_body"].setdefault(
                    "thinking", {"type": "disabled"}
                )

        elif "moonshot" in url:
            # Kimi: disable thinking for non-reasoning models.
            if "thinking" not in model:
                api_request.setdefault("extra_body", {})
                api_request["extra_body"].setdefault(
                    "thinking", {"type": "disabled"}
                )
            # Kimi K2.5 locks these parameters — sending them causes errors.
            if "k2.5" in model or "k2-turbo" in model:
                for key in ("temperature", "top_p", "frequency_penalty", "presence_penalty"):
                    api_request.pop(key, None)

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        """Generate a completion using the OpenAI-compatible API"""

        # Select model based on tier or explicit override
        model_config = self.resolve_model_config(request)
        model = model_config.model_id
        model_alias = self._resolve_alias(model)

        # Prepare API request (translate Anthropic-style image blocks to OpenAI format)
        messages = prepare_openai_messages(request.messages)
        api_request = {
            "model": model,
            "messages": messages,
            "temperature": request.temperature,
            "top_p": request.top_p,
            "frequency_penalty": request.frequency_penalty,
            "presence_penalty": request.presence_penalty,
        }

        if request.max_tokens:
            api_request["max_tokens"] = min(request.max_tokens, model_config.max_tokens)

        if request.stop:
            api_request["stop"] = request.stop

        if request.functions and model_config.supports_functions:
            # Convert legacy 'functions' to 'tools' format (Kimi K2.5+ requires 'tools')
            tools = []
            for fn in request.functions:
                if isinstance(fn, dict) and fn.get("type") == "function":
                    tools.append(fn)  # Already in tools format
                elif isinstance(fn, dict):
                    tools.append({"type": "function", "function": fn})
            if tools:
                api_request["tools"] = tools
            if request.function_call:
                # Convert function_call to tool_choice format
                if isinstance(request.function_call, dict) and "name" in request.function_call:
                    api_request["tool_choice"] = {"type": "function", "function": {"name": request.function_call["name"]}}
                elif request.function_call == "auto":
                    api_request["tool_choice"] = "auto"
                elif request.function_call == "none":
                    api_request["tool_choice"] = "none"

        if request.seed is not None:
            api_request["seed"] = request.seed

        if request.response_format:
            api_request["response_format"] = request.response_format

        if request.user:
            api_request["user"] = request.user

        # Provider-specific reasoning mode handling.
        # Many Chinese LLM providers default to thinking/reasoning ON, which puts
        # output in non-standard fields and breaks agent protocol JSON parsing.
        self._apply_provider_defaults(api_request)

        # Kimi: inject prompt_cache_key for session-based cache routing.
        # Official API field that improves cache hit rate for multi-turn/agent scenarios.
        if "moonshot" in self.base_url.lower() and request.session_id:
            api_request.setdefault("extra_body", {})["prompt_cache_key"] = request.session_id

        # Make API call
        import time

        start_time = time.time()

        try:
            response = await self.client.chat.completions.create(**api_request)
        except Exception as e:
            raise Exception(f"OpenAI-compatible API error ({self.base_url}): {e}")

        latency_ms = int((time.time() - start_time) * 1000)

        # Extract response
        choice = response.choices[0]
        message = choice.message

        # Normalize content: some compatible providers may return a list of
        # content parts rather than a plain string. Extract text segments.
        # Also handles reasoning models (Z.AI GLM, DeepSeek-R1, Kimi-K2-thinking)
        # that return content in 'reasoning_content' when 'content' is empty.
        url = self.base_url.lower()
        model_lower = model.lower()
        # Only use reasoning_content fallback for known thinking models that
        # put real output there. Otherwise we'd leak chain-of-thought as output.
        needs_reasoning_fallback = (
            ("moonshot" in url and "thinking" in model_lower)
            or ("z.ai" in url)
            or ("deepseek" in url and "r1" in model_lower)
        )
        has_tool_calls = hasattr(message, "tool_calls") and message.tool_calls

        def _extract_text_from_message(msg) -> str:
            try:
                content = getattr(msg, "content", None)
                if isinstance(content, str) and content.strip():
                    return content
                if isinstance(content, list):
                    parts: List[str] = []
                    for part in content:
                        try:
                            text = getattr(part, "text", None)
                            if not text and isinstance(part, dict):
                                text = part.get("text")
                            if isinstance(text, str) and text.strip():
                                parts.append(text.strip())
                        except Exception:
                            pass
                    if parts:
                        return "\n\n".join(parts).strip()
                if hasattr(content, "text"):
                    txt = getattr(content, "text", "")
                    if txt:
                        return txt
                # Fallback: only for known reasoning models without tool_calls,
                # to avoid leaking internal chain-of-thought as response text.
                if needs_reasoning_fallback and not has_tool_calls:
                    for alt_field in ["reasoning_content", "output", "thinking"]:
                        alt_value = getattr(msg, alt_field, None)
                        if isinstance(alt_value, str) and alt_value.strip():
                            return alt_value
            except Exception:
                pass
            return ""

        content_text = _extract_text_from_message(message)

        # Handle token usage (some providers might not return this)
        prompt_tokens = 0
        completion_tokens = 0
        total_tokens = 0
        cache_read_tokens = 0
        if hasattr(response, "usage") and response.usage:
            try:
                prompt_tokens = int(getattr(response.usage, "prompt_tokens", 0))
                completion_tokens = int(getattr(response.usage, "completion_tokens", 0))
                total_tokens = int(
                    getattr(
                        response.usage,
                        "total_tokens",
                        prompt_tokens + completion_tokens,
                    )
                )
                # Kimi returns cached tokens in usage.cached_tokens (OpenAI format)
                cache_read_tokens = int(getattr(response.usage, "cached_tokens", 0) or 0)
                # Also check prompt_tokens_details.cached_tokens (OpenAI standard)
                if cache_read_tokens == 0:
                    ptd = getattr(response.usage, "prompt_tokens_details", None)
                    if ptd:
                        cache_read_tokens = int(getattr(ptd, "cached_tokens", 0) or 0)
            except Exception:
                prompt_tokens = 0
                completion_tokens = 0
                total_tokens = 0
                cache_read_tokens = 0
        if total_tokens == 0:
            # Estimate if not provided
            prompt_tokens = self.count_tokens(request.messages, model)
            completion_tokens = self.count_tokens(
                [{"role": "assistant", "content": content_text}], model
            )
            total_tokens = prompt_tokens + completion_tokens

        if cache_read_tokens > 0:
            logger.info(f"OpenAI-compatible prompt cache: read={cache_read_tokens}, input={prompt_tokens}, model={model}")

        # Calculate cost using alias for proper lookup
        cost = self.estimate_cost(prompt_tokens, completion_tokens, model_alias, cache_read_tokens=cache_read_tokens)

        # Build response
        return CompletionResponse(
            content=content_text,
            model=model,
            provider=self.config.get("name", "openai_compatible"),
            usage=TokenUsage(
                input_tokens=prompt_tokens,
                output_tokens=completion_tokens,
                total_tokens=total_tokens,
                estimated_cost=cost,
                cache_read_tokens=cache_read_tokens,
            ),
            finish_reason=choice.finish_reason
            if hasattr(choice, "finish_reason")
            else "stop",
            function_call=message.function_call
            if hasattr(message, "function_call") and message.function_call
            else (
                {"name": message.tool_calls[0].function.name, "arguments": message.tool_calls[0].function.arguments}
                if hasattr(message, "tool_calls") and message.tool_calls and message.tool_calls[0].function
                else None
            ),
            request_id=response.id if hasattr(response, "id") else None,
            latency_ms=latency_ms,
        )

    async def stream_complete(self, request: CompletionRequest) -> AsyncIterator[str]:
        """Stream a completion using the OpenAI-compatible API"""

        # Select model based on tier or explicit override
        model_config = self.resolve_model_config(request)
        model = model_config.model_id

        # Prepare API request (translate Anthropic-style image blocks to OpenAI format)
        messages = prepare_openai_messages(request.messages)
        api_request = {
            "model": model,
            "messages": messages,
            "temperature": request.temperature,
            "stream": True,
            "stream_options": {"include_usage": True},  # Request usage statistics
        }

        if request.max_tokens:
            api_request["max_tokens"] = request.max_tokens

        # Provider-specific reasoning mode handling
        self._apply_provider_defaults(api_request)

        # Kimi: inject prompt_cache_key for session-based cache routing (streaming path)
        if "moonshot" in self.base_url.lower() and request.session_id:
            api_request.setdefault("extra_body", {})["prompt_cache_key"] = request.session_id

        # Make streaming API call
        try:
            stream = await self.client.chat.completions.create(**api_request)

            # Reasoning model fallback: when content is empty, check
            # provider-specific reasoning fields for the actual output.
            # - Kimi kimi-k2-thinking: uses reasoning_content field
            # Note: MiniMax now uses Anthropic provider, not routed here.
            url = self.base_url.lower()
            model = api_request.get("model", "")
            is_kimi_thinking = "moonshot" in url and "thinking" in model

            async for chunk in stream:
                if not chunk.choices:
                    continue
                delta = chunk.choices[0].delta
                text = delta.content
                if not text and is_kimi_thinking:
                    text = getattr(delta, "reasoning_content", None)
                if text:
                    yield text

                # Check for usage in the chunk (usually the last one)
                if chunk.usage:
                    yield {
                        "usage": {
                            "total_tokens": chunk.usage.total_tokens,
                            "input_tokens": chunk.usage.prompt_tokens,
                            "output_tokens": chunk.usage.completion_tokens,
                        },
                        "model": chunk.model,
                        "provider": "openai_compatible",
                    }

        except Exception as e:
            raise Exception(f"OpenAI-compatible streaming error ({self.base_url}): {e}")
