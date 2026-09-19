"""=============================================================================
文件: python/llm-service/llm_service/api/mcp_mock.py
-------------------------------------------------------------------------------
【一句话功能】 提供用于冒烟测试的简单 MCP 模拟接口
【关键内容】 使用 FastAPI 路由器导出 POST /mcp/mock 端点
             目前仅支持 echo 功能，用于验证 MCP 协议集成
【协作关系】 被冒烟测试脚本调用；模拟 MCP 协议的最小功能子集
============================================================================="""
from fastapi import APIRouter, HTTPException
from pydantic import BaseModel, Field
from typing import Any, Dict, Optional

router = APIRouter(prefix="/mcp", tags=["mcp-mock"])


class MCPInvokeRequest(BaseModel):
    """代表对 MCP 模拟器的请求体模型。

    字段说明：
    - function: 要调用的函数名称（字符串）。路由实现会忽略大小写并支持 'echo'。
    - args: 可选的任意键值对载荷，传递给被调用的函数作为参数。

    使用 Pydantic 的模型可以自动做输入校验并整合到 FastAPI 的 OpenAPI 文档中。
    """

    function: str = Field(..., description="要调用的模拟函数名，当前仅支持 'echo'。")
    args: Optional[Dict[str, Any]] = Field(
        None, description="传递给模拟函数的可选参数对象（任意 JSON 可序列化结构）。"
    )


@router.post("/mock")
async def mcp_mock(req: MCPInvokeRequest) -> Dict[str, Any]:
    """MCP mock endpoint.

    行为：
    - 接受一个 JSON 请求体，解析为 `MCPInvokeRequest`。
    - 如果 `function` 字段为 'echo'（不区分大小写），则返回一个包含原始 `args`
      的响应，响应格式为 `{"ok": True, "function": <name>, "echo": <args>}`。
    - 对于未知的 `function`，抛出 `HTTPException(status_code=400)` 表示客户端错误。

    实现细节与语法说明：
    - 路由通过 `@router.post("/mock")` 装饰器注册到 `router`，并在应用中通过
      `include_router(router)` 引入。
    - 函数是异步的（`async def`），以便在未来扩展时可与异步 I/O（例如调用外部服务）无缝集成。
    - 使用 `req.function.lower()` 做大小写无关比较，保证调用更宽容。
    - 返回值类型标注为 `Dict[str, Any]`，帮助类型检查和编辑器补全。
    """

    # 仅支持 echo 用例：直接把传入的 args 作为 echo 字段返回，空时返回空对象 {}
    if req.function.lower() == "echo":
        return {"ok": True, "function": req.function, "echo": req.args or {}}

    # 对于未识别的函数名，返回 400 Bad Request，携带错误详情
    raise HTTPException(status_code=400, detail="Unknown function")
