window.CODEXSCOPE_SAMPLE_DATA = {
  sample: true,
  generatedAt: "2026-05-09 00:16:00",
  windowDays: 30,
  pricingRules: [
    { label: "gpt-5.5", patterns: ["gpt-5.5"], input: 5.00, cached: 0.50, output: 30.00 },
    { label: "gpt-5.4 mini", patterns: ["gpt-5.4-mini", "gpt_5.4_mini", "gpt 5.4 mini"], input: 0.75, cached: 0.075, output: 4.50 },
    { label: "gpt-5.4", patterns: ["gpt-5.4"], input: 2.50, cached: 0.25, output: 15.00 },
    { label: "gpt-5.3 codex spark", patterns: ["gpt-5.3-codex-spark", "gpt_5.3_codex_spark", "gpt 5.3 codex spark"], input: 1.75, cached: 0.175, output: 14.00 },
    { label: "gpt-5.3 codex", patterns: ["gpt-5.3-codex", "gpt_5.3_codex", "gpt 5.3 codex"], input: 1.75, cached: 0.175, output: 14.00 },
    { label: "gpt-5.2 codex", patterns: ["gpt-5.2-codex", "gpt_5.2_codex", "gpt 5.2 codex"], input: 1.75, cached: 0.175, output: 14.00 },
    { label: "gpt-5 / 5.1 codex", patterns: ["gpt-5.1-codex", "gpt_5.1_codex", "gpt 5.1 codex", "gpt-5-codex", "gpt_5_codex", "gpt 5 codex", "gpt-5"], input: 1.25, cached: 0.125, output: 10.00 }
  ],
  availableRange: {
    start: 1778170560000,
    end: 1778256960000
  },
  sessionsCatalog: {
    sample_01: { name: "CodexScope", model: "gpt-5.5" },
    sample_02: { name: "Design Review", model: "gpt-5.4" },
    sample_03: { name: "CLI Agent", model: "gpt-5.3-codex-spark" }
  },
  records: [
    [1778256000000, "sample_01", "gpt-5.5", 920000, 850000, 18000, 4800, 942800],
    [1778256120000, "sample_02", "gpt-5.4", 780000, 720000, 16000, 4100, 800100],
    [1778256240000, "sample_03", "gpt-5.3-codex-spark", 650000, 560000, 12000, 3300, 665300],
    [1778256360000, "sample_01", "gpt-5.5", 610000, 570000, 11000, 2200, 623200],
    [1778256480000, "sample_02", "gpt-5.4", 540000, 490000, 9000, 1800, 550800],
    [1778256600000, "sample_01", "gpt-5.5", 120000, 90000, 6000, 1200, 127200],
    [1778256720000, "sample_03", "gpt-5.3-codex-spark", 85000, 60000, 5000, 900, 90900],
    [1778256840000, "sample_01", "gpt-5.5", 3600000, 3300000, 42000, 8600, 3650600],
    [1778256900000, "sample_02", "gpt-5.4", 2100000, 1950000, 31000, 6200, 2137200],
    [1778256960000, "sample_01", "gpt-5.5", 1850000, 1720000, 25000, 5100, 1880100]
  ],
  ttfbRecords: [
    [1778256000000, "sample_01", "gpt-5.5", 13200],
    [1778256120000, "sample_02", "gpt-5.4", 15400],
    [1778256240000, "sample_03", "gpt-5.3-codex-spark", 8600],
    [1778256840000, "sample_01", "gpt-5.5", 18100]
  ],
  failureRecords: [],
  summary: {
    totalTokens: 14268300,
    totalTokensLabel: "14.27M",
    inputTokens: 13255000,
    inputLabel: "13.26M",
    cachedTokens: 12210000,
    cachedLabel: "12.21M",
    outputTokens: 175000,
    outputLabel: "175K",
    reasoningTokens: 38100,
    reasoningLabel: "38K",
    requests: 10,
    requestsLabel: "10",
    failures: 0,
    successRate: 100,
    successRateLabel: "100.0%",
    cacheHit: 92.1,
    cacheHitLabel: "92.1%",
    peakTokens: 3650600,
    peakLabel: "3.65M",
    peakTime: "00:14",
    peakTpmLabel: "1.83M TPM"
  },
  limits: {
    planType: "pro",
    primaryUsed: 38,
    primaryRemaining: 62,
    primaryReset: "01:21",
    primaryWindowMinutes: 300,
    secondaryUsed: 19,
    secondaryRemaining: 81,
    secondaryReset: "02:12",
    secondaryWindowMinutes: 10080,
    rateLimitReachedType: null
  },
  trend: [],
  sessions: [],
  models: [],
  risk: [],
  coverage: [
    { metric: "真实额度", source: "sample rate_limits", status: "ok" },
    { metric: "Token 消耗", source: "sample token usage", status: "ok" },
    { metric: "会话排行", source: "sample sessions", status: "ok" },
    { metric: "模型排行", source: "sample models", status: "ok" },
    { metric: "峰值速率", source: "sample buckets", status: "ok" },
    { metric: "缓存命中", source: "sample cached / input", status: "ok" }
  ]
};
