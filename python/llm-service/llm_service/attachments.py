"""=============================================================================
文件: python/llm-service/llm_service/attachments.py
-------------------------------------------------------------------------------
【一句话功能】
  附件解析器：从 Redis 读取附件并转换为各 provider 的内容块格式。
【关键内容】
  AttachmentResolver :18 / :41
  Redis key 形如 shannon:att:{id}，TTL 1800s
  支持 image/pdf/text 多种 MIME 类型
【协作关系】
  被 api/agent.py 在构建消息时调用；依赖 Redis（config 中配置）。
=============================================================================
-------------------------------------------------------------------------------
【原 docstring】
  Attachment resolver: reads attachments from Redis, converts to provider-specific
  content block formats (OpenAI, Anthropic, Gemini).

  Supported file categories:
  - Binary vision: image/png, image/jpeg, image/gif, image/webp → native image blocks
  - Binary document: application/pdf → native document blocks
  - Text files: text/*, application/json, application/xml → decoded to text, injected inline
=============================================================================
"""
import asyncio
import base64
import json
import logging
from typing import Optional

logger = logging.getLogger(__name__)

ATTACHMENT_TTL = 1800  # 30 minutes

# MIME types that should be decoded to text instead of passed as binary.
# These are readable by ALL providers without special content block support.
TEXT_MIME_TYPES = {
    "text/plain", "text/markdown", "text/csv", "text/html", "text/xml",
    "text/x-python", "text/x-java", "text/x-c", "text/x-go",
    "text/javascript", "text/css", "text/yaml", "text/x-toml",
    "application/json", "application/xml", "application/x-yaml",
    "application/javascript", "application/typescript",
}


def is_text_mime(media_type: str) -> bool:
    """Check if a MIME type represents a text-decodable file."""
    if media_type in TEXT_MIME_TYPES:
        return True
    # Catch-all: any text/* subtype
    if media_type.startswith("text/"):
        return True
    return False


class AttachmentResolver:
    def __init__(self, redis_client):
        self.redis = redis_client

    async def resolve(self, attachment_id: str, session_id: str = "") -> Optional[dict]:
        """Resolve an attachment ID to its data from Redis.

        When session_id is provided, validates it against the stored record
        to prevent cross-session attachment access.
        """
        key = f"shannon:att:{attachment_id}"
        raw = await self.redis.get(key)
        if raw is None:
            logger.warning(f"Attachment {attachment_id} not found or expired")
            return None

        # Refresh TTL on access
        await self.redis.expire(key, ATTACHMENT_TTL)

        att = json.loads(raw)

        # Session isolation
        if session_id and att.get("session_id") and att["session_id"] != session_id:
            logger.warning(f"Attachment {attachment_id} session mismatch (expected {session_id})")
            return None
        media_type = att["media_type"]
        filename = att.get("filename", "")

        # Text files: decode base64 → plain text for universal provider compatibility
        if is_text_mime(media_type):
            try:
                text_content = base64.b64decode(att["data"]).decode("utf-8")
                return {
                    "id": att["id"],
                    "media_type": media_type,
                    "filename": filename,
                    "text_content": text_content,
                    "source": "text",
                }
            except (UnicodeDecodeError, Exception) as e:
                logger.warning(f"Failed to decode text attachment {attachment_id}: {e}, falling back to base64")

        # Binary files (images, PDFs): keep as base64
        return {
            "id": att["id"],
            "media_type": media_type,
            "filename": filename,
            "data": att["data"],  # base64 encoded string
            "source": "base64",
        }

    async def resolve_many(self, attachment_refs: list, session_id: str = "") -> list:
        """Resolve a list of attachment references in parallel."""
        # Separate Redis-backed refs (need async I/O) from URL refs (instant).
        url_results = []
        coros = []
        order: list[tuple[int, str]] = []  # (original_index, "id"|"url")

        for i, ref in enumerate(attachment_refs):
            if not isinstance(ref, dict):
                logger.warning(f"Skipping non-dict attachment ref at index {i}: {type(ref).__name__}")
                continue
            if "id" in ref:
                coros.append(self.resolve(ref["id"], session_id=session_id))
                order.append((i, "id"))
            elif "url" in ref:
                url_results.append({
                    "url": ref["url"],
                    "media_type": ref.get("media_type", "application/octet-stream"),
                    "source": "url",
                })
                order.append((i, "url"))

        # Resolve all Redis lookups concurrently.
        resolved = await asyncio.gather(*coros) if coros else []

        # Merge results preserving original order.
        results = []
        id_iter = iter(resolved)
        url_iter = iter(url_results)
        for _, kind in order:
            if kind == "id":
                att = next(id_iter)
                if att is not None:
                    results.append(att)
            else:
                results.append(next(url_iter))
        return results

    def to_anthropic_block(self, att: dict) -> dict:
        """Convert attachment to Anthropic content block."""
        media_type = att["media_type"]
        if media_type == "application/pdf":
            return {
                "type": "document",
                "source": {
                    "type": "base64",
                    "media_type": media_type,
                    "data": att["data"],
                },
            }
        elif is_text_mime(media_type):
            # Anthropic supports text/plain as document block natively
            return {
                "type": "document",
                "source": {
                    "type": "base64",
                    "media_type": "text/plain",
                    "data": att["data"],
                },
            }
        else:
            return {
                "type": "image",
                "source": {
                    "type": "base64",
                    "media_type": media_type,
                    "data": att["data"],
                },
            }

    def to_openai_block(self, att: dict) -> dict:
        """Convert attachment to OpenAI content block."""
        media_type = att["media_type"]
        data_uri = f"data:{media_type};base64,{att['data']}"
        if media_type == "application/pdf":
            return {
                "type": "file",
                "file": {
                    "filename": att.get("filename", "document.pdf"),
                    "file_data": data_uri,
                },
            }
        else:
            return {
                "type": "image_url",
                "image_url": {"url": data_uri},
            }

    def to_gemini_block(self, att: dict) -> dict:
        """Convert attachment to Gemini inline_data block."""
        return {
            "inlineData": {
                "mimeType": att["media_type"],
                "data": att["data"],
            }
        }

    def to_content_blocks(self, attachments: list, provider: str) -> list:
        """Convert a list of attachments to provider-specific content blocks."""
        converters = {
            "anthropic": self.to_anthropic_block,
            "openai": self.to_openai_block,
            "gemini": self.to_gemini_block,
        }
        convert = converters.get(provider, self.to_openai_block)
        return [convert(att) for att in attachments]
