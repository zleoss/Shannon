"""=============================================================================
文件: python/playwright-service/app.py
-------------------------------------------------------------------------------
【一句话功能】
  Playwright 截图与浏览器自动化 FastAPI 服务入口。
【关键内容】
  lifespan :499；app :536
  _get_proxy_browser :77 / _do_capture :549
  POPUP_DETECTION_JS :144 / DETECT_SCROLL_HIJACK_JS :233
  FixedWindowRateLimiter :455 / _rate_limit :490
  GET /health :543 / POST /capture :721 / POST /capture/sections :755
  POST /browser/action :1129 / POST /browser/close :1293
  GET /browser/sessions :1303
【协作关系】
  被 browser_use 工具经 HTTP 调用；内部使用 session_manager 与 security。
=============================================================================
-------------------------------------------------------------------------------
【原 docstring】
  Playwright Screenshot Service

  A FastAPI service for capturing LP screenshots with popup dismissal.
  Primary use: Shannon ads research LP analysis.

  Extended with browser session management for general browser automation.
=============================================================================
"""

import asyncio
import base64
import logging
import os
import time
from contextlib import asynccontextmanager
from enum import Enum
from typing import Any, Dict, List, Optional, Tuple

from fastapi import Depends, FastAPI, HTTPException, Request
from playwright.async_api import async_playwright, Browser, TimeoutError as PlaywrightTimeout
from playwright_stealth import Stealth
from pydantic import BaseModel, HttpUrl, Field

from security import validate_url_for_ssrf
from session_manager import BrowserSessionManager

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

# Stealth mode: patches navigator.webdriver, plugins, chrome.runtime, etc.
STEALTH_ENABLED = os.getenv("STEALTH_ENABLED", "1").strip().lower() in ("1", "true", "yes")
_stealth = Stealth() if STEALTH_ENABLED else None

# Proxy fallback for WAF-blocked sites (e.g., Akamai)
SCRAPER_API_KEY = os.getenv("SCRAPER_API_KEY", "")
PROXY_FALLBACK_ENABLED = bool(SCRAPER_API_KEY)

# Page titles that indicate WAF/bot blocking
WAF_BLOCK_TITLES = {"access denied", "403 forbidden", "attention required", "just a moment"}

# Proxy requests need longer timeout (residential proxy adds latency)
PROXY_TIMEOUT_MS = int(os.getenv("PROXY_TIMEOUT_MS", "60000"))

# Configuration (read from environment variables)
MAX_CONCURRENT_BROWSERS = int(os.getenv("MAX_CONCURRENT_BROWSERS", "2"))
MAX_CONCURRENT_ACTIONS = int(
    os.getenv("MAX_CONCURRENT_ACTIONS", str(MAX_CONCURRENT_BROWSERS * 5))
)
REQUEST_TIMEOUT_MS = int(os.getenv("REQUEST_TIMEOUT_MS", "30000"))
PLAYWRIGHT_RATE_LIMIT_PER_MINUTE = int(os.getenv("PLAYWRIGHT_RATE_LIMIT_PER_MINUTE", "0"))
ENABLE_BROWSER_EVALUATE = os.getenv("ENABLE_BROWSER_EVALUATE", "false").strip().lower() in (
    "1",
    "true",
    "yes",
)
VIEWPORT_WIDTH = 1280
VIEWPORT_HEIGHT = 800
DEFAULT_USER_AGENT = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"


async def _apply_stealth(context):
    """Apply stealth patches to a browser context if enabled."""
    if _stealth:
        await _stealth.apply_stealth_async(context)


def _is_waf_blocked(title: str) -> bool:
    """Check if page title indicates WAF/bot blocking."""
    return title.strip().lower() in WAF_BLOCK_TITLES if title else False


# Lazy-initialized proxy browser (only created on first WAF block)
_proxy_browser: Browser = None
_proxy_browser_lock = asyncio.Lock()


async def _get_proxy_browser() -> Optional[Browser]:
    """Get or create a proxy-enabled browser for WAF fallback."""
    global _proxy_browser
    if not PROXY_FALLBACK_ENABLED:
        return None
    async with _proxy_browser_lock:
        if _proxy_browser and _proxy_browser.is_connected():
            return _proxy_browser
        logger.info("Launching proxy browser (ScraperAPI)")
        # ScraperAPI proxy: headless_browser_mode optimizes for Playwright/Chromium
        _proxy_browser = await _playwright.chromium.launch(
            headless=True,
            proxy={
                "server": "http://proxy-server.scraperapi.com:8001",
                "username": "scraperapi.country_code=jp.device_type=desktop",
                "password": SCRAPER_API_KEY,
            },
            args=[
                "--no-sandbox",
                "--disable-setuid-sandbox",
                "--disable-dev-shm-usage",
                "--disable-gpu",
                "--ignore-certificate-errors",
            ]
        )
        return _proxy_browser


# Concurrency control
_semaphore: asyncio.Semaphore = None
_action_semaphore: asyncio.Semaphore = None
_browser: Browser = None
_playwright = None
_session_manager: BrowserSessionManager = None


# Popup dismissal selectors (click-based)
POPUP_SELECTORS = [
    # Cookie consent buttons
    "#onetrust-accept-btn-handler",
    ".cc-btn.cc-allow",
    "[class*='cookie'] button[class*='accept']",
    "button[id*='cookie'][id*='accept']",

    # Japanese patterns
    "button:has-text('同意')",
    "button:has-text('同意する')",
    "button:has-text('閉じる')",
    "[aria-label='閉じる']",
    "button:has-text('OK')",
    "button:has-text('はい')",

    # Generic close buttons
    "[aria-label='Close']",
    "[aria-label='close']",
    ".modal-close",
    "button.close",
    "[class*='modal'] [class*='close']",
    "[class*='popup'] [class*='close']",
    "[class*='overlay'] button[class*='close']",

    # Newsletter/promo popups
    "[class*='newsletter'] button[class*='close']",
    "[class*='promo'] button[class*='close']",
]

# JavaScript to detect if popup/modal is visible
POPUP_DETECTION_JS = """
() => {
    const selectors = [
        '[class*="popup"]',
        '[class*="modal"]',
        '[class*="overlay"]',
        '[class*="cookie"]',
        '[class*="consent"]',
        '[aria-modal="true"]',
        '[role="dialog"]',
    ];

    for (const sel of selectors) {
        const elements = document.querySelectorAll(sel);
        for (const el of elements) {
            if (el.tagName === 'BODY' || el.tagName === 'HTML') continue;

            const style = getComputedStyle(el);
            const rect = el.getBoundingClientRect();

            // Check if element is visible and covers significant area
            const isVisible = style.display !== 'none' &&
                              style.visibility !== 'hidden' &&
                              style.opacity !== '0' &&
                              rect.width > 0 && rect.height > 0;

            const coversViewport = rect.width > window.innerWidth * 0.3 ||
                                   rect.height > window.innerHeight * 0.3;

            const isFixed = style.position === 'fixed' || style.position === 'absolute';
            const hasHighZ = parseInt(style.zIndex) > 100 || style.zIndex === 'auto';

            if (isVisible && (isFixed || hasHighZ) && coversViewport) {
                return true;
            }
        }
    }
    return false;
}
"""

# JavaScript to remove popup elements from DOM
POPUP_REMOVAL_JS = """
() => {
    const selectors = [
        '[class*="popup"]',
        '[class*="modal"]',
        '[class*="overlay"]',
        '[class*="cookie"]',
        '[class*="consent"]',
        '[aria-modal="true"]',
        '[role="dialog"]',
    ];

    let removed = 0;
    selectors.forEach(sel => {
        document.querySelectorAll(sel).forEach(el => {
            // Skip if it's the main content
            if (el.tagName === 'BODY' || el.tagName === 'HTML') return;
            // Check if element covers significant viewport
            const rect = el.getBoundingClientRect();
            const coversViewport = rect.width > window.innerWidth * 0.5 ||
                                   rect.height > window.innerHeight * 0.5;
            const isFixed = getComputedStyle(el).position === 'fixed';
            const hasHighZ = parseInt(getComputedStyle(el).zIndex) > 100;

            if ((isFixed || hasHighZ) && coversViewport) {
                el.remove();
                removed++;
            }
        });
    });

    // Reset overflow only if popups set it to hidden.
    // IMPORTANT: Setting both body AND html overflow to 'auto' simultaneously
    // triggers a Chromium compositor bug that blanks below-fold content in
    // full-page screenshots. Only reset the one that was actually changed.
    const bodyOF = getComputedStyle(document.body).overflow;
    const htmlOF = getComputedStyle(document.documentElement).overflow;
    if (bodyOF === 'hidden') document.body.style.overflow = '';
    if (htmlOF === 'hidden') document.documentElement.style.overflow = '';

    return removed;
}
"""


# JavaScript to detect scroll-hijacking containers (Swiper, fullPage.js, etc.)
# Returns slide count if detected, so Python can do slide-by-slide capture.
DETECT_SCROLL_HIJACK_JS = """
() => {
    const html = document.documentElement;

    // Detect pages where content is trapped in slides (scrollHeight ~= viewport height)
    const scrollTrapped = html.scrollHeight <= window.innerHeight + 50;
    if (!scrollTrapped) return null;

    // Check for vertical fullscreen Swiper (not small carousels)
    const swiperEl = document.querySelector('.swiper');
    if (swiperEl && swiperEl.swiper) {
        const swiper = swiperEl.swiper;
        // Only handle vertical fullscreen swipers (direction=vertical and multiple slides)
        const isVertical = (swiper.params?.direction || swiper.options?.direction) === 'vertical';
        const isFullscreen = swiperEl.offsetHeight >= window.innerHeight * 0.8;
        if (isVertical && isFullscreen && swiper.slides.length > 1) {
            const allIndices = [];
            for (let i = 0; i < swiper.slides.length; i++) {
                if (!swiper.slides[i].classList.contains('swiper-slide-duplicate')) {
                    allIndices.push(i);
                }
            }
            return {type: 'swiper', totalSlides: swiper.slides.length, contentSlides: allIndices};
        }
    }

    // Check for fullPage.js
    const fpSections = document.querySelectorAll('.fp-section, .section[data-anchor]');
    if (fpSections.length > 1) {
        const indices = [];
        for (let i = 0; i < fpSections.length; i++) indices.push(i);
        return {type: 'fullpage', totalSlides: fpSections.length, contentSlides: indices};
    }

    return null;
}
"""

# Navigate to a specific slide index in a scroll-hijacking container
NAVIGATE_TO_SLIDE_JS = """
(slideIndex) => {
    // Swiper
    const swiperEl = document.querySelector('.swiper');
    if (swiperEl && swiperEl.swiper) {
        swiperEl.swiper.slideTo(slideIndex, 0);  // instant transition
        return true;
    }

    // fullPage.js
    if (window.fullpage_api) {
        window.fullpage_api.silentMoveTo(slideIndex + 1);
        return true;
    }

    // Generic: scroll section into view
    const sections = document.querySelectorAll('.fp-section, .section[data-anchor]');
    if (sections[slideIndex]) {
        sections[slideIndex].scrollIntoView();
        return true;
    }

    return false;
}
"""

# Force all lazy-loaded images to eager and trigger load via incremental scroll.
# Handles: native loading="lazy", IntersectionObserver-based JS lazy loaders,
# and scroll-triggered CSS animations (opacity:0 until scroll).
FORCE_LAZY_LOAD_JS = """
() => {
    // Force native lazy images to eager and re-trigger load
    const lazyImgs = document.querySelectorAll('img[loading="lazy"]');
    lazyImgs.forEach(img => {
        img.loading = 'eager';
        // Reset src to force browser to re-evaluate loading
        const src = img.src;
        if (src) { img.src = ''; img.src = src; }
    });
    return lazyImgs.length;
}
"""

# Check how many images are still not loaded
CHECK_IMAGES_LOADED_JS = """
() => {
    const imgs = document.querySelectorAll('img');
    let notLoaded = 0;
    imgs.forEach(img => {
        if (img.offsetWidth > 0 && img.offsetHeight > 0 && (!img.complete || img.naturalWidth === 0)) {
            notLoaded++;
        }
    });
    return { total: imgs.length, notLoaded };
}
"""


# Check if the currently active slide has visible content (for lazy-loaded slides)
CHECK_ACTIVE_SLIDE_CONTENT_JS = """
() => {
    const swiperEl = document.querySelector('.swiper');
    if (swiperEl && swiperEl.swiper) {
        const active = swiperEl.swiper.slides[swiperEl.swiper.activeIndex];
        if (!active) return false;
        return active.children.length > 0 &&
               !!active.querySelector('div, img, section, p, h1, h2, h3, span, a, video, canvas');
    }
    return true;  // non-swiper: assume content exists
}
"""


async def _preload_lazy_content(page, viewport_height: int = 800) -> None:
    """Preload lazy content by forcing eager loading and scrolling incrementally.

    Handles native loading="lazy", JS IntersectionObserver lazy loaders,
    and scroll-triggered CSS animations. Should be called before full_page screenshots.
    """
    # Layer 1: Force native lazy→eager
    try:
        forced = await page.evaluate(FORCE_LAZY_LOAD_JS)
        if forced > 0:
            logger.debug(f"Forced {forced} lazy images to eager")
    except Exception as e:
        logger.debug(f"Force lazy load failed: {e}")

    # Layer 2: Incremental scroll to trigger IntersectionObserver/scroll handlers
    try:
        scroll_height = await page.evaluate("() => document.documentElement.scrollHeight")
        if scroll_height > viewport_height:
            step = viewport_height
            pos = 0
            while pos < scroll_height:
                await page.evaluate(f"() => window.scrollTo(0, {pos})")
                await page.wait_for_timeout(150)
                pos += step
            # Scroll back to top
            await page.evaluate("() => window.scrollTo(0, 0)")
            await page.wait_for_timeout(200)
    except Exception as e:
        logger.debug(f"Incremental scroll failed: {e}")

    # Layer 3: Wait briefly for any remaining images to finish loading
    try:
        status = await page.evaluate(CHECK_IMAGES_LOADED_JS)
        if status.get("notLoaded", 0) > 0:
            await page.wait_for_timeout(500)
            logger.debug(f"Waiting for {status['notLoaded']} images to finish loading")
    except Exception:
        pass


class CaptureRequest(BaseModel):
    url: HttpUrl
    full_page: bool = True
    wait_ms: int = 3000
    include_text: bool = False  # Also extract page.inner_text("body") for content hashing


class CaptureResponse(BaseModel):
    success: bool
    screenshot: Optional[str] = None  # base64 encoded (clean page)
    popup_screenshot: Optional[str] = None  # base64 encoded (before dismissal, only if popup detected)
    popup_detected: bool = False
    title: Optional[str] = None
    text_content: Optional[str] = None  # Visible text from page (when include_text=True)
    error: Optional[str] = None
    popups_dismissed: int = 0


# Device presets for responsive capture
DEVICE_PRESETS = {
    "desktop": {"width": 1280, "height": 800, "is_mobile": False, "has_touch": False},
    "mobile": {"width": 375, "height": 812, "is_mobile": True, "has_touch": True,
               "user_agent": "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15"},
    "tablet": {"width": 768, "height": 1024, "is_mobile": True, "has_touch": True},
}

# Default selectors for LP section detection (top-level only to avoid over-segmentation)
DEFAULT_SECTION_SELECTORS = [
    "body > section",
    "body > div > section",
    "body > main > section",
    "body > main > div[class*='section']",
    "body > div[class*='block']",
    "body > div[class*='section']",
    "body > div[class*='container'] > section",
]


class SectionInfo(BaseModel):
    """Information about a captured section."""
    index: int
    y: float
    height: float
    width: float
    selector_matched: Optional[str] = None
    screenshot: str  # base64 encoded


class SectionCaptureRequest(BaseModel):
    url: HttpUrl
    device: str = "desktop"  # desktop, mobile, tablet, or Playwright device name
    min_height: int = 300  # Skip sections smaller than this
    max_sections: int = 8  # Cap output to prevent token explosion
    merge_threshold: int = 500  # Merge adjacent sections smaller than this
    wait_ms: int = 3000
    selectors: Optional[List[str]] = None  # Custom selectors, uses defaults if None


class SectionCaptureResponse(BaseModel):
    success: bool
    sections: List[SectionInfo] = []
    device: str = "desktop"
    viewport: Dict[str, int] = {}
    total_page_height: float = 0
    sections_found: int = 0
    sections_after_filter: int = 0
    title: Optional[str] = None
    error: Optional[str] = None


class FixedWindowRateLimiter:
    """Simple in-memory rate limiter (best-effort, per instance)."""

    def __init__(self, limit_per_window: int, window_seconds: float = 60.0):
        self._limit = limit_per_window
        self._window_seconds = window_seconds
        self._lock = asyncio.Lock()
        self._counters: Dict[str, Tuple[int, float]] = {}

    async def allow(self, key: str) -> bool:
        if self._limit <= 0:
            return True

        now = time.monotonic()
        async with self._lock:
            count, window_start = self._counters.get(key, (0, now))
            if now - window_start >= self._window_seconds:
                count, window_start = 0, now
            if count >= self._limit:
                self._counters[key] = (count, window_start)
                return False
            self._counters[key] = (count + 1, window_start)
            return True

def validate_url(url: str) -> None:
    """Validate URL for SSRF protection."""
    try:
        validate_url_for_ssrf(url)
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e)) from e


_rate_limiter = FixedWindowRateLimiter(PLAYWRIGHT_RATE_LIMIT_PER_MINUTE, window_seconds=60.0)


async def _rate_limit(request: Request) -> None:
    if request.url.path == "/health":
        return

    client_host = request.client.host if request.client else "unknown"
    if not await _rate_limiter.allow(client_host):
        raise HTTPException(status_code=429, detail="Rate limit exceeded")


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Manage browser lifecycle."""
    global _semaphore, _action_semaphore, _browser, _playwright, _session_manager

    _semaphore = asyncio.Semaphore(MAX_CONCURRENT_BROWSERS)
    _action_semaphore = asyncio.Semaphore(MAX_CONCURRENT_ACTIONS)
    _playwright = await async_playwright().start()
    _browser = await _playwright.chromium.launch(
        headless=True,
        args=[
            "--no-sandbox",
            "--disable-setuid-sandbox",
            "--disable-dev-shm-usage",
            "--disable-gpu",
        ]
    )
    logger.info("Browser started (stealth=%s, proxy_fallback=%s)", STEALTH_ENABLED, PROXY_FALLBACK_ENABLED)

    # Initialize session manager for browser automation
    _session_manager = BrowserSessionManager(
        _browser, stealth_fn=_apply_stealth, default_user_agent=DEFAULT_USER_AGENT
    )
    await _session_manager.start()
    logger.info("Session manager started")

    yield

    # Cleanup
    await _session_manager.stop()
    if _proxy_browser and _proxy_browser.is_connected():
        await _proxy_browser.close()
    await _browser.close()
    await _playwright.stop()
    logger.info("Browser stopped")


app = FastAPI(
    title="Playwright Screenshot Service",
    version="1.0.0",
    lifespan=lifespan
)


@app.get("/health")
async def health():
    """Health check endpoint."""
    return {"status": "ok", "browser_connected": _browser is not None and _browser.is_connected()}


async def _do_capture(browser: Browser, url: str, body: CaptureRequest, timeout_ms: int = 0) -> CaptureResponse:
    """Core capture logic — navigates, dismisses popups, takes screenshot."""
    nav_timeout = timeout_ms or REQUEST_TIMEOUT_MS
    context = None
    page = None
    try:
        context = await browser.new_context(
            viewport={"width": VIEWPORT_WIDTH, "height": VIEWPORT_HEIGHT},
            user_agent=DEFAULT_USER_AGENT,
            ignore_https_errors=True,
        )
        try:
            await _apply_stealth(context)
        except Exception:
            logger.warning("Stealth init failed for %s, continuing without", url, exc_info=True)
        page = await context.new_page()
        page.set_default_timeout(nav_timeout)

        # Navigate with timeout (use domcontentloaded to avoid waiting for all resources)
        await page.goto(url, timeout=nav_timeout, wait_until="domcontentloaded")

        # Wait for page to settle
        await page.wait_for_timeout(body.wait_ms)

        # Detect if popup is present
        popup_detected = False
        popup_screenshot_b64 = None
        try:
            popup_detected = await page.evaluate(POPUP_DETECTION_JS)
        except Exception as e:
            logger.warning(f"Popup detection failed: {e}")

        # If popup detected, capture it first before dismissal
        if popup_detected:
            logger.info(f"Popup detected on {url}, capturing before dismissal")
            popup_bytes = await page.screenshot(full_page=False)  # Viewport only for popup
            popup_screenshot_b64 = base64.b64encode(popup_bytes).decode("utf-8")

        # Try to dismiss popups by clicking close buttons
        popups_dismissed = 0
        for selector in POPUP_SELECTORS:
            try:
                element = page.locator(selector).first
                if await element.is_visible(timeout=500):
                    await element.click(timeout=1000)
                    popups_dismissed += 1
                    await page.wait_for_timeout(500)  # Wait for animation
            except Exception:
                continue  # Selector not found or not clickable

        # DOM-based popup removal as fallback
        try:
            removed = await page.evaluate(POPUP_REMOVAL_JS)
            popups_dismissed += removed
        except Exception as e:
            logger.warning(f"DOM removal failed: {e}")

        # Final wait for any animations
        if popups_dismissed > 0:
            await page.wait_for_timeout(500)

        # Detect scroll-hijacking (Swiper, fullPage.js, etc.) and capture slide-by-slide
        hijack_info = None
        if body.full_page:
            try:
                hijack_info = await page.evaluate(DETECT_SCROLL_HIJACK_JS)
            except Exception as e:
                logger.warning(f"Scroll hijack detection failed: {e}")

        if logger.isEnabledFor(logging.DEBUG):
            try:
                debug_info = await page.evaluate("""() => {
                    const html = document.documentElement;
                    const body = document.body;
                    const hs = getComputedStyle(html);
                    const bs = getComputedStyle(body);
                    return {
                        htmlOverflow: hs.overflow, htmlOverflowY: hs.overflowY,
                        bodyOverflow: bs.overflow, bodyOverflowY: bs.overflowY,
                        scrollHeight: html.scrollHeight, innerHeight: window.innerHeight,
                        hasSwiper: !!document.querySelector('.swiper'),
                        hasSwiperInstance: !!document.querySelector('.swiper')?.swiper
                    };
                }""")
                logger.debug(f"Scroll hijack debug for {url}: {debug_info}")
                logger.debug(f"Scroll hijack detection result for {url}: {hijack_info}")
            except Exception as e:
                logger.debug(f"Scroll hijack debug failed for {url}: {e}")
        if hijack_info and hijack_info.get("contentSlides"):
            logger.info(f"Detected {hijack_info['type']} with {len(hijack_info['contentSlides'])} content slides on {url}")
            from io import BytesIO
            from PIL import Image

            slide_images = []
            for slide_idx in hijack_info["contentSlides"]:
                try:
                    await page.evaluate(NAVIGATE_TO_SLIDE_JS, slide_idx)
                    await page.wait_for_timeout(400)
                    # Skip slides that are empty after lazy-load rendering
                    has_content = await page.evaluate(CHECK_ACTIVE_SLIDE_CONTENT_JS)
                    if not has_content:
                        continue
                    slide_bytes = await page.screenshot(full_page=False)
                    img = Image.open(BytesIO(slide_bytes)).convert("RGB")
                    slide_images.append(img)
                except Exception as e:
                    logger.warning(f"Failed to capture slide {slide_idx}: {e}")
                    continue

            if slide_images:
                # Stitch vertically
                total_height = sum(img.height for img in slide_images)
                width = slide_images[0].width
                stitched = Image.new("RGB", (width, total_height))
                y_offset = 0
                for img in slide_images:
                    stitched.paste(img, (0, y_offset))
                    y_offset += img.height

                buf = BytesIO()
                stitched.save(buf, format="PNG")
                screenshot_bytes = buf.getvalue()
            else:
                logger.warning(f"Scroll hijack detected but no slides captured for {url}, falling back to viewport screenshot")
                screenshot_bytes = await page.screenshot(full_page=False)
        else:
            # Standard full-page screenshot
            if body.full_page:
                await _preload_lazy_content(page, VIEWPORT_HEIGHT)
            screenshot_bytes = await page.screenshot(full_page=body.full_page)
        screenshot_b64 = base64.b64encode(screenshot_bytes).decode("utf-8")

        # Get page title
        title = await page.title()

        # Extract visible text content if requested (for content hashing/diff)
        text_content = None
        if body.include_text:
            try:
                text_content = await page.inner_text("body")
            except Exception as e:
                logger.warning(f"Text extraction failed for {url}: {e}")

        return CaptureResponse(
            success=True,
            screenshot=screenshot_b64,
            popup_screenshot=popup_screenshot_b64,
            popup_detected=popup_detected,
            title=title,
            text_content=text_content,
            popups_dismissed=popups_dismissed
        )

    except PlaywrightTimeout as e:
        logger.warning("Playwright timeout for %s: %s", url, str(e)[:200])
        return CaptureResponse(
            success=False,
            error=f"Timeout loading page (>{nav_timeout}ms)"
        )
    except Exception as e:
        logger.exception(f"Capture failed for {url}")
        return CaptureResponse(
            success=False,
            error=str(e)
        )
    finally:
        if page:
            await page.close()
        if context:
            await context.close()


@app.post("/capture", response_model=CaptureResponse)
async def capture(body: CaptureRequest, _: None = Depends(_rate_limit)):
    """Capture screenshot with popup dismissal. Falls back to proxy on WAF block."""
    url = str(body.url)

    # SSRF protection
    validate_url(url)

    # Check if service is initialized (lifespan startup completed)
    if _semaphore is None or _browser is None:
        raise HTTPException(status_code=503, detail="Service not ready - please retry")

    # Acquire semaphore for concurrency control
    async with _semaphore:
        result = await _do_capture(_browser, url, body)

        # If WAF blocked and proxy available, retry through residential proxy
        if _is_waf_blocked(result.title) and PROXY_FALLBACK_ENABLED:
            logger.info("WAF blocked on %s (title=%s), retrying with proxy", url, result.title)
            try:
                proxy_browser = await _get_proxy_browser()
                if proxy_browser:
                    proxy_result = await _do_capture(proxy_browser, url, body, timeout_ms=PROXY_TIMEOUT_MS)
                    if proxy_result.success and not _is_waf_blocked(proxy_result.title):
                        logger.info("Proxy fallback succeeded for %s", url)
                        result = proxy_result
                    else:
                        logger.warning("Proxy fallback also blocked for %s (title=%s)", url, proxy_result.title)
            except Exception:
                logger.exception("Proxy fallback failed for %s, returning original result", url)

        return result


@app.post("/capture/sections", response_model=SectionCaptureResponse)
async def capture_sections(body: SectionCaptureRequest, _: None = Depends(_rate_limit)):
    """
    Capture LP sections as separate screenshots for block-type analysis.

    Extracts page sections using DOM selectors, filters by size,
    merges adjacent small sections, and captures each as separate image.
    """
    url = str(body.url)
    validate_url(url)

    if _semaphore is None or _browser is None:
        raise HTTPException(status_code=503, detail="Service not ready")

    async with _semaphore:
        context = None
        page = None
        try:
            # Get device settings
            device_settings = DEVICE_PRESETS.get(body.device.lower())
            if device_settings is None:
                device_settings = DEVICE_PRESETS["desktop"]
                logger.warning(f"Device '{body.device}' not found, using desktop")

            # Create context with device emulation
            context_opts = {
                "viewport": {"width": device_settings["width"], "height": device_settings["height"]},
                "user_agent": device_settings.get("user_agent", DEFAULT_USER_AGENT),
            }
            if device_settings.get("is_mobile"):
                context_opts["is_mobile"] = True
            if device_settings.get("has_touch"):
                context_opts["has_touch"] = True

            context_opts["ignore_https_errors"] = True
            context = await _browser.new_context(**context_opts)
            try:
                await _apply_stealth(context)
            except Exception:
                logger.warning("Stealth init failed for %s, continuing without", url, exc_info=True)
            page = await context.new_page()

            # Navigate
            await page.goto(url, timeout=REQUEST_TIMEOUT_MS, wait_until="domcontentloaded")
            await page.wait_for_timeout(body.wait_ms)

            # Dismiss popups (reuse existing logic)
            for selector in POPUP_SELECTORS:
                try:
                    element = page.locator(selector).first
                    if await element.is_visible(timeout=500):
                        await element.click(timeout=1000)
                        await page.wait_for_timeout(300)
                except Exception:
                    continue

            try:
                await page.evaluate(POPUP_REMOVAL_JS)
            except Exception:
                pass

            # Detect scroll-hijacking containers (Swiper, fullPage.js, etc.)
            hijack_info = None
            try:
                hijack_info = await page.evaluate(DETECT_SCROLL_HIJACK_JS)
            except Exception as e:
                logger.warning(f"Scroll hijack detection failed: {e}")

            # Get page dimensions
            page_height = await page.evaluate("() => document.documentElement.scrollHeight")
            viewport_width = device_settings["width"]
            viewport_height = device_settings["height"]

            # For scroll-hijacked pages, capture each slide as a section
            if hijack_info and hijack_info.get("contentSlides"):
                logger.info(f"Detected {hijack_info['type']} with {len(hijack_info['contentSlides'])} content slides on {url}")
                from io import BytesIO

                section_results = []
                content_slides = hijack_info["contentSlides"]
                # Cap at max_sections
                if len(content_slides) > body.max_sections:
                    content_slides = content_slides[:body.max_sections]

                captured_idx = 0
                for slide_idx in content_slides:
                    try:
                        await page.evaluate(NAVIGATE_TO_SLIDE_JS, slide_idx)
                        await page.wait_for_timeout(400)
                        # Skip slides that are empty after lazy-load rendering
                        has_content = await page.evaluate(CHECK_ACTIVE_SLIDE_CONTENT_JS)
                        if not has_content:
                            continue
                        slide_bytes = await page.screenshot(full_page=False)
                        slide_b64 = base64.b64encode(slide_bytes).decode("utf-8")
                        section_results.append(SectionInfo(
                            index=captured_idx,
                            y=captured_idx * viewport_height,
                            height=viewport_height,
                            width=viewport_width,
                            selector_matched=f"{hijack_info['type']}_slide_{slide_idx}",
                            screenshot=slide_b64,
                        ))
                        captured_idx += 1
                    except Exception as e:
                        logger.warning(f"Failed to capture slide {slide_idx}: {e}")
                        continue

                sections_found = len(hijack_info["contentSlides"])
                page_height = sections_found * viewport_height

            else:
                # Standard section extraction
                # Extract sections using selectors
                selectors = body.selectors or DEFAULT_SECTION_SELECTORS
                raw_sections = []

                for sel in selectors:
                    try:
                        elements = page.locator(sel)
                        count = await elements.count()
                        for i in range(count):
                            el = elements.nth(i)
                            try:
                                if not await el.is_visible(timeout=500):
                                    continue
                                box = await el.bounding_box()
                                if box and box["height"] >= body.min_height:
                                    raw_sections.append({
                                        "y": box["y"],
                                        "height": box["height"],
                                        "width": box["width"],
                                        "selector": sel,
                                    })
                            except Exception:
                                continue
                    except Exception:
                        continue

                sections_found = len(raw_sections)

                # Fallback: viewport-based chunking if too few sections
                if len(raw_sections) < 3:
                    logger.info(f"Only {len(raw_sections)} sections found, using viewport chunking")
                    raw_sections = []
                    chunk_height = 800
                    y = 0
                    while y < page_height:
                        h = min(chunk_height, page_height - y)
                        if h >= body.min_height:
                            raw_sections.append({
                                "y": y,
                                "height": h,
                                "width": viewport_width,
                                "selector": "viewport_chunk",
                            })
                        y += chunk_height

                # Sort by Y position and dedupe overlapping
                raw_sections.sort(key=lambda s: s["y"])
                deduped = []
                for sec in raw_sections:
                    # Skip if overlaps >50% with previous
                    if deduped:
                        prev = deduped[-1]
                        prev_end = prev["y"] + prev["height"]
                        sec_end = sec["y"] + sec["height"]
                        overlap = max(0, min(prev_end, sec_end) - max(prev["y"], sec["y"]))
                        overlap_pct = overlap / min(prev["height"], sec["height"]) if min(prev["height"], sec["height"]) > 0 else 0
                        if overlap_pct > 0.5:
                            continue
                    deduped.append(sec)

                # Merge adjacent small sections
                merged = []
                for sec in deduped:
                    if merged and sec["height"] < body.merge_threshold:
                        prev = merged[-1]
                        gap = sec["y"] - (prev["y"] + prev["height"])
                        if gap < 100 and prev["height"] < body.merge_threshold:
                            # Merge with previous
                            prev["height"] = (sec["y"] + sec["height"]) - prev["y"]
                            prev["selector"] = "merged"
                            continue
                    merged.append(sec)

                # Cap at max_sections
                if len(merged) > body.max_sections:
                    merged = merged[:body.max_sections]

                # Preload lazy content before full-page capture
                await _preload_lazy_content(page, device_settings["height"])

                # Take full-page screenshot first, then clip sections from it
                full_screenshot_bytes = await page.screenshot(full_page=True)

                # Capture each section by clipping from full screenshot
                section_results = []
                for idx, sec in enumerate(merged):
                    try:
                        # Use PIL to crop section from full screenshot
                        from io import BytesIO
                        from PIL import Image

                        full_img = Image.open(BytesIO(full_screenshot_bytes))

                        # Calculate crop box (left, upper, right, lower)
                        left = 0
                        upper = int(sec["y"])
                        right = int(sec["width"]) if sec["width"] <= full_img.width else full_img.width
                        lower = int(sec["y"] + sec["height"])

                        # Clamp to image bounds
                        upper = max(0, min(upper, full_img.height))
                        lower = max(0, min(lower, full_img.height))

                        if lower <= upper:
                            logger.warning(f"Section {idx} has invalid bounds: y={sec['y']}, height={sec['height']}")
                            continue

                        section_img = full_img.crop((left, upper, right, lower))

                        # Convert back to bytes
                        section_buffer = BytesIO()
                        section_img.save(section_buffer, format="PNG")
                        screenshot_bytes = section_buffer.getvalue()

                        screenshot_b64 = base64.b64encode(screenshot_bytes).decode("utf-8")
                        section_results.append(SectionInfo(
                            index=idx,
                            y=sec["y"],
                            height=sec["height"],
                            width=sec["width"],
                            selector_matched=sec["selector"],
                            screenshot=screenshot_b64,
                        ))
                    except Exception as e:
                        logger.warning(f"Failed to capture section {idx}: {e}")
                        continue

            title = await page.title()

            return SectionCaptureResponse(
                success=True,
                sections=section_results,
                device=body.device,
                viewport={"width": device_settings["width"], "height": device_settings["height"]},
                total_page_height=page_height,
                sections_found=sections_found,
                sections_after_filter=len(section_results),
                title=title,
            )

        except PlaywrightTimeout:
            return SectionCaptureResponse(
                success=False,
                error=f"Timeout loading page (>{REQUEST_TIMEOUT_MS}ms)"
            )
        except Exception as e:
            logger.exception(f"Section capture failed for {url}")
            return SectionCaptureResponse(
                success=False,
                error=str(e)
            )
        finally:
            if page:
                await page.close()
            if context:
                await context.close()


# =============================================================================
# Browser Session API - Stateful browser automation
# =============================================================================

class BrowserActionType(str, Enum):
    """Supported browser actions."""
    NAVIGATE = "navigate"
    CLICK = "click"
    TYPE = "type"
    SCREENSHOT = "screenshot"
    SCROLL = "scroll"
    WAIT = "wait"
    EXTRACT = "extract"
    EVALUATE = "evaluate"


class BrowserActionRequest(BaseModel):
    """Request for a browser action."""
    session_id: str = Field(..., description="Unique session identifier")
    action: BrowserActionType = Field(..., description="Action to perform")

    # Navigation
    url: Optional[str] = Field(None, description="URL to navigate to (for navigate action)")
    wait_until: Optional[str] = Field("domcontentloaded", description="Wait condition: load, domcontentloaded, networkidle")

    # Element interaction
    selector: Optional[str] = Field(None, description="CSS/XPath selector for element")
    text: Optional[str] = Field(None, description="Text to type (for type action)")

    # Click options
    button: Optional[str] = Field("left", description="Mouse button: left, right, middle")
    click_count: Optional[int] = Field(1, description="Number of clicks")

    # Scroll
    x: Optional[int] = Field(None, description="Horizontal scroll amount or X coordinate")
    y: Optional[int] = Field(None, description="Vertical scroll amount or Y coordinate")

    # Wait
    timeout_ms: Optional[int] = Field(5000, description="Timeout in milliseconds")

    # Screenshot
    full_page: Optional[bool] = Field(False, description="Capture full page screenshot")

    # Extract
    extract_type: Optional[str] = Field("text", description="What to extract: text, html, attribute")
    attribute: Optional[str] = Field(None, description="Attribute name to extract")

    # Evaluate
    script: Optional[str] = Field(None, description="JavaScript to evaluate")

    # Session options (used when creating new session)
    viewport_width: Optional[int] = Field(1280, description="Viewport width")
    viewport_height: Optional[int] = Field(720, description="Viewport height")
    locale: Optional[str] = Field("en-US", description="Browser locale")


class ElementInfo(BaseModel):
    """Information about a page element."""
    tag: str
    text: Optional[str] = None
    attributes: Dict[str, str] = Field(default_factory=dict)
    visible: bool = True
    bounding_box: Optional[Dict[str, float]] = None


class BrowserActionResponse(BaseModel):
    """Response from a browser action."""
    success: bool
    session_id: str
    action: str
    error: Optional[str] = None

    # Navigation result
    url: Optional[str] = None
    title: Optional[str] = None

    # Screenshot result
    screenshot: Optional[str] = None  # base64 encoded

    # Extract result
    content: Optional[str] = None
    elements: Optional[List[ElementInfo]] = None

    # Evaluate result
    result: Optional[Any] = None

    # Metadata
    elapsed_ms: Optional[int] = None


class SessionCloseRequest(BaseModel):
    """Request to close a browser session."""
    session_id: str


class SessionStatsResponse(BaseModel):
    """Response with session statistics."""
    active_sessions: int
    max_sessions: int
    ttl_seconds: int
    sessions: List[Dict[str, Any]]


@app.post("/browser/action", response_model=BrowserActionResponse)
async def browser_action(action_req: BrowserActionRequest, _: None = Depends(_rate_limit)):
    """
    Execute a browser action within a session.

    Sessions are created automatically on first action and persist until:
    - Explicit close via /browser/close
    - TTL expiration (5 minutes idle)
    - Server restart
    """
    start_time = time.time()

    if _session_manager is None or _action_semaphore is None:
        raise HTTPException(status_code=503, detail="Service not ready")

    async with _action_semaphore:
        try:
            # Get or create session
            page, _ = await _session_manager.get_or_create_session(
                session_id=action_req.session_id,
                viewport_width=action_req.viewport_width,
                viewport_height=action_req.viewport_height,
                locale=action_req.locale,
            )

            result = BrowserActionResponse(
                success=True,
                session_id=action_req.session_id,
                action=action_req.action.value,
            )

            # Execute action
            if action_req.action == BrowserActionType.NAVIGATE:
                if not action_req.url:
                    raise HTTPException(status_code=400, detail="URL required for navigate action")

                # SSRF protection
                validate_url(action_req.url)

                await page.goto(
                    action_req.url,
                    timeout=action_req.timeout_ms,
                    wait_until=action_req.wait_until,
                )
                result.url = page.url
                result.title = await page.title()

            elif action_req.action == BrowserActionType.CLICK:
                if not action_req.selector:
                    raise HTTPException(status_code=400, detail="Selector required for click action")

                await page.click(
                    action_req.selector,
                    button=action_req.button,
                    click_count=action_req.click_count,
                    timeout=action_req.timeout_ms,
                )

            elif action_req.action == BrowserActionType.TYPE:
                if not action_req.selector:
                    raise HTTPException(status_code=400, detail="Selector required for type action")
                if action_req.text is None:
                    raise HTTPException(status_code=400, detail="Text required for type action")

                await page.fill(action_req.selector, action_req.text, timeout=action_req.timeout_ms)

            elif action_req.action == BrowserActionType.SCREENSHOT:
                if action_req.full_page:
                    vp = page.viewport_size or {"height": 800}
                    await _preload_lazy_content(page, vp["height"])
                screenshot_bytes = await page.screenshot(full_page=action_req.full_page)
                result.screenshot = base64.b64encode(screenshot_bytes).decode("utf-8")
                result.url = page.url
                result.title = await page.title()

            elif action_req.action == BrowserActionType.SCROLL:
                if action_req.selector:
                    # Scroll element into view
                    await page.locator(action_req.selector).scroll_into_view_if_needed(
                        timeout=action_req.timeout_ms
                    )
                else:
                    # Scroll by amount
                    await page.evaluate(f"window.scrollBy({action_req.x or 0}, {action_req.y or 0})")

            elif action_req.action == BrowserActionType.WAIT:
                if action_req.selector:
                    await page.wait_for_selector(action_req.selector, timeout=action_req.timeout_ms)
                else:
                    await page.wait_for_timeout(action_req.timeout_ms)

            elif action_req.action == BrowserActionType.EXTRACT:
                if not action_req.selector:
                    # Extract from whole page
                    if action_req.extract_type == "text":
                        result.content = await page.inner_text("body")
                    elif action_req.extract_type == "html":
                        result.content = await page.content()
                else:
                    elements = page.locator(action_req.selector)
                    count = await elements.count()

                    extracted_elements = []
                    for i in range(min(count, 50)):  # Limit to 50 elements
                        el = elements.nth(i)
                        try:
                            elem_info = ElementInfo(
                                tag=await el.evaluate("el => el.tagName.toLowerCase()"),
                                text=await el.inner_text() if action_req.extract_type == "text" else None,
                                visible=await el.is_visible(),
                            )
                            if action_req.extract_type == "attribute" and action_req.attribute:
                                attr_val = await el.get_attribute(action_req.attribute)
                                elem_info.attributes[action_req.attribute] = attr_val or ""

                            # Get bounding box
                            try:
                                box = await el.bounding_box()
                                if box:
                                    elem_info.bounding_box = box
                            except Exception:
                                pass

                            extracted_elements.append(elem_info)
                        except Exception:
                            continue

                    result.elements = extracted_elements
                    if extracted_elements and action_req.extract_type == "text":
                        result.content = "\n".join(e.text for e in extracted_elements if e.text)

            elif action_req.action == BrowserActionType.EVALUATE:
                if not ENABLE_BROWSER_EVALUATE:
                    raise HTTPException(status_code=403, detail="Evaluate action is disabled")
                if not action_req.script:
                    raise HTTPException(status_code=400, detail="Script required for evaluate action")

                eval_result = await page.evaluate(action_req.script)
                result.result = eval_result

            result.elapsed_ms = int((time.time() - start_time) * 1000)
            return result

        except PlaywrightTimeout as e:
            return BrowserActionResponse(
                success=False,
                session_id=action_req.session_id,
                action=action_req.action.value,
                error=f"Timeout: {str(e)}",
                elapsed_ms=int((time.time() - start_time) * 1000),
            )
        except HTTPException:
            raise
        except Exception as e:
            logger.exception(f"Browser action failed: {action_req.action}")
            return BrowserActionResponse(
                success=False,
                session_id=action_req.session_id,
                action=action_req.action.value,
                error=str(e),
                elapsed_ms=int((time.time() - start_time) * 1000),
            )


@app.post("/browser/close")
async def browser_close(body: SessionCloseRequest, _: None = Depends(_rate_limit)):
    """Close a browser session and free resources."""
    if _session_manager is None:
        raise HTTPException(status_code=503, detail="Service not ready")

    closed = await _session_manager.close_session(body.session_id)
    return {"success": closed, "session_id": body.session_id}


@app.get("/browser/sessions", response_model=SessionStatsResponse)
async def browser_sessions(_: None = Depends(_rate_limit)):
    """Get statistics about active browser sessions."""
    if _session_manager is None:
        raise HTTPException(status_code=503, detail="Service not ready")

    return await _session_manager.get_stats()


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8002)
