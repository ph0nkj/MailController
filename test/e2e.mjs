#!/usr/bin/env node
// Local end-to-end UI checks for MailController.
//
// The script drives headless Chrome through the DevTools Protocol, exercises
// the real SPA, and writes screenshots to test/screenshots. It uses only
// Node's built-in WebSocket/fetch support, so no npm dependencies are needed.

import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const BASE = (process.env.MC_TEST_BASE_URL || "http://127.0.0.1:18088").replace(/\/$/, "");
const CDP_PORT = Number(process.env.MC_TEST_CDP_PORT || 9223);
const ADMIN_PASSWORD = process.env.MC_ADMIN_PASSWORD || "";
const SHOTS = path.join(HERE, "screenshots");
const MOCK_LLM = process.env.MC_TEST_MOCK_LLM || "http://127.0.0.1:18090/v1";
const VALID_OPENAI_KEY = "sk-test-valid-key";
const VALID_ANTHROPIC_KEY = "sk-ant-test-valid-key";
const BAD_KEY = "sk-test-invalid-key";
const VIEWPORT = { width: 1440, height: 900 };
const MOBILE = { width: 390, height: 844 };

const results = [];
const jsErrors = [];
const networkFailures = [];

function check(name, ok, detail = "") {
  results.push({ name, ok: Boolean(ok), detail });
  const suffix = detail ? ` — ${detail}` : "";
  console.log(`${ok ? "PASS" : "FAIL"} ${name}${suffix}`);
}

function redact(value) {
  return String(value).replaceAll(VALID_OPENAI_KEY, "[redacted]").replaceAll(VALID_ANTHROPIC_KEY, "[redacted]").replaceAll(BAD_KEY, "[redacted]");
}

class PageSession {
  constructor(ws) {
    this.ws = ws;
    this.nextID = 1;
    this.pending = new Map();
    this.waiters = [];
    ws.addEventListener("message", (event) => {
      let message;
      try {
        message = JSON.parse(event.data);
      } catch {
        return;
      }
      if (message.id) {
        const pending = this.pending.get(message.id);
        if (!pending) return;
        this.pending.delete(message.id);
        if (message.error) pending.reject(new Error(`${message.error.message} (${message.error.code})`));
        else pending.resolve(message.result);
        return;
      }
      if (message.method === "Runtime.exceptionThrown") {
        const details = message.params?.exceptionDetails;
        jsErrors.push(redact(details?.exception?.description || details?.text || "unknown exception"));
      }
      if (message.method === "Network.loadingFailed") {
        const params = message.params || {};
        if (params.blockedReason || params.errorText !== "net::ERR_ABORTED") {
          networkFailures.push(`${params.errorText || params.blockedReason} ${params.type || ""}`);
        }
      }
      for (const waiter of [...this.waiters]) {
        if (waiter.method === message.method) {
          this.waiters.splice(this.waiters.indexOf(waiter), 1);
          waiter.resolve(message.params);
        }
      }
    });
  }

  static async open() {
    let response = await fetch(`http://127.0.0.1:${CDP_PORT}/json/new?about:blank`, { method: "PUT" });
    if (!response.ok) {
      response = await fetch(`http://127.0.0.1:${CDP_PORT}/json/new?about:blank`);
    }
    if (!response.ok) throw new Error(`Chrome CDP rejected a new tab: HTTP ${response.status}`);
    const target = await response.json();
    const ws = new WebSocket(target.webSocketDebuggerUrl);
    await new Promise((resolve, reject) => {
      ws.addEventListener("open", resolve, { once: true });
      ws.addEventListener("error", () => reject(new Error("Chrome CDP WebSocket failed")), { once: true });
    });
    const page = new PageSession(ws);
    await Promise.all([
      page.send("Page.enable"),
      page.send("Runtime.enable"),
      page.send("Network.enable"),
    ]);
    return page;
  }

  send(method, params = {}) {
    const id = this.nextID++;
    this.ws.send(JSON.stringify({ id, method, params }));
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
    });
  }

  waitEvent(method, timeout = 15000) {
    return new Promise((resolve, reject) => {
      const waiter = { method, resolve, reject };
      this.waiters.push(waiter);
      const timer = setTimeout(() => {
        const index = this.waiters.indexOf(waiter);
        if (index >= 0) this.waiters.splice(index, 1);
        reject(new Error(`timed out waiting for ${method}`));
      }, timeout);
      const originalResolve = waiter.resolve;
      waiter.resolve = (params) => {
        clearTimeout(timer);
        originalResolve(params);
      };
    });
  }

  async evaluate(expression) {
    const response = await this.send("Runtime.evaluate", {
      expression,
      returnByValue: true,
      awaitPromise: true,
      userGesture: true,
    });
    if (response.exceptionDetails) {
      const description = response.exceptionDetails.exception?.description || response.exceptionDetails.text;
      throw new Error(redact(description));
    }
    return response.result?.value;
  }

  async waitFor(expression, label, timeout = 15000) {
    const deadline = Date.now() + timeout;
    let last = "";
    while (Date.now() < deadline) {
      try {
        const value = await this.evaluate(`Boolean(${expression})`);
        if (value) return true;
      } catch (error) {
        last = error.message;
      }
      await sleep(100);
    }
    throw new Error(`timed out waiting for ${label}${last ? ` (${last})` : ""}`);
  }

  async setViewport(size) {
    await this.send("Emulation.setDeviceMetricsOverride", {
      width: size.width,
      height: size.height,
      deviceScaleFactor: 1,
      mobile: size.width < 600,
    });
    await sleep(150);
  }

  async navigate(url) {
    const loaded = this.waitEvent("Page.loadEventFired", 20000).catch(() => undefined);
    await this.send("Page.navigate", { url });
    await loaded;
    await this.waitFor("document.readyState === 'complete'", "document ready");
  }

  async screenshot(name) {
    await sleep(320);
    const { data } = await this.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
    const target = path.join(SHOTS, name);
    await writeFile(target, Buffer.from(data, "base64"));
    console.log(`SHOT ${path.relative(HERE, target)}`);
  }

  async close() {
    try {
      await this.send("Page.close");
    } catch {
      // The browser may already have closed the tab.
    }
    try {
      this.ws.close();
    } catch {
      // Ignore shutdown races.
    }
  }
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

const setValue = (selector, value) => `(() => {
  const el = document.querySelector(${JSON.stringify(selector)});
  if (!el) return false;
  const proto = el instanceof HTMLSelectElement
    ? HTMLSelectElement.prototype
    : el instanceof HTMLTextAreaElement
      ? HTMLTextAreaElement.prototype
      : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, "value").set.call(el, ${JSON.stringify(value)});
  el.dispatchEvent(new Event("input", { bubbles: true }));
  el.dispatchEvent(new Event("change", { bubbles: true }));
  return true;
})()`;

const aiCard = "[...document.querySelectorAll('section.card')].find((card) => card.querySelector('#ai-api-key'))";
const aiBadge = `(${aiCard})?.querySelector('.row-between .badge')?.textContent.trim() || ''`;
const aiButton = (label) => `(() => {
  const card = ${aiCard};
  const button = [...card.querySelectorAll('button')].find((item) => item.textContent.trim() === ${JSON.stringify(label)});
  if (!button) return false;
  button.click();
  return true;
})()`;

async function waitForTitle(page, title) {
  await page.waitFor(
    `document.querySelector('.page-title')?.textContent.trim() === ${JSON.stringify(title)}`,
    `page title ${title}`,
  );
}

async function contentWidth(page) {
  await sleep(300);
  return page.evaluate("document.querySelector('.content').getBoundingClientRect().width");
}

async function openRoute(page, route, title) {
  const clicked = await page.evaluate(`(() => {
    const link = document.querySelector('a.nav-link[href=${JSON.stringify(route)}]');
    if (!link) return false;
    link.click();
    return true;
  })()`);
  check(`navigate to ${title}`, clicked);
  await waitForTitle(page, title);
  await page.waitFor(`document.querySelector('.view') !== null`, "animated view");
}

async function overflowReport(page) {
  return page.evaluate(`(() => {
    const viewport = Math.min(
      document.documentElement.clientWidth,
      window.visualViewport ? window.visualViewport.width : document.documentElement.clientWidth,
    );
    const offenders = [];
    for (const el of document.querySelectorAll("body *")) {
      const style = getComputedStyle(el);
      if (style.display === "none" || style.visibility === "hidden" || style.position === "fixed") continue;
      const rect = el.getBoundingClientRect();
      if (rect.width === 0 || rect.height === 0) continue;
      if (rect.right <= viewport + 1 && rect.left >= -1) continue;
      let parent = el.parentElement;
      let insideScroller = false;
      while (parent) {
        const parentStyle = getComputedStyle(parent);
        if (/(auto|scroll)/.test(parentStyle.overflowX) && parent.scrollWidth > parent.clientWidth + 1) {
          insideScroller = true;
          break;
        }
        parent = parent.parentElement;
      }
      if (!insideScroller) {
        offenders.push({
          tag: el.tagName,
          cls: String(el.getAttribute("class") || "").slice(0, 70),
          right: Math.round(rect.right),
        });
      }
    }
    return {
      viewport,
      innerWidth: window.innerWidth,
      scrollWidth: document.documentElement.scrollWidth,
      overflow: Math.max(0, document.documentElement.scrollWidth - viewport),
      offenders: offenders.slice(0, 8),
    };
  })()`);
}

async function checkLayout(page, label) {
  const report = await overflowReport(page);
  check(
    `${label}: no horizontal page overflow`,
    report.overflow <= 1,
    `scrollWidth=${report.scrollWidth} viewport=${report.viewport}`,
  );
  if (report.offenders.length > 0) {
    check(`${label}: no elements outside viewport`, false, JSON.stringify(report.offenders));
  } else {
    check(`${label}: no elements outside viewport`, true);
  }
  const cardOverflow = await page.evaluate(`(() => {
    const bad = [];
    for (const card of document.querySelectorAll('.card')) {
      const cardRect = card.getBoundingClientRect();
      for (const control of card.querySelectorAll('input, select, textarea, button')) {
        const rect = control.getBoundingClientRect();
        if (rect.width === 0 || rect.height === 0) continue;
        if (rect.right > cardRect.right + 1 || rect.left < cardRect.left - 1) {
          bad.push({ tag: control.tagName, id: control.id || '', cls: control.className || '' });
        }
      }
    }
    return bad.slice(0, 8);
  })()`);
  check(`${label}: controls stay inside cards`, cardOverflow.length === 0, JSON.stringify(cardOverflow));
  return report;
}

async function main() {
  await mkdir(SHOTS, { recursive: true });
  if (!ADMIN_PASSWORD) throw new Error("MC_ADMIN_PASSWORD is not set");

  const page = await PageSession.open();
  let fatal = null;
  try {
    await page.setViewport(VIEWPORT);
    await page.navigate(`${BASE}/`);
    await page.waitFor("document.querySelector('.auth-card')", "login form");

    const loginLogo = await page.evaluate(`(() => {
      const logo = document.querySelector('.auth-logo');
      return logo ? { complete: logo.complete, width: logo.naturalWidth, height: logo.naturalHeight } : null;
    })()`);
    check("login logo loads", loginLogo?.complete && loginLogo.width === 256, JSON.stringify(loginLogo));
    await page.screenshot("01-login-desktop.png");

    await page.evaluate(setValue("#password", ADMIN_PASSWORD));
    await page.evaluate("document.querySelector('.auth-card form').requestSubmit()");
    await page.waitFor("document.querySelector('.app-shell')", "authenticated shell");
    await waitForTitle(page, "Dashboard");

    const chrome = await page.evaluate(`(() => {
      const logo = document.querySelector('.brand-logo');
      const topbar = document.querySelector('.topbar');
      return {
        logo: logo ? { complete: logo.complete, width: logo.naturalWidth, height: logo.naturalHeight } : null,
        topbarTitle: Boolean(document.querySelector('.topbar-title')),
        topbarText: topbar ? topbar.textContent.trim().replace(/\\s+/g, ' ') : '',
        navLinks: document.querySelectorAll('.nav-link').length,
        activeNav: document.querySelector('.nav-link.active')?.textContent.trim() || '',
        h1: document.querySelector('.page-title')?.textContent.trim() || '',
      };
    })()`);
    check("sidebar logo loads", chrome.logo?.complete && chrome.logo.width === 256, JSON.stringify(chrome.logo));
    check("no duplicate top bar page title", chrome.topbarTitle === false, chrome.topbarText);
    check("all five navigation links are present", chrome.navLinks === 5, String(chrome.navLinks));
    check("page heading is shown once", chrome.h1 === "Dashboard" && chrome.activeNav === "Dashboard");

    const widths = {};
    const dashboardLayout = await checkLayout(page, "dashboard desktop");
    widths.dashboard = await contentWidth(page);
    await page.screenshot("02-dashboard-desktop.png");

    // Campaigns: unique heading and a deliberately long campaign name/subject.
    await openRoute(page, "/campaigns", "Campaigns");
    const campaignsHead = await page.evaluate(`(() => ({
      topbarTitle: Boolean(document.querySelector('.topbar-title')),
      titles: [...document.querySelectorAll('.page-title')].map((item) => item.textContent.trim()),
      topbarText: document.querySelector('.topbar')?.textContent.trim().replace(/\\s+/g, ' ') || '',
    }))()`);
    check("Campaigns is not repeated in the top bar", campaignsHead.topbarTitle === false && campaignsHead.topbarText === "Local admin Sign out", campaignsHead.topbarText);
    check("Campaigns page has one page heading", JSON.stringify(campaignsHead.titles) === JSON.stringify(["Campaigns"]));

    const created = await page.evaluate(`(async () => {
      const me = await (await fetch('/api/v1/auth/me', { credentials: 'same-origin' })).json();
      const payload = {
        name: ${JSON.stringify("Campaign " + "X".repeat(100) + " #1")},
        subject: ${JSON.stringify("Weekly digest " + "unbroken-token-".repeat(12))},
        body_text: ${JSON.stringify("Body " + "0123456789".repeat(40))},
        body_html: "",
        daily_limit: 500,
        rate_per_min: 60,
      };
      const response = await fetch('/api/v1/campaigns', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': me.csrf_token },
        body: JSON.stringify(payload),
      });
      const text = await response.text();
      return { ok: response.ok, status: response.status, body: text.slice(0, 300) };
    })()`);
    check("create campaign through API", created.ok, `${created.status} ${created.body}`);

    await page.navigate(`${BASE}/campaigns`);
    await waitForTitle(page, "Campaigns");
    await page.waitFor("document.querySelector('.campaign-row')", "campaign row");
    const campaignLayout = await checkLayout(page, "campaigns desktop");
    widths.campaigns = await contentWidth(page);
    await page.screenshot("03-campaigns-desktop.png");

    const campaignNameMetrics = await page.evaluate(`(() => {
      const name = document.querySelector('.campaign-name');
      const cell = name?.closest('td');
      return name && cell ? {
        nameWidth: Math.round(name.getBoundingClientRect().width),
        cellWidth: Math.round(cell.getBoundingClientRect().width),
        nameHeight: Math.round(name.getBoundingClientRect().height),
      } : null;
    })()`);
    check(
      "long campaign name wraps inside its cell",
      campaignNameMetrics && campaignNameMetrics.nameWidth <= campaignNameMetrics.cellWidth + 1,
      JSON.stringify(campaignNameMetrics),
    );

    await page.evaluate("document.querySelector('.campaign-row').click()");
    await page.waitFor("document.querySelector('.campaign-body')", "campaign detail");
    await checkLayout(page, "campaign detail desktop");
    await page.screenshot("04-campaign-detail-desktop.png");

    await openRoute(page, "/mailbox", "Mailbox");
    await checkLayout(page, "mailbox desktop");
    await page.screenshot("05-mailbox-desktop.png");

    await openRoute(page, "/assistant", "Assistant");
    await checkLayout(page, "assistant desktop");
    await page.screenshot("06-assistant-desktop.png");

    // Settings + AI connection flow.
    await openRoute(page, "/settings", "Settings");
    await page.waitFor("document.querySelector('#ai-api-key')", "API key field");
    const initialAI = await page.evaluate(`(() => {
      const card = ${aiCard};
      const advanced = card.querySelector('.ai-advanced');
      const provider = card.querySelector('#ai-provider');
      return {
        badge: ${aiBadge},
        advancedOpen: advanced ? advanced.open : null,
        providerInsideAdvanced: provider ? provider.closest('details') === advanced : null,
        connectButton: [...card.querySelectorAll('button')].some((item) => item.textContent.trim() === 'Connect'),
      };
    })()`);
    check("AI settings start unconfigured", initialAI.badge === "Not configured", initialAI.badge);
    check("advanced AI settings are collapsed", initialAI.advancedOpen === false);
    check(
      "provider/model are hidden by default",
      initialAI.advancedOpen === false && initialAI.providerInsideAdvanced === true,
    );
    check("single Connect action is shown", initialAI.connectButton === true);
    await checkLayout(page, "settings desktop");
    widths.settings = await contentWidth(page);
    await page.screenshot("07-settings-desktop.png");

    // Use the local mock endpoint through advanced settings.
    await page.evaluate(`(() => {
      const card = ${aiCard};
      card.querySelector('.ai-advanced').open = true;
      return true;
    })()`);
    await page.evaluate(setValue("#ai-base-url", MOCK_LLM));
    await sleep(100);
    await page.evaluate(setValue("#ai-api-key", BAD_KEY));
    const enteredBadKey = await page.evaluate("document.querySelector('#ai-api-key').value");
    check("API key can be typed", enteredBadKey === BAD_KEY, "length=" + enteredBadKey.length);
    check("click Connect with an invalid key", await page.evaluate(aiButton("Connect")));
    await page.waitFor(`(${aiCard})?.querySelector('.ai-test-result.error')`, "AI error state", 20000);
    const failedAI = await page.evaluate(`(() => {
      const card = ${aiCard};
      return {
        badge: ${aiBadge},
        error: card.querySelector('.ai-test-result.error')?.textContent.trim() || '',
        keyValue: card.querySelector('#ai-api-key').value,
        formError: card.querySelector('.form-error')?.textContent.trim() || '',
      };
    })()`);
    check("invalid key shows Not connected", failedAI.badge === "Not connected", failedAI.badge);
    check("invalid key shows a readable error", failedAI.error.length > 0, failedAI.error);
    check("API key input is cleared after save", failedAI.keyValue === "", "length=" + failedAI.keyValue.length);
    await page.screenshot("08-settings-ai-error.png");
    await page.waitFor("document.querySelectorAll('.toast').length === 0", "error toast to clear", 8000);

    await page.evaluate(setValue("#ai-api-key", VALID_OPENAI_KEY));
    check("click Save & test with a valid key", await page.evaluate(aiButton("Save & test")));
    await page.waitFor(`(${aiCard})?.querySelector('.ai-test-result.ok')`, "AI connected state", 20000);
    const connectedAI = await page.evaluate(`(() => {
      const card = ${aiCard};
      return {
        badge: ${aiBadge},
        text: card.querySelector('.ai-test-result.ok')?.textContent.trim() || '',
        keyValue: card.querySelector('#ai-api-key').value,
        body: document.body.innerHTML,
        local: JSON.stringify(localStorage) + JSON.stringify(sessionStorage),
      };
    })()`);
    check("valid key shows Connected", connectedAI.badge === "Connected", connectedAI.badge);
    check("Connected state names the model", /Connected.*gpt-4o-mini/.test(connectedAI.text), connectedAI.text);
    check("API key remains out of the DOM", !connectedAI.body.includes(VALID_OPENAI_KEY) && !connectedAI.local.includes(VALID_OPENAI_KEY));
    await page.screenshot("09-settings-ai-connected.png");

    const status = await page.evaluate(`(async () => (await fetch('/api/v1/llm/status', { credentials: 'same-origin' })).json())()`);
    check("status reports OpenAI and configured", status.configured === true && status.provider === "openai" && status.model === "gpt-4o-mini", JSON.stringify(status));
    check("status response never exposes the key", !JSON.stringify(status).includes(VALID_OPENAI_KEY) && !Object.hasOwn(status, "api_key"));

    // Reloading Settings must auto-check the saved provider and stay connected.
    await page.navigate(`${BASE}/settings`);
    await waitForTitle(page, "Settings");
    await page.waitFor("document.querySelector('#ai-api-key')", "API key field after reload");
    await page.waitFor(`(${aiCard})?.querySelector('.ai-test-result.ok')`, "automatic connection check", 20000);
    const autoChecked = await page.evaluate(`(() => {
      const card = ${aiCard};
      return {
        badge: ${aiBadge},
        advancedOpen: card.querySelector('.ai-advanced').open,
        text: card.querySelector('.ai-test-result.ok')?.textContent.trim() || '',
      };
    })()`);
    check("Settings auto-checks a saved provider", autoChecked.badge === "Connected", autoChecked.badge);
    check("advanced settings remain collapsed after reload", autoChecked.advancedOpen === false);

    // Provider inference for Anthropic keys.
    await page.evaluate(`(() => { ${aiCard}.querySelector('.ai-advanced').open = true; return true; })()`);
    await page.evaluate(setValue("#ai-base-url", MOCK_LLM));
    await page.evaluate(setValue("#ai-api-key", VALID_ANTHROPIC_KEY));
    check("click Save & test with an Anthropic key", await page.evaluate(aiButton("Save & test")));
    await page.waitFor(`(${aiCard})?.querySelector('.ai-test-result.ok')`, "Anthropic connected state", 20000);
    const anthropic = await page.evaluate(`(() => {
      const card = ${aiCard};
      return {
        badge: ${aiBadge},
        text: card.querySelector('.ai-test-result.ok')?.textContent.trim() || '',
        provider: card.querySelector('#ai-provider').value,
        model: card.querySelector('#ai-model').value,
      };
    })()`);
    check("Anthropic key is detected automatically", anthropic.provider === "anthropic" && anthropic.model === "claude-sonnet-4-5", JSON.stringify(anthropic));
    check("Anthropic connection is reported", anthropic.badge === "Connected" && anthropic.text.includes("claude-sonnet-4-5"), anthropic.text);
    await page.waitFor("document.querySelectorAll('.toast').length === 0", "success toast to clear", 8000);
    await page.screenshot("10-settings-ai-anthropic.png");

    const statusAnthropic = await page.evaluate(`(async () => (await fetch('/api/v1/llm/status', { credentials: 'same-origin' })).json())()`);
    check("status reports Anthropic", statusAnthropic.provider === "anthropic" && statusAnthropic.model === "claude-sonnet-4-5", JSON.stringify(statusAnthropic));

    // Responsive checks at a phone viewport.
    await page.waitFor("document.querySelectorAll('.toast').length === 0", "toasts to clear", 8000);
    await page.setViewport(MOBILE);
    await openRoute(page, "/", "Dashboard");
    await waitForTitle(page, "Dashboard");
    await checkLayout(page, "dashboard mobile");
    const mobileChrome = await page.evaluate(`(() => {
      const sidebar = document.querySelector('.sidebar').getBoundingClientRect();
      const links = [...document.querySelectorAll('.nav-link')].map((link) => {
        const rect = link.getBoundingClientRect();
        return { width: Math.round(rect.width), height: Math.round(rect.height), right: Math.round(rect.right) };
      });
      return {
        accountChip: getComputedStyle(document.querySelector('.account-chip')).display,
        sidebarWidth: Math.round(sidebar.width),
        links,
        logoWidth: Math.round(document.querySelector('.brand-logo').getBoundingClientRect().width),
        viewAnimation: getComputedStyle(document.querySelector('.view')).animationName,
      };
    })()`);
    check(
      "all mobile navigation icons are visible",
      mobileChrome.links.length === 5 &&
        mobileChrome.links.every((link) => link.width > 0 && link.height > 0 && link.right <= mobileChrome.sidebarWidth + 1),
      JSON.stringify(mobileChrome.links),
    );
    check("mobile logo is compact", mobileChrome.logoWidth === 32, String(mobileChrome.logoWidth));
    check("view transition is animated", mobileChrome.viewAnimation === "view-in", mobileChrome.viewAnimation);
    await page.screenshot("11-dashboard-mobile.png");

    await openRoute(page, "/campaigns", "Campaigns");
    await page.waitFor("document.querySelector('.campaign-row')", "campaign row");
    await checkLayout(page, "campaigns mobile");
    await page.screenshot("12-campaigns-mobile.png");

    await page.evaluate(`(() => {
      const button = [...document.querySelectorAll('.page-actions button')].find((item) => item.textContent.trim() === 'New campaign');
      if (!button) return false;
      button.click();
      return true;
    })()`);
    await page.waitFor("document.querySelector('.modal-backdrop')", "campaign modal");
    const modalMetrics = await page.evaluate(`(() => {
      const modal = document.querySelector('.modal');
      const backdrop = document.querySelector('.modal-backdrop');
      const head = document.querySelector('.modal-head');
      const rect = (element) => {
        const value = element.getBoundingClientRect();
        return {
          top: Math.round(value.top),
          bottom: Math.round(value.bottom),
          left: Math.round(value.left),
          right: Math.round(value.right),
          width: Math.round(value.width),
          height: Math.round(value.height),
        };
      };
      return {
        modal: rect(modal),
        backdrop: rect(backdrop),
        head: rect(head),
        viewport: { width: window.innerWidth, height: window.innerHeight },
        animation: getComputedStyle(modal).animationName,
      };
    })()`);
    check(
      "campaign modal fits the phone viewport",
      modalMetrics.modal.left >= 0 &&
        modalMetrics.modal.right <= modalMetrics.viewport.width &&
        modalMetrics.modal.top >= 0 &&
        modalMetrics.modal.bottom <= modalMetrics.viewport.height &&
        modalMetrics.head.top >= 0,
      JSON.stringify(modalMetrics),
    );
    check(
      "modal backdrop covers the whole viewport",
      modalMetrics.backdrop.top === 0 &&
        modalMetrics.backdrop.left === 0 &&
        modalMetrics.backdrop.right === modalMetrics.viewport.width,
      JSON.stringify(modalMetrics.backdrop),
    );
    check("modal transition is animated", modalMetrics.animation === "modal-in", modalMetrics.animation);
    await checkLayout(page, "campaign modal mobile");
    await page.screenshot("13-campaign-modal-mobile.png");
    await page.evaluate("document.querySelector('.modal button[aria-label=\"Close\"]').click()");
    await page.waitFor("!document.querySelector('.modal-backdrop')", "modal closed");

    await openRoute(page, "/settings", "Settings");
    await page.waitFor("document.querySelector('#ai-api-key')", "API key field");
    await page.waitFor(`(${aiCard})?.querySelector('.ai-test-result.ok')`, "automatic connection check on mobile", 20000);
    await page.evaluate("document.querySelector('#ai-api-key').scrollIntoView({ block: 'center' })");
    await sleep(200);
    await checkLayout(page, "settings mobile");
    await page.screenshot("14-settings-ai-mobile.png");

    const contentWidths = Object.values(widths);
    check(
      "content width is consistent across desktop pages",
      contentWidths.length === 3 && contentWidths.every((value) => Math.abs(value - contentWidths[0]) <= 1),
      JSON.stringify(widths),
    );

    const normalDuration = await page.evaluate(
      "parseFloat(getComputedStyle(document.querySelector('.view')).animationDuration)",
    );
    await page.send("Emulation.setEmulatedMedia", {
      features: [{ name: "prefers-reduced-motion", value: "reduce" }],
    });
    await sleep(100);
    const reducedDuration = await page.evaluate(
      "parseFloat(getComputedStyle(document.querySelector('.view')).animationDuration)",
    );
    check(
      "prefers-reduced-motion disables animations",
      normalDuration > 0 && reducedDuration <= 0.02,
      `normal=${normalDuration}s reduced=${reducedDuration}s`,
    );
    await page.send("Emulation.setEmulatedMedia", { features: [] });

    check("no uncaught JavaScript exceptions", jsErrors.length === 0, jsErrors.join(" | "));
    check("no failed network requests", networkFailures.length === 0, networkFailures.join(" | "));
  } catch (error) {
    fatal = error;
    check("test run completed without fatal errors", false, redact(error.message));
  } finally {
    await page.close();
  }

  const failed = results.filter((item) => !item.ok);
  const summary = {
    base: BASE,
    mock: MOCK_LLM,
    viewport: VIEWPORT,
    mobile: MOBILE,
    passed: results.length - failed.length,
    failed: failed.length,
    results,
    jsErrors: jsErrors.map(redact),
    networkFailures: networkFailures.map(redact),
  };
  await writeFile(path.join(HERE, "data", "ui-results.json"), JSON.stringify(summary, null, 2));
  console.log(`\n${summary.passed}/${results.length} checks passed`);
  if (failed.length > 0) {
    console.error(`Failed: ${failed.map((item) => item.name).join(", ")}`);
    process.exitCode = 1;
  }
  if (fatal) process.exitCode = 1;
}

main().catch((error) => {
  console.error(redact(error.stack || error.message));
  process.exit(1);
});
