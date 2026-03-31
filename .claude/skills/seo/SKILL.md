---
name: seo
description: >
  Comprehensive SEO analysis for any website. Full site audits, single-page analysis,
  technical SEO (crawlability, indexability, Core Web Vitals with INP), schema markup,
  E-E-A-T content quality, image optimization, sitemap analysis, GEO for AI search,
  local SEO, and strategic planning. Uses Wove browser tools for real-page execution.
  Triggers on: "SEO", "audit", "schema", "Core Web Vitals", "sitemap", "E-E-A-T",
  "AI Overviews", "GEO", "technical SEO", "content quality", "structured data",
  "local SEO", "page speed".
user-invokable: true
argument-hint: "[command] [url]"
---

# SEO: Universal SEO Analysis Skill for Wove

Comprehensive SEO analysis across all industries. Orchestrates 13 specialized sub-skills
using Wove's browser tools for real-page execution with JavaScript context.

## Wove Tools Available

| Tool | Purpose |
|------|---------|
| `web_open` | Open browser widget with URL |
| `web_read_text` | Clean text extraction by CSS selector |
| `web_read_html` | Raw HTML extraction (meta tags, head, structure) |
| `web_seo_audit` | Full audit: JSON-LD, OG, meta, headings, alt text, links |
| `web_exec_js` | Execute JavaScript in page context (Performance API, DOM) |
| `web_click` | Click elements by CSS selector |
| `web_type_input` | Type into inputs (test search, forms) |
| `web_press_key` | Simulate key presses (Enter, Tab, Escape) |
| `term_run_command` | Terminal commands (curl, DNS, SSL checks) |

**Key advantage over CLI-based SEO tools:** Real browser context with JavaScript execution.
web_exec_js can measure Performance API, count DOM elements, check computed styles, run
PerformanceObserver -- things curl/wget cannot do.

## Quick Reference

| Command | What it does |
|---------|-------------|
| `/seo audit <url>` | Full website audit with category delegation |
| `/seo page <url>` | Deep single-page analysis |
| `/seo sitemap <url or generate>` | Analyze or generate XML sitemaps |
| `/seo schema <url>` | Detect, validate, and generate Schema.org markup |
| `/seo images <url>` | Image optimization analysis |
| `/seo technical <url>` | Technical SEO audit (9 categories) |
| `/seo content <url>` | E-E-A-T and content quality analysis |
| `/seo geo <url>` | AI Overviews / Generative Engine Optimization |
| `/seo local <url>` | Local SEO (GBP, NAP, citations, reviews, local schema) |
| `/seo plan <business-type>` | Strategic SEO planning |
| `/seo programmatic [url\|plan]` | Programmatic SEO analysis and planning |
| `/seo hreflang [url]` | Hreflang/i18n SEO audit and generation |

## Orchestration Logic

When the user invokes `/seo audit`:
1. Open URL with `web_open`
2. Run `web_seo_audit` on homepage for baseline (JSON-LD, OG, meta, headings, links)
3. Detect business type from homepage signals
4. Detect industry vertical (for local businesses)
5. Run sub-skill checks by category
6. Aggregate into SEO Health Score (0-100)
7. Generate prioritized action plan (Critical > High > Medium > Low)

For individual commands, load the relevant sub-skill directly.

## Industry Detection

Detect business type from homepage signals:
- **SaaS**: /pricing, /features, /integrations, /docs, "free trial", "sign up"
- **Local Service**: phone number, address, service area, Google Maps embed -> auto-run seo-local
- **E-commerce**: /products, /collections, /cart, "add to cart", Product schema
- **Publisher**: /blog, /articles, Article schema, author pages, publication dates
- **Corporate**: /case-studies, /portfolio, /industries, client logos

For local businesses, detect vertical:
- **Restaurant**: /menu, reservations, cuisine types
- **Healthcare**: insurance, appointments, NPI, HIPAA
- **Legal**: attorney, practice areas, bar admission
- **Home Services**: service area, emergency, licensed/insured
- **Real Estate**: listings, MLS, properties, agent bio
- **Automotive**: inventory, VIN, test drive, dealership

## Quality Gates

Read `references/quality-gates.md` for thresholds per page type.
Hard rules:
- WARNING at 30+ location pages (enforce 60%+ unique content)
- HARD STOP at 50+ location pages (require user justification)
- Never recommend HowTo schema (deprecated Sept 2023)
- FAQPage: only gov/health for Google rich results (Aug 2023); existing on commercial sites -> Info priority (AI citation benefit); new FAQPage -> not recommended for Google
- All Core Web Vitals use INP, never FID
- WARNING at 100+ programmatic pages, HARD STOP at 500+ without audit

## Reference Files

Load on-demand as needed (do NOT load all at startup):
- `references/cwv-thresholds.md`: Core Web Vitals thresholds, LCP subparts, measurement
- `references/schema-types.md`: All supported schema types with deprecation status (Feb 2026)
- `references/eeat-framework.md`: E-E-A-T criteria (Sept 2025 QRG + Dec 2025 core update)
- `references/quality-gates.md`: Content length minimums, uniqueness thresholds
- `references/local-seo-signals.md`: Local ranking factors, review benchmarks, citation tiers
- `references/local-schema-types.md`: LocalBusiness subtypes, industry schema, citation sources

## Scoring Methodology

### SEO Health Score (0-100)

| Category | Weight |
|----------|--------|
| Technical SEO | 22% |
| Content Quality | 23% |
| On-Page SEO | 20% |
| Schema / Structured Data | 10% |
| Performance (CWV) | 10% |
| AI Search Readiness | 10% |
| Images | 5% |

### Priority Levels
- **Critical**: Blocks indexing or causes penalties (immediate fix)
- **High**: Significantly impacts rankings (fix within 1 week)
- **Medium**: Optimization opportunity (fix within 1 month)
- **Low**: Nice to have (backlog)

## Sub-Skills

1. **seo-audit** -- Full website audit
2. **seo-page** -- Deep single-page analysis
3. **seo-technical** -- Technical SEO (9 categories)
4. **seo-content** -- E-E-A-T and content quality
5. **seo-schema** -- Schema markup detection and generation
6. **seo-images** -- Image optimization
7. **seo-sitemap** -- Sitemap analysis and generation
8. **seo-geo** -- AI Overviews / GEO optimization
9. **seo-local** -- Local SEO (GBP, NAP, citations, reviews)
10. **seo-plan** -- Strategic planning with industry templates
11. **seo-programmatic** -- Programmatic SEO analysis
12. **seo-hreflang** -- Hreflang/i18n audit and generation
