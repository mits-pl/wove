// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/woveterm/wove/pkg/aiusechat/uctypes"
	"github.com/woveterm/wove/pkg/waveobj"
	"github.com/woveterm/wove/pkg/wcore"
	"github.com/woveterm/wove/pkg/wshrpc"
	"github.com/woveterm/wove/pkg/wshrpc/wshclient"
	"github.com/woveterm/wove/pkg/wshutil"
	"github.com/woveterm/wove/pkg/wstore"
)

type WebNavigateToolInput struct {
	WidgetId string `json:"widget_id"`
	Url      string `json:"url"`
}

func parseWebNavigateInput(input any) (*WebNavigateToolInput, error) {
	result := &WebNavigateToolInput{}

	if input == nil {
		return nil, fmt.Errorf("input is required")
	}

	inputBytes, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal input: %w", err)
	}

	if err := json.Unmarshal(inputBytes, result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal input: %w", err)
	}

	if result.WidgetId == "" {
		return nil, fmt.Errorf("widget_id is required")
	}

	if result.Url == "" {
		return nil, fmt.Errorf("url is required")
	}

	return result, nil
}

func GetWebNavigateToolDefinition(tabId string) uctypes.ToolDefinition {

	return uctypes.ToolDefinition{
		Name:        "web_navigate",
		DisplayName: "Navigate Web Widget",
		Description: "Navigate web widget to a URL.",
		ToolLogName: "web:navigate",
		Strict:      true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"widget_id": map[string]any{
					"type":        "string",
					"description": "8-character widget ID of the web browser widget",
				},
				"url": map[string]any{
					"type":        "string",
					"description": "URL to navigate to",
				},
			},
			"required":             []string{"widget_id", "url"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, toolUseData *uctypes.UIMessageDataToolUse) string {
			parsed, err := parseWebNavigateInput(input)
			if err != nil {
				return fmt.Sprintf("error parsing input: %v", err)
			}
			return fmt.Sprintf("navigating web widget %s to %q", parsed.WidgetId, parsed.Url)
		},
		ToolAnyCallback: func(input any, toolUseData *uctypes.UIMessageDataToolUse) (any, error) {
			parsed, err := parseWebNavigateInput(input)
			if err != nil {
				return nil, err
			}

			ctx, cancelFn := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelFn()

			fullBlockId, err := wcore.ResolveBlockIdFromPrefix(ctx, tabId, parsed.WidgetId)
			if err != nil {
				return nil, err
			}

			blockORef := waveobj.MakeORef(waveobj.OType_Block, fullBlockId)
			meta := map[string]any{
				"url": parsed.Url,
			}

			err = wstore.UpdateObjectMeta(ctx, blockORef, meta, false)
			if err != nil {
				return nil, fmt.Errorf("failed to update web block URL: %w", err)
			}

			wcore.SendWaveObjUpdate(blockORef)
			return true, nil
		},
	}
}

// webSelectorInput holds parsed input for web selector tools.
type webSelectorInput struct {
	WidgetId string
	Selector string
}

func parseWebSelectorInput(input any) (*webSelectorInput, error) {
	inputMap, ok := input.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid input format")
	}
	widgetId, ok := inputMap["widget_id"].(string)
	if !ok || widgetId == "" {
		return nil, fmt.Errorf("missing or invalid widget_id parameter")
	}
	selector, _ := inputMap["selector"].(string)
	if selector == "" {
		selector = "body"
	}
	return &webSelectorInput{WidgetId: widgetId, Selector: selector}, nil
}

// webReadContent resolves a web widget, reloads it, and fetches content via CSS selector.
func webReadContent(tabId string, input *webSelectorInput, opts *wshrpc.WebSelectorOpts) (string, error) {
	ctx, cancelFn := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelFn()

	fullBlockId, err := wcore.ResolveBlockIdFromPrefix(ctx, tabId, input.WidgetId)
	if err != nil {
		return "", fmt.Errorf("resolving block: %w", err)
	}

	rpcClient := wshclient.GetBareRpcClient()
	blockInfo, err := wshclient.BlockInfoCommand(rpcClient, fullBlockId, nil)
	if err != nil {
		return "", fmt.Errorf("getting block info: %w", err)
	}

	// Reload the page before reading to ensure fresh content
	reloadData := wshrpc.CommandWebSelectorData{
		WorkspaceId: blockInfo.WorkspaceId,
		BlockId:     fullBlockId,
		TabId:       blockInfo.TabId,
		Selector:    "body",
		Opts:        &wshrpc.WebSelectorOpts{Reload: true},
	}
	_, _ = wshclient.WebSelectorCommand(rpcClient, reloadData, &wshrpc.RpcOpts{
		Route:   wshutil.ElectronRoute,
		Timeout: 15000,
	})

	// Fetch content with the requested options
	data := wshrpc.CommandWebSelectorData{
		WorkspaceId: blockInfo.WorkspaceId,
		BlockId:     fullBlockId,
		TabId:       blockInfo.TabId,
		Selector:    input.Selector,
		Opts:        opts,
	}
	results, err := wshclient.WebSelectorCommand(rpcClient, data, &wshrpc.RpcOpts{
		Route:   wshutil.ElectronRoute,
		Timeout: 10000,
	})
	if err != nil {
		return "", fmt.Errorf("reading web content: %w", err)
	}
	if len(results) == 0 {
		return "", fmt.Errorf("no elements matched selector %q", input.Selector)
	}

	text := strings.Join(results, "\n")
	const maxLen = 5000
	if len(text) > maxLen {
		text = text[:maxLen] + fmt.Sprintf("\n... [truncated, %d chars total. Use a more specific CSS selector to get a smaller section.]", len(text))
	}
	return text, nil
}

func webToolCallDesc(toolAction string) func(any, any, *uctypes.UIMessageDataToolUse) string {
	return func(input any, output any, _ *uctypes.UIMessageDataToolUse) string {
		parsed, err := parseWebSelectorInput(input)
		if err != nil {
			return fmt.Sprintf("error: %v", err)
		}
		return fmt.Sprintf("%s from web widget %s (selector: %s)", toolAction, parsed.WidgetId, parsed.Selector)
	}
}

var webSelectorSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"widget_id": map[string]any{
			"type":        "string",
			"description": "8-character widget ID of the web browser widget",
		},
		"selector": map[string]any{
			"type":        "string",
			"description": "Standard CSS selector (document.querySelectorAll compatible). Use tag names, classes, IDs, attributes, and combinators only. Examples: 'h2', '.content', '#main', 'div.card > p', '[data-id=\"123\"]', 'section h2'. NEVER use jQuery pseudo-selectors like :contains(), :has(), :visible, :first, :last, :eq() — they are NOT valid CSS and will throw errors. To find elements by text content, select the parent element or use web_exec_js with JavaScript instead. Defaults to 'body'.",
		},
	},
	"required": []string{"widget_id"},
}

func GetWebReadTextToolDefinition(tabId string) uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:             "web_read_text",
		DisplayName:      "Read Web Page Text",
		Description:      "Get page text by CSS selector. Auto-refreshes. Returns clean text, no HTML.",
		ShortDescription: "Read text from web widget",
		ToolLogName:      "web:readtext",
		InputSchema:      webSelectorSchema,
		ToolCallDesc:     webToolCallDesc("reading text"),
		ToolTextCallback: func(input any) (string, error) {
			parsed, err := parseWebSelectorInput(input)
			if err != nil {
				return "", err
			}
			return webReadContent(tabId, parsed, &wshrpc.WebSelectorOpts{InnerText: true, All: true, Highlight: true})
		},
	}
}

func GetWebReadHTMLToolDefinition(tabId string) uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:             "web_read_html",
		DisplayName:      "Read Web Page HTML",
		Description:      "Get innerHTML by CSS selector. Auto-refreshes. For inspecting page structure and attributes.",
		ShortDescription: "Read HTML from web widget",
		ToolLogName:      "web:readhtml",
		InputSchema:      webSelectorSchema,
		ToolCallDesc:     webToolCallDesc("reading HTML"),
		ToolTextCallback: func(input any) (string, error) {
			parsed, err := parseWebSelectorInput(input)
			if err != nil {
				return "", err
			}
			return webReadContent(tabId, parsed, &wshrpc.WebSelectorOpts{Inner: true, All: true, Highlight: true})
		},
	}
}

const seoAuditJS = `
const data = {};

// Title
data.title = document.title || '';

// Meta tags
const metas = {};
document.querySelectorAll('meta[name], meta[property]').forEach(m => {
    const key = m.getAttribute('name') || m.getAttribute('property');
    if (key) metas[key] = m.getAttribute('content') || '';
});
data.meta = metas;

// Canonical
const canonical = document.querySelector('link[rel="canonical"]');
data.canonical = canonical ? canonical.getAttribute('href') : null;

// Hreflang
const hreflangs = [];
document.querySelectorAll('link[rel="alternate"][hreflang]').forEach(l => {
    hreflangs.push({ lang: l.getAttribute('hreflang'), href: l.getAttribute('href') });
});
if (hreflangs.length) data.hreflang = hreflangs;

// JSON-LD
const jsonLd = [];
document.querySelectorAll('script[type="application/ld+json"]').forEach(s => {
    try { jsonLd.push(JSON.parse(s.textContent)); } catch(e) { jsonLd.push({ error: e.message, raw: s.textContent.slice(0, 500) }); }
});
if (jsonLd.length) data.jsonLd = jsonLd;

// Open Graph
const og = {};
document.querySelectorAll('meta[property^="og:"]').forEach(m => {
    og[m.getAttribute('property')] = m.getAttribute('content') || '';
});
if (Object.keys(og).length) data.openGraph = og;

// Twitter Card
const tw = {};
document.querySelectorAll('meta[name^="twitter:"]').forEach(m => {
    tw[m.getAttribute('name')] = m.getAttribute('content') || '';
});
if (Object.keys(tw).length) data.twitterCard = tw;

// Headings structure
const headings = {};
['h1','h2','h3'].forEach(tag => {
    const els = document.querySelectorAll(tag);
    if (els.length) headings[tag] = Array.from(els).map(e => e.innerText.trim().slice(0, 100));
});
data.headings = headings;

// Images without alt
const imgsNoAlt = [];
document.querySelectorAll('img:not([alt]), img[alt=""]').forEach(img => {
    imgsNoAlt.push(img.src?.slice(0, 200) || img.getAttribute('data-src')?.slice(0, 200) || '[inline]');
});
if (imgsNoAlt.length) data.imagesWithoutAlt = imgsNoAlt;

// Links count
data.links = {
    internal: document.querySelectorAll('a[href^="/"], a[href^="' + location.origin + '"]').length,
    external: document.querySelectorAll('a[href^="http"]').length - document.querySelectorAll('a[href^="' + location.origin + '"]').length,
    nofollow: document.querySelectorAll('a[rel*="nofollow"]').length,
};

// URL
data.url = location.href;

return JSON.stringify(data, null, 2);
`

func GetWebExecJsToolDefinition(tabId string) uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:             "web_exec_js",
		DisplayName:      "Execute JavaScript",
		Description:      "Execute arbitrary JavaScript code in the web widget's page context, like a browser DevTools console. The code runs via a function body — use 'return' to send back a result. For example: 'return document.title' or 'return document.querySelectorAll(\"a\").length'. Does NOT reload the page, so it preserves current page state (form values, scroll position, etc.).",
		ShortDescription: "Execute JS in web widget",
		ToolLogName:      "web:execjs",
		Strict:           true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"widget_id": map[string]any{
					"type":        "string",
					"description": "8-character widget ID of the web browser widget",
				},
				"code": map[string]any{
					"type":        "string",
					"description": "JavaScript code to execute. Runs as a function body — use 'return' to return a value.",
				},
			},
			"required":             []string{"widget_id", "code"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, _ *uctypes.UIMessageDataToolUse) string {
			inputMap, _ := input.(map[string]any)
			widgetId, _ := inputMap["widget_id"].(string)
			code, _ := inputMap["code"].(string)
			if len(code) > 80 {
				code = code[:80] + "..."
			}
			return fmt.Sprintf("executing JS in web widget %s: %s", widgetId, code)
		},
		ToolTextCallback: func(input any) (string, error) {
			inputMap, ok := input.(map[string]any)
			if !ok {
				return "", fmt.Errorf("invalid input format")
			}
			widgetId, _ := inputMap["widget_id"].(string)
			if widgetId == "" {
				return "", fmt.Errorf("widget_id is required")
			}
			code, _ := inputMap["code"].(string)
			if code == "" {
				return "", fmt.Errorf("code is required")
			}
			return webExecJsOnWidget(tabId, widgetId, code)
		},
	}
}

func GetWebSEOAuditToolDefinition(tabId string) uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:             "web_seo_audit",
		DisplayName:      "SEO Audit",
		Description:      "Full SEO audit: title, meta, canonical, hreflang, JSON-LD, OG, Twitter Card, headings, alt text, links. Auto-refreshes.",
		ShortDescription: "SEO audit of web page",
		ToolLogName:      "web:seoaudit",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"widget_id": map[string]any{
					"type":        "string",
					"description": "8-character widget ID of the web browser widget",
				},
			},
			"required": []string{"widget_id"},
		},
		ToolCallDesc: func(input any, output any, _ *uctypes.UIMessageDataToolUse) string {
			inputMap, _ := input.(map[string]any)
			widgetId, _ := inputMap["widget_id"].(string)
			return fmt.Sprintf("running SEO audit on web widget %s", widgetId)
		},
		ToolTextCallback: func(input any) (string, error) {
			parsed, err := parseWebSelectorInput(input)
			if err != nil {
				return "", err
			}
			return webReadContent(tabId, parsed, &wshrpc.WebSelectorOpts{ExecJs: seoAuditJS})
		},
	}
}


// webExecJsOnWidget executes JS in a web widget WITHOUT reloading the page first.
// Use this for action tools (click, type, press key, exec_js) that should not reset page state.
func webExecJsOnWidget(tabId string, widgetId string, js string) (string, error) {
	ctx, cancelFn := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelFn()

	fullBlockId, err := wcore.ResolveBlockIdFromPrefix(ctx, tabId, widgetId)
	if err != nil {
		return "", fmt.Errorf("resolving block: %w", err)
	}

	rpcClient := wshclient.GetBareRpcClient()
	blockInfo, err := wshclient.BlockInfoCommand(rpcClient, fullBlockId, nil)
	if err != nil {
		return "", fmt.Errorf("getting block info: %w", err)
	}

	data := wshrpc.CommandWebSelectorData{
		WorkspaceId: blockInfo.WorkspaceId,
		BlockId:     fullBlockId,
		TabId:       blockInfo.TabId,
		Selector:    "body",
		Opts:        &wshrpc.WebSelectorOpts{ExecJs: js},
	}
	results, err := wshclient.WebSelectorCommand(rpcClient, data, &wshrpc.RpcOpts{
		Route:   wshutil.ElectronRoute,
		Timeout: 10000,
	})
	if err != nil {
		return "", fmt.Errorf("executing JS: %w", err)
	}
	if len(results) == 0 {
		return "", fmt.Errorf("no result from JS execution")
	}

	text := strings.Join(results, "\n")
	const maxLen = 50000
	if len(text) > maxLen {
		text = text[:maxLen] + "\n... [truncated]"
	}
	return text, nil
}

func GetWebClickToolDefinition(tabId string) uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:             "web_click",
		DisplayName:      "Click Web Element",
		Description:      "Click an element on the web page by CSS selector. For links (<a> tags), navigates to the href URL. For other elements, triggers a click event.",
		ShortDescription: "Click element in web widget",
		ToolLogName:      "web:click",
		Strict:           true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"widget_id": map[string]any{
					"type":        "string",
					"description": "8-character widget ID of the web browser widget",
				},
				"selector": map[string]any{
					"type":        "string",
					"description": "CSS selector of the element to click (e.g. 'button#submit', 'a.nav-link', '.btn-primary')",
				},
			},
			"required":             []string{"widget_id", "selector"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, _ *uctypes.UIMessageDataToolUse) string {
			inputMap, _ := input.(map[string]any)
			widgetId, _ := inputMap["widget_id"].(string)
			selector, _ := inputMap["selector"].(string)
			return fmt.Sprintf("clicking %q in web widget %s", selector, widgetId)
		},
		ToolTextCallback: func(input any) (string, error) {
			inputMap, ok := input.(map[string]any)
			if !ok {
				return "", fmt.Errorf("invalid input format")
			}
			widgetId, _ := inputMap["widget_id"].(string)
			if widgetId == "" {
				return "", fmt.Errorf("widget_id is required")
			}
			selector, _ := inputMap["selector"].(string)
			if selector == "" {
				return "", fmt.Errorf("selector is required")
			}
			js := fmt.Sprintf(`
				const el = document.querySelector(%q);
				if (!el) return 'error: no element found for selector %s';
				const desc = (el.tagName || '') + (el.id ? '#'+el.id : '') + (el.className ? '.'+el.className.split(' ').join('.') : '');
				// For links, navigate directly since el.click() may not trigger navigation in webview
				const anchor = el.closest('a[href]') || (el.tagName === 'A' && el.href ? el : null);
				if (anchor && anchor.href) {
					window.location.href = anchor.href;
					return 'navigating to ' + anchor.href + ' (clicked ' + desc + ')';
				}
				el.click();
				return 'clicked ' + desc;
			`, selector, selector)
			return webExecJsOnWidget(tabId, widgetId, js)
		},
	}
}

// webMouseClickOnWidget sends a native mouse click via Electron's sendInputEvent.
// Uses CSS selector to find the element position, then clicks at its center.
func webMouseClickOnWidget(tabId string, widgetId string, selector string) (string, error) {
	ctx, cancelFn := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelFn()

	fullBlockId, err := wcore.ResolveBlockIdFromPrefix(ctx, tabId, widgetId)
	if err != nil {
		return "", fmt.Errorf("resolving block: %w", err)
	}

	rpcClient := wshclient.GetBareRpcClient()
	blockInfo, err := wshclient.BlockInfoCommand(rpcClient, fullBlockId, nil)
	if err != nil {
		return "", fmt.Errorf("getting block info: %w", err)
	}

	data := wshrpc.CommandWebSelectorData{
		WorkspaceId: blockInfo.WorkspaceId,
		BlockId:     fullBlockId,
		TabId:       blockInfo.TabId,
		Selector:    selector,
		Opts:        &wshrpc.WebSelectorOpts{MouseClick: true},
	}
	results, err := wshclient.WebSelectorCommand(rpcClient, data, &wshrpc.RpcOpts{
		Route:   wshutil.ElectronRoute,
		Timeout: 10000,
	})
	if err != nil {
		return "", fmt.Errorf("mouse click: %w", err)
	}
	if len(results) == 0 {
		return "", fmt.Errorf("no result from mouse click")
	}
	return results[0], nil
}

// webMouseClickXYOnWidget sends a native mouse click at specific x,y coordinates.
// Use for clicking inside iframes or elements that can't be found by CSS selector.
func webMouseClickXYOnWidget(tabId string, widgetId string, x int, y int) (string, error) {
	ctx, cancelFn := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelFn()

	fullBlockId, err := wcore.ResolveBlockIdFromPrefix(ctx, tabId, widgetId)
	if err != nil {
		return "", fmt.Errorf("resolving block: %w", err)
	}

	rpcClient := wshclient.GetBareRpcClient()
	blockInfo, err := wshclient.BlockInfoCommand(rpcClient, fullBlockId, nil)
	if err != nil {
		return "", fmt.Errorf("getting block info: %w", err)
	}

	data := wshrpc.CommandWebSelectorData{
		WorkspaceId: blockInfo.WorkspaceId,
		BlockId:     fullBlockId,
		TabId:       blockInfo.TabId,
		Selector:    fmt.Sprintf("__xy:%d:%d", x, y),
		Opts:        &wshrpc.WebSelectorOpts{MouseClick: true},
	}
	results, err := wshclient.WebSelectorCommand(rpcClient, data, &wshrpc.RpcOpts{
		Route:   wshutil.ElectronRoute,
		Timeout: 10000,
	})
	if err != nil {
		return "", fmt.Errorf("mouse click at (%d,%d): %w", x, y, err)
	}
	if len(results) == 0 {
		return "", fmt.Errorf("no result from mouse click")
	}
	return results[0], nil
}

func GetWebMouseClickToolDefinition(tabId string) uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:             "web_mouse_click",
		DisplayName:      "Mouse Click Web Element",
		Description:      "Perform a native mouse click on a web page element. Unlike web_click which uses JavaScript click(), this dispatches a real mouse event that works with iframes (e.g. reCAPTCHA), embedded widgets, and elements that ignore synthetic clicks. You can click by CSS selector OR by x,y coordinates. Use coordinates when the target is inside an iframe.",
		ShortDescription: "Native mouse click in web widget",
		ToolLogName:      "web:mouseclick",
		Strict:           false,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"widget_id": map[string]any{
					"type":        "string",
					"description": "8-character widget ID of the web browser widget",
				},
				"selector": map[string]any{
					"type":        "string",
					"description": "CSS selector of the element to click (e.g. 'button#submit'). Omit if using x/y coordinates.",
				},
				"x": map[string]any{
					"type":        "integer",
					"description": "X coordinate (pixels from left edge of the page) to click. Use with y for iframe elements. You can find coordinates using web_exec_js with getBoundingClientRect().",
				},
				"y": map[string]any{
					"type":        "integer",
					"description": "Y coordinate (pixels from top edge of the page) to click. Use with x for iframe elements.",
				},
			},
			"required": []string{"widget_id"},
		},
		ToolCallDesc: func(input any, output any, _ *uctypes.UIMessageDataToolUse) string {
			inputMap, _ := input.(map[string]any)
			widgetId, _ := inputMap["widget_id"].(string)
			selector, _ := inputMap["selector"].(string)
			if selector != "" {
				return fmt.Sprintf("mouse clicking %q in web widget %s", selector, widgetId)
			}
			x, _ := inputMap["x"].(float64)
			y, _ := inputMap["y"].(float64)
			return fmt.Sprintf("mouse clicking at (%d,%d) in web widget %s", int(x), int(y), widgetId)
		},
		ToolTextCallback: func(input any) (string, error) {
			inputMap, ok := input.(map[string]any)
			if !ok {
				return "", fmt.Errorf("invalid input format")
			}
			widgetId, _ := inputMap["widget_id"].(string)
			if widgetId == "" {
				return "", fmt.Errorf("widget_id is required")
			}
			selector, _ := inputMap["selector"].(string)
			xVal, hasX := inputMap["x"].(float64)
			yVal, hasY := inputMap["y"].(float64)

			if selector != "" {
				return webMouseClickOnWidget(tabId, widgetId, selector)
			}
			if hasX && hasY {
				return webMouseClickXYOnWidget(tabId, widgetId, int(xVal), int(yVal))
			}
			return "", fmt.Errorf("either 'selector' or both 'x' and 'y' coordinates are required")
		},
	}
}

func GetWebOpenToolDefinition(tabId string, ownedWidgets ...*uctypes.OwnedWidgetSet) uctypes.ToolDefinition {
	var owned *uctypes.OwnedWidgetSet
	if len(ownedWidgets) > 0 {
		owned = ownedWidgets[0]
	}
	return uctypes.ToolDefinition{
		Name:             "web_open",
		DisplayName:      "Open Web Browser Widget",
		Description:      "Open a new web browser widget with the given URL. Only open ONE browser at a time — if you need to visit multiple pages, use web_navigate to switch URLs in the existing widget. Only call web_open again if you genuinely need two pages visible side by side.",
		ShortDescription: "Open new web browser widget",
		ToolLogName:      "web:open",
		Strict:           true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "URL to open in the new web browser widget",
				},
			},
			"required":             []string{"url"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, _ *uctypes.UIMessageDataToolUse) string {
			inputMap, _ := input.(map[string]any)
			url, _ := inputMap["url"].(string)
			return fmt.Sprintf("opening new web widget with URL %q", url)
		},
		ToolAnyCallback: func(input any, toolUseData *uctypes.UIMessageDataToolUse) (any, error) {
			inputMap, ok := input.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid input format")
			}
			url, _ := inputMap["url"].(string)
			if url == "" {
				return nil, fmt.Errorf("url is required")
			}

			ctx, cancelFn := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelFn()

			_ = ctx // context used implicitly by RPC
			rpcClient := wshclient.GetBareRpcClient()
			oref, err := wshclient.CreateBlockCommand(rpcClient, wshrpc.CommandCreateBlockData{
				TabId: tabId,
				BlockDef: &waveobj.BlockDef{
					Meta: map[string]any{
						waveobj.MetaKey_View: "web",
						waveobj.MetaKey_Url:  url,
					},
				},
				Focused: true,
			}, nil)
			if err != nil {
				return nil, fmt.Errorf("failed to create web widget: %w", err)
			}

			// Register widget as owned by this chat
			if owned != nil {
				owned.Add(oref.OID)
			}

			return map[string]any{
				"widget_id": oref.OID[:8],
				"url":       url,
			}, nil
		},
	}
}

func GetWebTypeInputToolDefinition(tabId string) uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:             "web_type_input",
		DisplayName:      "Type Text Into Web Input",
		Description:      "Type text into an input field, textarea, or contenteditable element on the web page. Focuses the element, clears its current value, and types the new text. Dispatches input and change events so frameworks detect the change.",
		ShortDescription: "Type text into web input",
		ToolLogName:      "web:typeinput",
		Strict:           true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"widget_id": map[string]any{
					"type":        "string",
					"description": "8-character widget ID of the web browser widget",
				},
				"selector": map[string]any{
					"type":        "string",
					"description": "CSS selector of the input element (e.g. 'input[name=\"email\"]', '#search-box', 'textarea.comment')",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "Text to type into the input field",
				},
				"clear": map[string]any{
					"type":        "boolean",
					"description": "Whether to clear the field before typing. Defaults to true.",
				},
			},
			"required":             []string{"widget_id", "selector", "text", "clear"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, _ *uctypes.UIMessageDataToolUse) string {
			inputMap, _ := input.(map[string]any)
			widgetId, _ := inputMap["widget_id"].(string)
			selector, _ := inputMap["selector"].(string)
			text, _ := inputMap["text"].(string)
			if len(text) > 40 {
				text = text[:40] + "..."
			}
			return fmt.Sprintf("typing %q into %q in web widget %s", text, selector, widgetId)
		},
		ToolTextCallback: func(input any) (string, error) {
			inputMap, ok := input.(map[string]any)
			if !ok {
				return "", fmt.Errorf("invalid input format")
			}
			widgetId, _ := inputMap["widget_id"].(string)
			if widgetId == "" {
				return "", fmt.Errorf("widget_id is required")
			}
			selector, _ := inputMap["selector"].(string)
			if selector == "" {
				return "", fmt.Errorf("selector is required")
			}
			text, _ := inputMap["text"].(string)
			clear := true
			if clearVal, ok := inputMap["clear"].(bool); ok {
				clear = clearVal
			}
			js := fmt.Sprintf(`
				const el = document.querySelector(%q);
				if (!el) return 'error: no element found for selector %s';
				el.focus();
				const isFormEl = (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT');
				if (%t) {
					if (isFormEl) {
						el.value = '';
					} else if (el.isContentEditable) {
						el.textContent = '';
					}
					el.dispatchEvent(new Event('input', { bubbles: true }));
					el.dispatchEvent(new Event('change', { bubbles: true }));
				}
				const text = %q;
				if (isFormEl) {
					// Set value via property and attribute for maximum compatibility
					el.value = (%t ? '' : (el.value || '')) + text;
					el.setAttribute('value', el.value);
					// Use native setter to trigger React/Vue/Angular internal state updates
					const proto = el.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype :
						el.tagName === 'SELECT' ? HTMLSelectElement.prototype :
						HTMLInputElement.prototype;
					const nativeSetter = Object.getOwnPropertyDescriptor(proto, 'value')?.set;
					if (nativeSetter) {
						nativeSetter.call(el, el.value);
					}
				} else if (el.isContentEditable) {
					el.textContent = (%t ? '' : (el.textContent || '')) + text;
				}
				// Dispatch full sequence of events for framework compatibility
				el.dispatchEvent(new Event('input', { bubbles: true }));
				el.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: text }));
				el.dispatchEvent(new Event('change', { bubbles: true }));
				const val = isFormEl ? el.value : el.textContent;
				return 'typed into ' + el.tagName + (el.id ? '#'+el.id : '') + ' (value: ' + (val || '').slice(0, 50) + ')';
			`, selector, selector, clear, text, clear, clear)
			return webExecJsOnWidget(tabId, widgetId, js)
		},
	}
}

func GetWebPressKeyToolDefinition(tabId string) uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:             "web_press_key",
		DisplayName:      "Press Key in Web Widget",
		Description:      "Simulate a key press on the web page. Dispatches keydown, keypress, and keyup events on the focused element (or a specific element if selector is provided). Common keys: 'Enter', 'Tab', 'Escape', 'ArrowDown', 'ArrowUp', 'Backspace', 'Delete', or any single character.",
		ShortDescription: "Press key in web widget",
		ToolLogName:      "web:presskey",
		Strict:           true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"widget_id": map[string]any{
					"type":        "string",
					"description": "8-character widget ID of the web browser widget",
				},
				"key": map[string]any{
					"type":        "string",
					"description": "Key to press (e.g. 'Enter', 'Tab', 'Escape', 'ArrowDown', 'a', '1')",
				},
				"selector": map[string]any{
					"type":        "string",
					"description": "Optional CSS selector of element to send the key to. Defaults to the currently focused element.",
				},
			},
			"required":             []string{"widget_id", "key", "selector"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, _ *uctypes.UIMessageDataToolUse) string {
			inputMap, _ := input.(map[string]any)
			widgetId, _ := inputMap["widget_id"].(string)
			key, _ := inputMap["key"].(string)
			selector, _ := inputMap["selector"].(string)
			if selector != "" {
				return fmt.Sprintf("pressing %q on %q in web widget %s", key, selector, widgetId)
			}
			return fmt.Sprintf("pressing %q in web widget %s", key, widgetId)
		},
		ToolTextCallback: func(input any) (string, error) {
			inputMap, ok := input.(map[string]any)
			if !ok {
				return "", fmt.Errorf("invalid input format")
			}
			widgetId, _ := inputMap["widget_id"].(string)
			if widgetId == "" {
				return "", fmt.Errorf("widget_id is required")
			}
			key, _ := inputMap["key"].(string)
			if key == "" {
				return "", fmt.Errorf("key is required")
			}
			selector, _ := inputMap["selector"].(string)
			var targetExpr string
			if selector != "" {
				targetExpr = fmt.Sprintf("document.querySelector(%q) || document.activeElement || document.body", selector)
			} else {
				targetExpr = "document.activeElement || document.body"
			}
			js := fmt.Sprintf(`
				const target = %s;
				const key = %q;
				const opts = { key: key, code: 'Key' + key.toUpperCase(), bubbles: true, cancelable: true };
				if (key === 'Enter') opts.code = 'Enter';
				else if (key === 'Tab') opts.code = 'Tab';
				else if (key === 'Escape') opts.code = 'Escape';
				else if (key === 'Backspace') opts.code = 'Backspace';
				else if (key === 'Delete') opts.code = 'Delete';
				else if (key.startsWith('Arrow')) opts.code = key;
				else if (key === ' ') opts.code = 'Space';
				target.dispatchEvent(new KeyboardEvent('keydown', opts));
				target.dispatchEvent(new KeyboardEvent('keypress', opts));
				target.dispatchEvent(new KeyboardEvent('keyup', opts));
				if (key === 'Enter' && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA')) {
					const form = target.closest('form');
					if (form && target.tagName === 'INPUT') {
						form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
					}
				}
				return 'pressed ' + key + ' on ' + target.tagName + (target.id ? '#'+target.id : '');
			`, targetExpr, key)
			return webExecJsOnWidget(tabId, widgetId, js)
		},
	}
}

const getConsoleLogsJS = `
(function() {
	if (!window.__woveConsoleLogs) return JSON.stringify([]);
	var logs = window.__woveConsoleLogs;
	var startIdx = arguments[0] || 0;
	var filtered = logs.slice(startIdx);
	return JSON.stringify({
		total: logs.length,
		returned: filtered.length,
		startIndex: startIdx,
		entries: filtered.slice(-200)
	}, null, 2);
})()
`

func GetWebGetConsoleToolDefinition(tabId string) uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:             "web_get_console",
		DisplayName:      "Get Browser Console",
		Description:      "Read browser console output (console.log, console.warn, console.error) from the web widget. Returns recent console entries with level, message, and timestamp. Useful for debugging errors, checking API responses, and understanding application state.",
		ShortDescription: "Read browser console logs",
		ToolLogName:      "web:getconsole",
		Strict:           true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"widget_id": map[string]any{
					"type":        "string",
					"description": "8-character widget ID of the web browser widget",
				},
				"level": map[string]any{
					"type":        "string",
					"description": "Filter by log level: 'error', 'warn', 'log', 'info', 'debug', or 'all' (default: 'all')",
				},
			},
			"required":             []string{"widget_id", "level"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, _ *uctypes.UIMessageDataToolUse) string {
			inputMap, _ := input.(map[string]any)
			widgetId, _ := inputMap["widget_id"].(string)
			level, _ := inputMap["level"].(string)
			if level == "" || level == "all" {
				return fmt.Sprintf("reading console logs from web widget %s", widgetId)
			}
			return fmt.Sprintf("reading console %s from web widget %s", level, widgetId)
		},
		ToolTextCallback: func(input any) (string, error) {
			inputMap, ok := input.(map[string]any)
			if !ok {
				return "", fmt.Errorf("invalid input format")
			}
			widgetId, _ := inputMap["widget_id"].(string)
			if widgetId == "" {
				return "", fmt.Errorf("widget_id is required")
			}
			level, _ := inputMap["level"].(string)
			if level == "" {
				level = "all"
			}

			js := fmt.Sprintf(`
				var allLogs = %s;
				var parsed = JSON.parse(allLogs);
				var level = %q;
				if (level !== 'all' && parsed.entries) {
					parsed.entries = parsed.entries.filter(function(e) { return e.level === level; });
					parsed.returned = parsed.entries.length;
				}
				return JSON.stringify(parsed, null, 2);
			`, getConsoleLogsJS, level)
			return webExecJsOnWidget(tabId, widgetId, js)
		},
	}
}

const inspectVueJS = `
(function() {
	var result = { url: window.location.href, title: document.title };

	// Inertia page
	try {
		var pageEl = document.querySelector('[data-page]');
		if (pageEl) {
			var data = JSON.parse(pageEl.dataset.page);
			result.inertia = { component: data.component || null, version: data.version || null, url: data.url || null };
			if (data.props) {
				var propKeys = Object.keys(data.props);
				result.inertia.propKeys = propKeys;
				result.inertia.propCount = propKeys.length;
			}
		}
	} catch(e) {}

	// Target element Vue chain
	var sel = (typeof selector !== 'undefined') ? selector : 'body';
	var el = document.querySelector(sel);
	if (!el) {
		result.error = 'no element found for selector: ' + selector;
		return JSON.stringify(result, null, 2);
	}

	var chain = [];
	var node = el;
	while (node) {
		var instance = node.__vueParentComponent;
		if (instance) {
			var type = instance.type;
			var file = type.__file || type.__name || instance.type.name || null;
			if (file && !chain.find(function(c) { return c.file === file; })) {
				chain.push({ name: type.__name || type.name || file.split('/').pop().replace('.vue',''), file: file });
			}
		}
		node = node.parentElement;
	}
	result.vueComponents = chain;

	// Vue app detection
	var appRoot = document.querySelector('#app');
	if (appRoot && appRoot.__vue_app__) {
		result.vueVersion = appRoot.__vue_app__.version || 'detected';
	} else if (window.__VUE__) {
		result.vueVersion = 'detected (devtools)';
	}

	return JSON.stringify(result, null, 2);
})()
`

func GetWebInspectVueToolDefinition(tabId string) uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:             "web_inspect_vue",
		DisplayName:      "Inspect Vue/Inertia Page",
		Description:      "Inspect the current web page for Vue.js component hierarchy and Inertia.js page data. Returns the Vue component chain for a given CSS selector (default: body), Inertia page component name, props keys, and Vue version. Useful for understanding frontend structure of Laravel/Inertia/Vue applications.",
		ShortDescription: "Inspect Vue/Inertia in web widget",
		ToolLogName:      "web:inspectvue",
		Strict:           true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"widget_id": map[string]any{
					"type":        "string",
					"description": "8-character widget ID of the web browser widget",
				},
				"selector": map[string]any{
					"type":        "string",
					"description": "CSS selector of the element to inspect for Vue components. Defaults to 'body'.",
				},
			},
			"required":             []string{"widget_id", "selector"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, _ *uctypes.UIMessageDataToolUse) string {
			inputMap, _ := input.(map[string]any)
			widgetId, _ := inputMap["widget_id"].(string)
			selector, _ := inputMap["selector"].(string)
			if selector == "" {
				selector = "body"
			}
			return fmt.Sprintf("inspecting Vue/Inertia at %q in web widget %s", selector, widgetId)
		},
		ToolTextCallback: func(input any) (string, error) {
			inputMap, ok := input.(map[string]any)
			if !ok {
				return "", fmt.Errorf("invalid input format")
			}
			widgetId, _ := inputMap["widget_id"].(string)
			if widgetId == "" {
				return "", fmt.Errorf("widget_id is required")
			}
			selector, _ := inputMap["selector"].(string)
			if selector == "" {
				selector = "body"
			}
			js := fmt.Sprintf(`
				var selector = %q;
				return %s
			`, selector, inspectVueJS)
			return webExecJsOnWidget(tabId, widgetId, js)
		},
	}
}
