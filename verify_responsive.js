const { chromium } = require("playwright");
const path = require("path");

const viewports = [
  [1560, 747],
  [1440, 747],
  [1280, 747],
  [1440, 900],
  [1280, 800],
  [1215, 807],
  [1180, 720],
  [1150, 650],
  [1100, 700],
  [1024, 700],
  [900, 700],
  [768, 900],
  [430, 900],
  [390, 844],
];

const pageUrl = "file://" + path.resolve(__dirname, "index.html");

function isVisibleInViewport(rect, height) {
  return rect.bottom > 0 && rect.top < height;
}

(async () => {
  const browser = await chromium.launch({ headless: true });
  const failures = [];
  const reports = [];

  for (const [width, height] of viewports) {
    const page = await browser.newPage({
      viewport: { width, height },
      deviceScaleFactor: 1,
    });
    await page.goto(pageUrl, { waitUntil: "load" });
    await page.screenshot({
      path: path.join(__dirname, `verify-${width}x${height}.png`),
      fullPage: false,
    });

    const report = await page.evaluate(() => {
      const app = document.querySelector(".app");
      const visiblePanels = Array.from(document.querySelectorAll(".panel,.metric,.coverage"))
        .filter((el) => {
          const rect = el.getBoundingClientRect();
          return rect.bottom > 0 && rect.top < innerHeight;
        })
        .map((el) => (el.querySelector("h2,h3")?.textContent || el.textContent || "")
          .trim()
          .replace(/\s+/g, " ")
          .slice(0, 32));

      const textOverflows = Array.from(document.querySelectorAll("*"))
        .filter((el) => {
          const style = getComputedStyle(el);
          return el !== app
            && el.scrollWidth > el.clientWidth + 1
            && style.overflow !== "visible"
            && style.textOverflow !== "ellipsis";
        })
        .map((el) => ({
          tag: el.tagName,
          cls: String(el.className),
          id: el.id,
          text: (el.textContent || "").trim().replace(/\s+/g, " ").slice(0, 80),
        }));

      const clippedInPanels = Array.from(document.querySelectorAll(".panel,.metric,.coverage")).flatMap((panel) => {
        const panelRect = panel.getBoundingClientRect();
        return Array.from(panel.querySelectorAll("*"))
          .filter((el) => {
            if (el.closest(".chart-wrap")) return false;
            const rect = el.getBoundingClientRect();
            return rect.width > 1
              && rect.height > 1
              && rect.bottom > panelRect.top
              && rect.top < panelRect.bottom
              && (rect.left < panelRect.left - 1 || rect.right > panelRect.right + 1);
          })
          .map((el) => ({
            panel: (panel.querySelector("h2,h3")?.textContent || "").trim(),
            tag: el.tagName,
            cls: String(el.className),
            text: (el.textContent || "").trim().replace(/\s+/g, " ").slice(0, 80),
          }));
      });

      return {
        scrollWidth: document.documentElement.scrollWidth,
        scrollHeight: document.documentElement.scrollHeight,
        topbarHeight: Math.round(document.querySelector(".topbar").getBoundingClientRect().height),
        visiblePanels,
        textOverflows,
        clippedInPanels,
        copyFlags: {
          hasFakeForecast: document.body.innerText.includes("预测"),
          hasFakeViewAll: document.body.innerText.includes("查看全部"),
          hasNonCodexSources: /New API|CPA|OpenAI API/.test(document.body.innerText),
        },
        semanticControls: Array.from(document.querySelectorAll(".tab,.link"))
          .map((el) => ({ tag: el.tagName, cls: String(el.className), text: (el.textContent || "").trim() }))
          .filter((item) => item.tag !== "BUTTON"),
        hiddenVisible: Array.from(document.querySelectorAll("[hidden]"))
          .filter((el) => {
            const rect = el.getBoundingClientRect();
            return rect.width > 0 || rect.height > 0;
          })
          .map((el) => ({ tag: el.tagName, cls: String(el.className), id: el.id, text: (el.textContent || "").trim() })),
        distributionState: {
          bars: document.querySelectorAll("#distributionChart .dist-bar").length,
          empty: !!document.querySelector("#distributionChart .dist-empty"),
          values: Array.from(document.querySelectorAll("#distributionChart .dist-bar-value"))
            .map((el) => el.textContent.trim())
            .filter(Boolean).length,
        },
        costState: {
          hasUsdDefault: document.querySelector(".currency-mode[data-currency='USD']")?.classList.contains("active")
            && (document.querySelector(".cost-total")?.textContent || "").includes("$"),
          hasCnyToggle: !!document.querySelector(".currency-mode[data-currency='CNY']"),
        },
        trendPaths: Array.from(document.querySelectorAll("#trendChart path[data-series]"))
          .map((path) => {
            const nums = (path.getAttribute("d")?.match(/-?\d+(?:\.\d+)?/g) || []).map(Number);
            const ys = [];
            for (let index = 1; index < nums.length; index += 2) ys.push(nums[index]);
            return {
              series: path.getAttribute("data-series"),
              minY: ys.length ? Math.min(...ys) : null,
              maxY: ys.length ? Math.max(...ys) : null,
            };
          }),
      };
    });

    const interactions = await page.evaluate(() => {
      const sessionButton = document.querySelector("#toggleSessions");
      const modelButton = document.querySelector("#toggleModels");
      const count = (selector) => document.querySelectorAll(selector).length;
      const beforeSessions = count("#sessionList .session-row");
      const sessionsToggleNotNeeded = !sessionButton || sessionButton.hidden;
      sessionButton?.click();
      const afterSessions = count("#sessionList .session-row");
      sessionButton?.click();
      const beforeModels = count("#modelList .model-row");
      const modelsToggleNotNeeded = !modelButton || modelButton.hidden;
      modelButton?.click();
      const afterModels = count("#modelList .model-row");
      modelButton?.click();
      const tab = document.querySelector('.tab[data-scroll-target="sessionPanel"]');
      tab?.click();
      const dayButton = document.querySelector('.period-btn[data-range="24h"]');
      dayButton?.click();
      const rangeAfterDay = document.querySelector("#rangeSummary")?.textContent?.trim() || "";
      const dayActivated = !dayButton || (dayButton.classList.contains("active") && rangeAfterDay.includes("最近24小时"));
      const sevenButton = document.querySelector('.period-btn[data-range="7"]');
      sevenButton?.click();
      const rangeAfterSeven = document.querySelector("#rangeSummary")?.textContent?.trim() || "";
      const distributionAfterSeven = count("#distributionChart .dist-bar") + count("#distributionChart .dist-empty");
      const sevenActivated = !sevenButton || (sevenButton.classList.contains("active") && rangeAfterSeven.includes("7天"));
      const tokenModeButton = document.querySelector('.dist-mode[data-dist-mode="tokens"]');
      tokenModeButton?.click();
      const tokenModeSummary = document.querySelector("#rangeSummary")?.textContent?.trim() || "";
      const tokenModeBars = count("#distributionChart .dist-bar-fill.token");
      const cnyButton = document.querySelector('.currency-mode[data-currency="CNY"]');
      cnyButton?.click();
      const cnySwitches = !cnyButton || (cnyButton.classList.contains("active") && (document.querySelector(".cost-total")?.textContent || "").includes("¥"));
      const usdButton = document.querySelector('.currency-mode[data-currency="USD"]');
      usdButton?.click();
      const usdSwitches = !usdButton || (usdButton.classList.contains("active") && (document.querySelector(".cost-total")?.textContent || "").includes("$"));
      const inputLegend = document.querySelector('.legend-item[data-series="input"]');
      const ensureActive = (button) => {
        if (button && button.getAttribute("aria-pressed") !== "true") button.click();
      };
      const lineSpread = (series) => {
        const d = document.querySelector(`#trendChart path[data-series="${series}"]`)?.getAttribute("d") || "";
        const nums = (d.match(/-?\d+(?:\.\d+)?/g) || []).map(Number);
        const ys = [];
        for (let index = 1; index < nums.length; index += 2) ys.push(nums[index]);
        return ys.length ? Math.max(...ys) - Math.min(...ys) : 0;
      };
      const totalLegend = document.querySelector('.legend-item[data-series="total"]');
      const reasoningLegend = document.querySelector('.legend-item[data-series="reasoning"]');
      const defaultLinearScale = document.querySelector('.trend-scale[data-trend-scale="linear"]')?.classList.contains("active");
      const cumulativeButton = document.querySelector('.trend-mode[data-trend-mode="cumulative"]');
      const intervalButton = document.querySelector('.trend-mode[data-trend-mode="interval"]');
      const defaultCumulativeMode = !cumulativeButton
        || (cumulativeButton.classList.contains("active") && (document.querySelector("#chartMeta")?.textContent?.trim() || "").includes("累计"));
      const logScaleButton = document.querySelector('.trend-scale[data-trend-scale="log"]');
      logScaleButton?.click();
      ensureActive(totalLegend);
      ensureActive(inputLegend);
      ensureActive(reasoningLegend);
      const inputSpread = lineSpread("input");
      const inputActive = !inputLegend || inputLegend.getAttribute("aria-pressed") === "true";
      const totalActive = !totalLegend || totalLegend.getAttribute("aria-pressed") === "true";
      const reasoningSpread = lineSpread("reasoning");
      const reasoningActive = !reasoningLegend || reasoningLegend.getAttribute("aria-pressed") === "true";
      const activeTrendLines = count('#trendChart path[data-series]');
      const logScaleActive = !logScaleButton || logScaleButton.classList.contains("active");
      const trendMultiSeries = totalActive && inputActive && reasoningActive && activeTrendLines >= 3 && inputSpread > 8 && reasoningSpread > 8 && logScaleActive;
      intervalButton?.click();
      const intervalModeSwitches = !intervalButton
        || (intervalButton.classList.contains("active") && (document.querySelector("#chartMeta")?.textContent?.trim() || "").includes("分时峰值"));
      const customButton = document.querySelector('.period-btn[data-range="custom"]');
      customButton?.click();
      const customRange = document.querySelector("#customRange");
      const startDate = document.querySelector("#startDate");
      const endDate = document.querySelector("#endDate");
      if (startDate && endDate) {
        startDate.value = startDate.min || startDate.value;
        endDate.value = endDate.max || endDate.value;
        endDate.dispatchEvent(new Event("change", { bubbles: true }));
      }
      const rangeAfterCustom = document.querySelector("#rangeSummary")?.textContent?.trim() || "";
      return {
        sessionsExpandable: sessionsToggleNotNeeded || afterSessions > beforeSessions,
        modelsExpandable: modelsToggleNotNeeded || afterModels > beforeModels,
        tabActivates: !tab || tab.classList.contains("active"),
        dateDayActivates: dayActivated,
        dateSevenActivates: sevenActivated,
        distributionRendersAfterDateChange: distributionAfterSeven > 0,
        distributionModeSwitches: !tokenModeButton || (tokenModeButton.classList.contains("active") && tokenModeSummary.includes("Token") && tokenModeBars > 0),
        currencyCnySwitches: cnySwitches,
        currencyUsdSwitches: usdSwitches,
        defaultLinearScale,
        trendMultiSeries,
        defaultCumulativeMode,
        intervalModeSwitches,
        customRangeReveals: !customButton || (customButton.classList.contains("active") && customRange && !customRange.hidden),
        customRangeComputes: !customButton || rangeAfterCustom.includes("至"),
      };
    });

    const minVisiblePanels = width >= 700 ? 9 : 4;
    const viewportLabel = `${width}x${height}`;
    const issues = [];
    if (report.scrollWidth > width) issues.push(`horizontal scroll ${report.scrollWidth} > ${width}`);
    if (report.textOverflows.length) issues.push(`${report.textOverflows.length} text overflows`);
    if (report.clippedInPanels.length) issues.push(`${report.clippedInPanels.length} panel clipping issues`);
    if (report.visiblePanels.length < minVisiblePanels) issues.push(`only ${report.visiblePanels.length}/${minVisiblePanels} visible panels`);
    if (report.copyFlags.hasFakeForecast) issues.push("fake forecast copy remains");
    if (report.copyFlags.hasFakeViewAll) issues.push("fake view-all copy remains");
    if (report.copyFlags.hasNonCodexSources) issues.push("non-Codex source copy remains");
    if (report.semanticControls.length) issues.push(`${report.semanticControls.length} non-button interactive-looking controls`);
    if (report.hiddenVisible.length) issues.push(`${report.hiddenVisible.length} hidden controls are still visible`);
    if (report.trendPaths.some((path) => path.maxY !== null && path.maxY > 194.5)) issues.push("trend curve renders below zero axis");
    if (!report.distributionState.bars && !report.distributionState.empty) issues.push("distribution chart renders no state");
    if (report.distributionState.bars && report.distributionState.values < report.distributionState.bars) issues.push("distribution bars missing numeric values");
    if (!report.costState.hasUsdDefault) issues.push("cost card does not default to USD");
    if (!report.costState.hasCnyToggle) issues.push("cost card missing CNY toggle");
    if (!interactions.sessionsExpandable) issues.push("session expand control does not expand");
    if (!interactions.modelsExpandable) issues.push("model expand control does not expand");
    if (!interactions.tabActivates) issues.push("navigation tab does not activate");
    if (!interactions.dateDayActivates) issues.push("24-hour date filter does not activate");
    if (!interactions.dateSevenActivates) issues.push("7-day date filter does not activate");
    if (!interactions.distributionRendersAfterDateChange) issues.push("distribution chart does not rerender after date filter");
    if (!interactions.distributionModeSwitches) issues.push("distribution metric toggle does not switch to token mode");
    if (!interactions.currencyCnySwitches) issues.push("cost currency toggle does not switch to CNY");
    if (!interactions.currencyUsdSwitches) issues.push("cost currency toggle does not switch back to USD");
    if (!interactions.defaultLinearScale) issues.push("trend scale does not default to linear");
    if (!interactions.trendMultiSeries) issues.push("trend chart does not show multiple visible curves together");
    if (!interactions.defaultCumulativeMode) issues.push("trend mode does not default to cumulative");
    if (!interactions.intervalModeSwitches) issues.push("trend mode does not switch to interval");
    if (!interactions.customRangeReveals) issues.push("custom date range does not reveal inputs");
    if (!interactions.customRangeComputes) issues.push("custom date range does not compute a range label");

    reports.push({
      viewport: viewportLabel,
      scroll: `${report.scrollWidth}x${report.scrollHeight}`,
      topbar: report.topbarHeight,
      visiblePanels: report.visiblePanels.length,
      issues,
    });
    if (issues.length) failures.push({ viewport: viewportLabel, issues, details: report });

    await page.close();
  }

  await browser.close();

  console.table(reports);
  if (failures.length) {
    console.error(JSON.stringify(failures, null, 2));
    process.exit(1);
  }
})();
