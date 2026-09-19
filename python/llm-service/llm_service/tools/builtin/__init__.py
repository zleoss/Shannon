"""=============================================================================
文件: python/llm-service/llm_service/tools/builtin/__init__.py
-------------------------------------------------------------------------------
【一句话功能】
  内置工具汇总导出（web/file/calculator/bash/browser 等）。
【关键内容】
  顶层 import 与 __all__ 聚合 :5-44
【协作关系】
  被 ToolRegistry.discover_tools 自动扫描注册。
=============================================================================
-------------------------------------------------------------------------------
【原 docstring】
  Built-in tools for Shannon platform
=============================================================================
"""

from .web_search import WebSearchTool
from .web_fetch import WebFetchTool
from .web_subpage_fetch import WebSubpageFetchTool
from .web_crawl import WebCrawlTool
from .calculator import CalculatorTool
from .file_ops import FileReadTool, FileWriteTool, FileListTool, FileSearchTool, FileEditTool, FileDeleteTool
from .data_tools import DiffFilesTool, JsonQueryTool
from .python_wasi_executor import PythonWasiExecutorTool
from .bash_executor import BashExecutorTool
from .x_search import XSearchTool

# Browser automation tool
try:
    from .browser_use import BrowserTool
    _HAS_BROWSER_TOOLS = True
except ImportError:
    _HAS_BROWSER_TOOLS = False

__all__ = [
    "WebSearchTool",
    "WebFetchTool",
    "WebSubpageFetchTool",
    "WebCrawlTool",
    "CalculatorTool",
    "FileReadTool",
    "FileWriteTool",
    "FileListTool",
    "FileSearchTool",
    "FileEditTool",
    "FileDeleteTool",
    "DiffFilesTool",
    "JsonQueryTool",
    "BashExecutorTool",
    "PythonWasiExecutorTool",
    "XSearchTool",
]

# Add browser tool to exports if available
if _HAS_BROWSER_TOOLS:
    __all__.append("BrowserTool")
