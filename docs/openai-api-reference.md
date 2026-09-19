# Shannon OpenAI-Compatible API Reference

## 📖 中文学习注解

### 本文核心摘要
本文档是 Shannon OpenAI 兼容 API 的完整参考文档，允许用户使用标准 OpenAI SDK 调用 Shannon 的高级研究和聊天能力。文档说明了 Base URL、Bearer Token/X-API-Key 两种认证方式、Chat Completions 端点（支持流和工具调用）、Completions 端点（纯文本代理，不经过编排器）、Models 端点（列出可用模型）、以及 Shannon 特有的参数扩展（x_session_id/x_role 等 OpenAI 不支持的参数以 x_ 前缀注入）。

### 章节导航
- **Base URL & Authentication**: API 地址和两种认证方式
- **Chat Completions (POST /v1/chat/completions)**: 请求体参数（model/messages/stream/tools 等），支持流式和非流式
- **Completions (POST /v1/completions)**: 纯文本补全，代理模式不经过编排器
- **Models (GET /v1/models)**: 列出可用的 Shannon 模型
- **Shannon-Specific Extensions**: x_session_id/x_role/x_model_tier 等参数
- **Research Capabilities**: 通过模型名触发深度研究能力
- **Error Handling**: OpenAI 兼容错误格式

### 与 AI Agent 体系的关联
- Gateway 实现：`go/orchestrator/cmd/gateway/` 处理 HTTP 路由
- 编排器路由：通过模型名或 x_ 参数触发编排工作流
- /v1/completions 不经过编排器——适合不需要路由/分解/策略的简单调用
- /v1/chat/completions 经过编排器——支持工具、研究、策略等完整功能

### 阅读建议
使用 OpenAI SDK 接入 Shannon 的开发者必读；注意 /v1/completions 和 /v1/chat/completions 的路由差异（是否经过编排器）。

Shannon provides an OpenAI-compatible API layer that allows you to use Shannon's advanced research and chat capabilities through standard OpenAI SDKs.

## Base URL

```
https://api.shannon.run/v1
```

For local development:
```
http://localhost:8080/v1
```

## Authentication

All requests require authentication via Bearer token or API key:

```bash
# Bearer token (preferred for SDK compatibility)
Authorization: Bearer sk-shannon-your-api-key

# API key header
X-API-Key: your-api-key
```

## Endpoints

### Chat Completions

Create a chat completion using Shannon's AI models.

```
POST /v1/chat/completions
```

#### Request Body

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `model` | string | No | Model to use. Defaults to `shannon-chat`. |
| `messages` | array | Yes | Array of message objects. |
| `stream` | boolean | No | Enable SSE streaming. Default: `false`. |
| `max_tokens` | integer | No | Maximum tokens to generate. |
| `temperature` | number | No | Sampling temperature (0-2). |
| `top_p` | number | No | Nucleus sampling parameter. |
| `stop` | array | No | Stop sequences. |
| `user` | string | No | Unique user identifier for tracking. |
| `stream_options` | object | No | Streaming options. |

##### Message Object

| Field | Type | Description |
|-------|------|-------------|
| `role` | string | One of: `system`, `user`, `assistant` |
| `content` | string \| array | Message content — plain string or array of content blocks (multimodal) |

**Multimodal Content (content as array):**

When `content` is an array, it supports these block types:

| Block Type | Description | Example |
|-----------|-------------|---------|
| `text` | Text content | `{"type": "text", "text": "Describe this image"}` |
| `image_url` | Image (base64 or URL) | `{"type": "image_url", "image_url": {"url": "data:image/png;base64,..."}}` |
| `file` | Document (PDF, etc.) | `{"type": "file", "file": {"file_data": "data:application/pdf;base64,...", "filename": "doc.pdf"}}` |

Supported image types: `image/png`, `image/jpeg`, `image/gif`, `image/webp`. Supported document types: `application/pdf`. Text files (`text/*`, `application/json`) are also accepted and decoded to text.

**Size limits:** 30MB HTTP body, 20MB total decoded attachments. Plain string `content` remains fully backward compatible.

##### Stream Options

| Field | Type | Description |
|-------|------|-------------|
| `include_usage` | boolean | Include token usage in final chunk |

#### Response (Non-Streaming)

```json
{
  "id": "chatcmpl-abc123",
  "object": "chat.completion",
  "created": 1703318400,
  "model": "shannon-deep-research",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "Based on my research..."
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 50,
    "completion_tokens": 1200,
    "total_tokens": 1250
  }
}
```

#### Response (Streaming)

SSE stream with chunks in OpenAI format:

```
data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1703318400,"model":"shannon-deep-research","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1703318400,"model":"shannon-deep-research","choices":[{"index":0,"delta":{"content":"Based on"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1703318400,"model":"shannon-deep-research","choices":[{"index":0,"delta":{"content":" my research"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1703318400,"model":"shannon-deep-research","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":1200,"total_tokens":1250}}

data: [DONE]
```

#### Shannon Events Extension

Shannon extends the OpenAI streaming format with agent lifecycle events via the `shannon_events` field. This enables rich UI experiences showing agent thinking, tool usage, and progress.

##### Extended Chunk Structure

```json
{
  "id": "chatcmpl-xxx",
  "object": "chat.completion.chunk",
  "model": "shannon-deep-research",
  "choices": [{"index": 0, "delta": {}}],
  "shannon_events": [
    {
      "type": "AGENT_THINKING",
      "agent_id": "Ryogoku",
      "message": "Analyzing the query...",
      "timestamp": 1766470485,
      "payload": {"role": "generalist"}
    }
  ]
}
```

##### ShannonEvent Fields

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | Event type (see below) |
| `agent_id` | string | Agent identifier (e.g., "Ryogoku", "synthesis") |
| `message` | string | Human-readable status message |
| `timestamp` | int64 | Unix timestamp |
| `payload` | object | Additional event-specific data |

##### Event Types

| Category | Event Type | Description |
|----------|------------|-------------|
| Lifecycle | `WORKFLOW_STARTED` | Task execution begins |
| | `AGENT_STARTED` | Agent activates |
| | `AGENT_COMPLETED` | Agent finishes work |
| Thinking | `AGENT_THINKING` | Agent reasoning/planning |
| Tools | `TOOL_INVOKED` | Tool being called |
| | `TOOL_OBSERVATION` | Tool result received |
| Progress | `PROGRESS` | Step completion updates |
| | `DATA_PROCESSING` | Processing/analyzing data |
| Control | `WORKFLOW_COMPLETED` | Task finished |
| | `ERROR_OCCURRED` | Error during execution |

### List Models

List available models.

```
GET /v1/models
```

#### Response

```json
{
  "object": "list",
  "data": [
    {
      "id": "shannon-deep-research",
      "object": "model",
      "created": 1703318400,
      "owned_by": "shannon"
    },
    {
      "id": "shannon-chat",
      "object": "model",
      "created": 1703318400,
      "owned_by": "shannon"
    }
  ]
}
```

### Get Model

Get details about a specific model.

```
GET /v1/models/{model}
```

#### Response

```json
{
  "id": "shannon-deep-research",
  "object": "model",
  "created": 1703318400,
  "owned_by": "shannon"
}
```

## Available Models

| Model | Description | Use Case |
|-------|-------------|----------|
| `shannon-deep-research` | Deep research with iterative refinement | Complex research queries requiring multiple sources |
| `shannon-quick-research` | Fast research for simple queries | Quick fact-finding |
| `shannon-standard-research` | Balanced research depth and speed | General research tasks |
| `shannon-academic-research` | Academic-style with citations | Scholarly research |
| `shannon-chat` | General chat completion (default) | Conversational AI |
| `shannon-complex` | Multi-agent orchestration | Complex multi-step tasks |

## Rate Limits

Rate limits are applied per API key and per model:

| Model | Requests/min | Tokens/min |
|-------|--------------|------------|
| `shannon-deep-research` | 10 | 100,000 |
| `shannon-quick-research` | 30 | 150,000 |
| `shannon-chat` | 60 | 200,000 |
| `shannon-complex` | 15 | 100,000 |

Rate limit headers are included in all responses:

```
X-RateLimit-Limit-Requests: 60
X-RateLimit-Remaining-Requests: 59
X-RateLimit-Limit-Tokens: 200000
X-RateLimit-Remaining-Tokens: 195000
X-RateLimit-Reset-Requests: 2024-01-15T10:30:00Z
```

## Session Management

Shannon supports multi-turn conversations with session persistence.

### X-Session-ID Header

Include `X-Session-ID` header to maintain conversation context:

```bash
curl -X POST https://api.shannon.run/v1/chat/completions \
  -H "Authorization: Bearer sk-shannon-xxx" \
  -H "X-Session-ID: my-session-123" \
  -d '{"model": "shannon-chat", "messages": [...]}'
```

**Session Behavior:**
- If no session ID is provided, one is derived from the conversation content
- If a session ID is reused by a different user (collision), a new session is generated
- The response will include `X-Session-ID` header when a new session is created or collision occurred

## Error Responses

All errors follow the OpenAI error format:

```json
{
  "error": {
    "message": "Invalid model: unknown-model",
    "type": "invalid_request_error",
    "code": "model_not_found"
  }
}
```

### Error Types

| HTTP Status | Type | Code | Description |
|-------------|------|------|-------------|
| 400 | `invalid_request_error` | `invalid_request` | Malformed request |
| 401 | `authentication_error` | `invalid_api_key` | Invalid API key |
| 404 | `invalid_request_error` | `model_not_found` | Unknown model |
| 429 | `rate_limit_error` | `rate_limit_exceeded` | Rate limit hit |
| 500 | `server_error` | `internal_error` | Internal error |

## SDK Examples

### Python (OpenAI SDK)

```python
from openai import OpenAI

client = OpenAI(
    api_key="sk-shannon-your-api-key",
    base_url="https://api.shannon.run/v1"
)

# Non-streaming
response = client.chat.completions.create(
    model="shannon-deep-research",
    messages=[
        {"role": "system", "content": "You are a research assistant."},
        {"role": "user", "content": "Research AI trends in 2024"}
    ]
)
print(response.choices[0].message.content)

# Streaming
stream = client.chat.completions.create(
    model="shannon-deep-research",
    messages=[{"role": "user", "content": "Research AI trends"}],
    stream=True
)
for chunk in stream:
    if chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="")
```

### LangChain

```python
from langchain_openai import ChatOpenAI

llm = ChatOpenAI(
    model="shannon-deep-research",
    api_key="sk-shannon-your-api-key",
    base_url="https://api.shannon.run/v1"
)

response = llm.invoke("Research competitor pricing for SaaS tools")
print(response.content)
```

### cURL

```bash
# Non-streaming
curl -X POST https://api.shannon.run/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-shannon-your-api-key" \
  -d '{
    "model": "shannon-deep-research",
    "messages": [
      {"role": "user", "content": "Research AI trends in 2024"}
    ]
  }'

# Streaming
curl -X POST https://api.shannon.run/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-shannon-your-api-key" \
  -d '{
    "model": "shannon-deep-research",
    "messages": [
      {"role": "user", "content": "Research AI trends in 2024"}
    ],
    "stream": true
  }'
```

### Node.js

```javascript
import OpenAI from 'openai';

const client = new OpenAI({
  apiKey: 'sk-shannon-your-api-key',
  baseURL: 'https://api.shannon.run/v1'
});

async function main() {
  const completion = await client.chat.completions.create({
    model: 'shannon-deep-research',
    messages: [{ role: 'user', content: 'Research AI trends' }]
  });
  console.log(completion.choices[0].message.content);
}

main();
```

## Best Practices

1. **Use streaming for long responses**: Research queries can produce lengthy responses. Streaming provides better user experience.

2. **Set appropriate timeouts**: Research models may take longer to respond (up to 5 minutes for deep research).

3. **Include system prompts**: Guide the model's behavior with clear system prompts.

4. **Use session IDs for multi-turn**: Maintain conversation context by providing consistent session IDs.

5. **Handle rate limits gracefully**: Check `Retry-After` header and implement exponential backoff.

## Differences from OpenAI API

| Feature | Shannon | OpenAI |
|---------|---------|--------|
| Function calling | Not supported (Phase 4) | Supported |
| Vision/Images | Not supported | Supported |
| Embeddings | Not supported (Phase 4) | Supported |
| Fine-tuning | Not supported | Supported |
| Session persistence | Built-in | Manual |

## Changelog

- **2024-01-15**: Initial release with Phase 1 & 2 features
  - Chat completions (streaming & non-streaming)
  - Model listing
  - Rate limiting
  - Session management
