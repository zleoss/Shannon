# Multimodal Messages — Frontend Integration Guide

## 📖 中文学习注解

### 本文核心摘要
本文档说明 Shannon 后端对多模态消息的支持——允许在聊天消息中发送文件附件（图片/PDF/文本文件），并通过 Tasks API 或 OpenAI 兼容 API 两种方式提交。附件以 base64 编码通过 context.attachments 传递，后端存 Redis 后按不同类型注入 LLM 消息（图片→image content block、PDF→document content block、文本→直接注入）。文档包含前端 UI 实现建议、文件大小限制和错误处理说明。

### 章节导航
- **Supported File Types**: 图片（PNG/JPEG/GIF/WebP 最大 20MB）、PDF（20MB）、文本文件（20MB）
- **API Protocol - Tasks API**: 通过 POST /api/v1/tasks/stream 提交，attachments 含 media_type/data/filename
- **API Protocol - OpenAI Compatible**: 通过 /v1/chat/completions 提交，遵循 OpenAI 多模态消息格式
- **Frontend UI Requirements**: 文件选择器 UI、预览、拖放上传、上传进度和错误状态
- **Error Handling**: 文件大小超限、类型不支持、base64 解码失败等情况处理
- **Streaming vs Non-Streaming**: 流式模式下附件事件的处理

### 与 AI Agent 体系的关联
- 附件接收处理：`python/llm-service/llm_service/api/agent.py` 中的 attachments 解析
- 附件存储：Redis（键值对，临时存储）
- 注入 LLM 消息：由 provider 适配器将附件转换为对应格式的 content block
- 前端可参考 OpenAI 兼容 API 的 SDK 实现

### 阅读建议
前端开发者必读；后端开发者了解 API 协议即可；重点关注文件类型支持和 attachment 数据格式。

> Frontend target: any Shannon API client

## Overview

Shannon 后端现已支持在聊天消息中发送文件附件（图片、PDF、文本文件等）。本文档描述前端需要对接的 API 协议和 UI 需求。

---

## 支持的文件类型

| 类别 | MIME Types | 最大单文件 | 后端处理方式 |
|------|-----------|----------|------------|
| **图片** | `image/png`, `image/jpeg`, `image/gif`, `image/webp` | 20MB decoded | 存 Redis → 作为 image content block 发给 LLM |
| **PDF** | `application/pdf` | 20MB decoded | 存 Redis → 作为 document content block 发给 LLM |
| **文本文件** | `text/plain`, `text/markdown`, `text/csv`, `text/html`, `text/yaml`, `application/json`, `application/xml`, 代码文件等 | 20MB decoded | 存 Redis → decode 为文本 → 注入 LLM 消息 |

**限制：**
- HTTP body 最大 30MB（base64 编码后约等于 20MB 原始数据）
- 单次请求所有附件 decoded 总和不超过 20MB
- 前端建议限制：单文件 10MB，最多 5 个附件

---

## API 协议

### 方式 1：Tasks API（推荐，通用客户端路径）

**`POST /api/v1/tasks/stream`**

附件通过 `context.attachments` 发送，每个附件是 base64 编码的对象：

```json
{
  "query": "分析这张图片和这个 CSV 文件",
  "session_id": "session-uuid",
  "context": {
    "attachments": [
      {
        "media_type": "image/png",
        "data": "<base64-encoded-content>",
        "filename": "screenshot.png"
      },
      {
        "media_type": "text/csv",
        "data": "<base64-encoded-content>",
        "filename": "sales-data.csv"
      }
    ]
  }
}
```

**字段说明：**

| 字段 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `media_type` | string | ✅ | MIME type，如 `image/png`、`application/pdf`、`text/csv` |
| `data` | string | ✅ | 文件内容的 base64 编码（不含 `data:` 前缀） |
| `filename` | string | 推荐 | 文件名，用于在对话历史中显示 |

**响应格式：** 与现有 tasks/stream 一致，不变。

### 方式 2：Chat Completions API（OpenAI 兼容格式）

**`POST /v1/chat/completions`**

使用 OpenAI 标准的 content array 格式：

```json
{
  "model": "shannon-chat",
  "messages": [
    {
      "role": "user",
      "content": [
        { "type": "text", "text": "描述这张图片" },
        {
          "type": "image_url",
          "image_url": {
            "url": "data:image/png;base64,<base64-content>"
          }
        }
      ]
    }
  ]
}
```

**支持的 content block 类型：**

| type | 用途 | 格式 |
|------|------|------|
| `text` | 文本内容 | `{ "type": "text", "text": "..." }` |
| `image_url` | 图片（base64 或 URL） | `{ "type": "image_url", "image_url": { "url": "data:image/png;base64,..." } }` |
| `file` | PDF/文档 | `{ "type": "file", "file": { "file_data": "data:application/pdf;base64,...", "filename": "doc.pdf" } }` |

---

## 前端实现清单

### 1. 文件选择与验证

```typescript
const ALLOWED_TYPES = [
  // 图片
  'image/png', 'image/jpeg', 'image/gif', 'image/webp',
  // 文档
  'application/pdf',
  // 文本
  'text/plain', 'text/markdown', 'text/csv', 'text/html',
  'application/json', 'text/xml', 'application/xml',
  // 代码（可选扩展）
  'text/javascript', 'text/x-python', 'text/yaml',
];

const MAX_FILE_SIZE = 10 * 1024 * 1024; // 10MB per file
const MAX_ATTACHMENTS = 5;
const MAX_TOTAL_SIZE = 20 * 1024 * 1024; // 20MB total
```

### 2. 文件转 base64

```typescript
function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      const result = reader.result as string;
      // 去掉 "data:...;base64," 前缀，只要纯 base64
      resolve(result.split(',')[1]);
    };
    reader.onerror = reject;
    reader.readAsDataURL(file);
  });
}
```

### 3. 构建请求（Tasks API 路径）

```typescript
interface AttachmentPayload {
  media_type: string;
  data: string;       // 纯 base64，不含前缀
  filename: string;
}

async function submitWithAttachments(
  query: string,
  sessionId: string,
  files: File[],
  context?: Record<string, any>
) {
  const attachments: AttachmentPayload[] = await Promise.all(
    files.map(async (file) => ({
      media_type: file.type || 'application/octet-stream',
      data: await fileToBase64(file),
      filename: file.name,
    }))
  );

  return submitTask({
    query,
    session_id: sessionId,
    context: {
      ...context,
      attachments,
    },
  });
}
```

### 4. 输入方式

前端应支持三种文件输入方式：

| 方式 | 实现 |
|------|------|
| **按钮选择** | `<input type="file" multiple accept="image/*,.pdf,.csv,.json,.md,.txt">` |
| **拖拽上传** | `onDrop` + `onDragOver` on the chat input area |
| **粘贴** | `onPaste` event — 检测 `clipboardData.items` 中的图片 |

### 5. 附件预览

| 文件类型 | 预览方式 |
|---------|---------|
| 图片 | 缩略图 `<img>` + 文件大小 |
| PDF | 文件图标 + 文件名 + 大小 |
| 文本文件 | 文件图标 + 文件名 + 大小 |

```tsx
// 图片预览用 Object URL（发送后 revoke）
const preview = file.type.startsWith('image/')
  ? URL.createObjectURL(file)
  : null; // 非图片显示文件图标
```

### 6. 用户消息中显示附件

提交后，在对话气泡中渲染附件：

```typescript
interface MessageAttachment {
  mediaType: string;
  preview: string;      // data URL (图片) 或 filename (其他)
  filename?: string;
  sizeBytes?: number;
}
```

- **图片**：`<img src={dataUrl}>` 可点击放大
- **PDF/文本**：文件图标 + 文件名 + 大小 badge

### 7. 对话历史中的附件感知

后端在 session history 中自动注入 `[Attached: filename (media_type)]` 到消息内容中。前端**不需要**在历史加载时做特殊处理——附件信息已经在 `content` 字符串里。

如果前端想在历史消息中也渲染附件缩略图，需要在提交时将附件 preview 存入 Redux state（因为原始 base64 数据不会从后端返回）。

---

## SSE 事件

附件不会产生新的 SSE 事件类型。现有事件不变：

- `WORKFLOW_STARTED` — `payload.task_context.attachments` 包含附件引用（id, media_type, filename, size_bytes）
- `LLM_OUTPUT` / `WORKFLOW_COMPLETED` — 响应内容中会包含对附件的分析结果

---

## 错误处理

| 场景 | HTTP Status | 错误信息 |
|------|------------|---------|
| 附件 base64 解码失败 | 400 | `Attachment base64 decode failed: ...` |
| 单次附件总大小超 20MB | 400 | `total attachment size X bytes exceeds 20971520 byte limit` |
| HTTP body 超 30MB | 400 | Request Entity Too Large |
| 不支持的 content block type | — | 静默忽略（不报错） |

前端建议在客户端做预检：
```typescript
const totalSize = files.reduce((sum, f) => sum + f.size, 0);
if (totalSize > 20 * 1024 * 1024) {
  toast.error('附件总大小不能超过 20MB');
  return;
}
```

---

## 已验证的工作流

所有文件类型在以下工作流中均已 E2E 验证：

| 工作流 | 图片 | PDF | JSON/CSV/MD |
|--------|------|-----|-------------|
| SimpleTask | ✅ | ✅ | ✅ |
| DAG (multi-agent) | ✅ | ✅ | ✅ |
| Swarm (multi-agent) | ✅ | ✅ | ✅ |
| Chat Completions | ✅ | ✅ | ✅ |
| 多轮对话 | ✅ | ✅ | ✅ |
| 多文件混合 | ✅ | ✅ | ✅ |

---

## Quick Start 示例

### 最简单的图片分析

```bash
# 1x1 red pixel PNG
B64="iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="

curl -X POST http://localhost:8080/api/v1/tasks/stream \
  -H "Content-Type: application/json" \
  -d "{
    \"query\": \"What color is this image?\",
    \"session_id\": \"test-1\",
    \"context\": {
      \"attachments\": [{
        \"media_type\": \"image/png\",
        \"data\": \"${B64}\",
        \"filename\": \"pixel.png\"
      }]
    }
  }"
```

### CSV 数据分析

```typescript
// Frontend code
const file = fileInput.files[0]; // user selected a .csv
const base64 = await fileToBase64(file);

await submitTask({
  query: "分析这个 CSV，统计每个区域的总销售额",
  session_id: currentSessionId,
  context: {
    attachments: [{
      media_type: "text/csv",
      data: base64,
      filename: file.name,
    }],
  },
});
```
