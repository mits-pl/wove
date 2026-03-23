// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

import { WindowService } from "@/app/store/services";
import { RpcResponseHelper, WshClient } from "@/app/store/wshclient";
import { RpcApi } from "@/app/store/wshclientapi";
import { Notification, WebContents, net, safeStorage, shell } from "electron";
import { getResolvedUpdateChannel } from "emain/updater";
import { unamePlatform } from "./emain-platform";
import { getWebContentsByBlockId, webGetSelector } from "./emain-web";
import { createBrowserWindow, getWaveWindowById, getWaveWindowByWorkspaceId } from "./emain-window";

// --- CDP WebCapture helpers ---

const INTERACTIVE_TAGS = new Set(["A", "BUTTON", "INPUT", "SELECT", "TEXTAREA", "DETAILS", "SUMMARY"]);
const INTERACTIVE_ROLES = new Set(["button", "link", "menuitem", "tab", "checkbox", "radio", "switch", "textbox", "combobox", "searchbox", "option"]);
const MAX_ELEMENTS = 200;

type CdpSnapshotNode = {
    parentIndex: number;
    nodeType: number;
    nodeName: string;
    nodeValue: string;
    backendNodeId: number;
    attributes: string[];
    textValue?: { index: number };
    inputValue?: { index: number };
    currentSourceURL?: { index: number };
};

type CdpLayoutTreeNode = {
    nodeIndex: number;
    bounds: number[]; // [x, y, w, h] in page coordinates
    text?: string;
};

type CdpDocumentSnapshot = {
    documentURL: { index: number };
    nodes: {
        parentIndex: number[];
        nodeType: number[];
        nodeName: { index: number }[];
        nodeValue: { index: number }[];
        backendNodeId: number[];
        attributes: { index: number }[][];
        textValue?: { index: number; value: { index: number } }[];
        inputValue?: { index: number; value: { index: number } }[];
    };
    layout: {
        nodeIndex: number[];
        bounds: number[][];
        text: { index: number }[];
    };
};

type CdpSnapshot = {
    documents: CdpDocumentSnapshot[];
    strings: string[];
};

function getActionLabel(tag: string, role: string): string {
    if (tag === "A" || role === "link") return "Click";
    if (tag === "BUTTON" || role === "button" || role === "menuitem" || role === "tab") return "Click";
    if (tag === "INPUT" || tag === "TEXTAREA" || role === "textbox" || role === "searchbox" || role === "combobox") return "Type";
    if (tag === "SELECT") return "Select";
    if (tag === "IMG") return "View";
    if (role === "checkbox" || role === "radio" || role === "switch") return "Toggle";
    return "View";
}

function buildCssSelector(tag: string, attrs: Record<string, string>): string {
    const t = tag.toLowerCase();
    if (attrs.id) return `${t}#${attrs.id}`;
    let sel = t;
    if (attrs.name) sel += `[name="${attrs.name}"]`;
    else if (attrs.type && (tag === "INPUT" || tag === "BUTTON")) sel += `[type="${attrs.type}"]`;
    if (attrs.class) {
        const classes = attrs.class.trim().split(/\s+/).slice(0, 2);
        sel += classes.map((c) => `.${c}`).join("");
    }
    return sel;
}

function processCdpSnapshot(snapshot: CdpSnapshot, viewportHeight: number): WebCaptureElement[] {
    const elements: WebCaptureElement[] = [];
    const strings = snapshot.strings;

    for (let docIdx = 0; docIdx < snapshot.documents.length; docIdx++) {
        const doc = snapshot.documents[docIdx];
        const nodes = doc.nodes;
        const layout = doc.layout;

        // Build a map from nodeIndex -> layout index for quick lookup
        const layoutMap = new Map<number, number>();
        for (let li = 0; li < layout.nodeIndex.length; li++) {
            layoutMap.set(layout.nodeIndex[li], li);
        }

        for (let li = 0; li < layout.nodeIndex.length; li++) {
            const nodeIdx = layout.nodeIndex[li];
            const nodeType = nodes.nodeType[nodeIdx];
            if (nodeType !== 1) continue; // only Element nodes

            const tag = strings[nodes.nodeName[nodeIdx].index].toUpperCase();
            // Skip non-meaningful tags
            if (["HTML", "HEAD", "BODY", "SCRIPT", "STYLE", "META", "LINK", "BR", "HR", "NOSCRIPT"].includes(tag)) continue;

            const bounds = layout.bounds[li];
            const x = Math.round(bounds[0]);
            const y = Math.round(bounds[1]);
            const w = Math.round(bounds[2]);
            const h = Math.round(bounds[3]);

            // Skip zero-size or off-screen elements
            if (w <= 0 || h <= 0) continue;
            if (y + h < 0 || y > viewportHeight * 2) continue; // skip elements far below viewport

            // Parse attributes
            const attrIndices = nodes.attributes[nodeIdx] || [];
            const attrs: Record<string, string> = {};
            for (let ai = 0; ai < attrIndices.length; ai += 2) {
                const key = strings[attrIndices[ai].index];
                const val = strings[attrIndices[ai + 1].index];
                attrs[key] = val;
            }

            const role = attrs["role"] || "";
            const isInteractive = INTERACTIVE_TAGS.has(tag) || INTERACTIVE_ROLES.has(role) || attrs["contenteditable"] === "true";

            // Get text content from layout
            let text = "";
            if (layout.text[li] && layout.text[li].index >= 0) {
                text = strings[layout.text[li].index] || "";
            }
            // Fallback: check inputValue for inputs
            if (!text && nodes.inputValue) {
                for (const iv of nodes.inputValue) {
                    if (iv.index === nodeIdx && iv.value.index >= 0) {
                        text = strings[iv.value.index] || "";
                        break;
                    }
                }
            }
            text = text.trim().slice(0, 80);

            // Only include interactive elements, images, and text-bearing visible elements
            if (!isInteractive && tag !== "IMG" && !text && tag !== "DIV" && tag !== "SPAN") continue;
            // Skip non-interactive elements without text (except images)
            if (!isInteractive && !text && tag !== "IMG") continue;

            const selector = buildCssSelector(tag, attrs);
            const action = getActionLabel(tag, role);
            const desc = `[${action}] ${selector}${text ? ` "${text}"` : ""} at (${x},${y})`;

            // Collect key attrs for output
            const outAttrs: Record<string, string> = {};
            if (attrs.href) outAttrs.href = attrs.href.slice(0, 100);
            if (attrs.type) outAttrs.type = attrs.type;
            if (attrs.placeholder) outAttrs.placeholder = attrs.placeholder.slice(0, 50);
            if (attrs["aria-label"]) outAttrs["aria-label"] = attrs["aria-label"].slice(0, 50);

            elements.push({
                idx: 0, // assigned later
                tag: tag.toLowerCase(),
                text: text || undefined,
                bbox: [x, y, w, h],
                int: isInteractive || undefined,
                sel: selector,
                frame: docIdx > 0 ? docIdx : undefined,
                desc,
            });

            if (elements.length >= MAX_ELEMENTS * 2) break; // collect extra, trim later
        }
        if (elements.length >= MAX_ELEMENTS * 2) break;
    }

    // Prioritize interactive elements, then trim to MAX_ELEMENTS
    elements.sort((a, b) => {
        if (a.int && !b.int) return -1;
        if (!a.int && b.int) return 1;
        return (a.bbox[1] - b.bbox[1]) || (a.bbox[0] - b.bbox[0]); // top-to-bottom, left-to-right
    });
    const trimmed = elements.slice(0, MAX_ELEMENTS);

    // Re-sort by position and assign sequential indices
    trimmed.sort((a, b) => (a.bbox[1] - b.bbox[1]) || (a.bbox[0] - b.bbox[0]));
    for (let i = 0; i < trimmed.length; i++) {
        trimmed[i].idx = i;
        // Update desc with final index
        const el = trimmed[i];
        const action = el.desc.match(/^\[(\w+)\]/)?.[1] || "View";
        el.desc = `[${action}] ${el.sel}${el.text ? ` "${el.text}"` : ""} at (${el.bbox[0]},${el.bbox[1]})`;
    }

    return trimmed;
}

async function injectSomMarkers(wc: WebContents, elements: WebCaptureElement[]): Promise<void> {
    if (elements.length === 0) return;
    // Build all markers in a single JS call for performance
    const markersJs = `
    (() => {
        const frag = document.createDocumentFragment();
        const markers = ${JSON.stringify(elements.map((el) => ({ idx: el.idx, x: el.bbox[0], y: el.bbox[1] })))};
        markers.forEach(m => {
            const d = document.createElement('div');
            d.className = '__wave_som';
            d.textContent = String(m.idx);
            d.style.cssText = 'position:absolute;left:' + m.x + 'px;top:' + m.y + 'px;background:rgba(220,38,38,0.85);color:#fff;font:bold 9px/12px system-ui,sans-serif;padding:0 3px;border-radius:2px;z-index:2147483647;pointer-events:none;';
            frag.appendChild(d);
        });
        document.body.appendChild(frag);
    })()`;
    await wc.executeJavaScript(markersJs);
}

async function cleanupSomMarkers(wc: WebContents): Promise<void> {
    await wc.executeJavaScript(`document.querySelectorAll('.__wave_som').forEach(e => e.remove())`);
}

export class ElectronWshClientType extends WshClient {
    constructor() {
        super("electron");
    }

    async handle_webselector(rh: RpcResponseHelper, data: CommandWebSelectorData): Promise<string[]> {
        if (!data.tabid || !data.blockid || !data.workspaceid) {
            throw new Error("tabid and blockid are required");
        }
        const ww = getWaveWindowByWorkspaceId(data.workspaceid);
        if (ww == null) {
            throw new Error(`no window found with workspace ${data.workspaceid}`);
        }
        const wc = await getWebContentsByBlockId(ww, data.tabid, data.blockid);
        if (wc == null) {
            throw new Error(`no webcontents found with blockid ${data.blockid}`);
        }
        // Native mouse click mode: uses Electron sendInputEvent for real mouse events
        // Works with iframes, reCAPTCHA, and elements that ignore JS click()
        if (data.opts?.mouseclick) {
            let x: number, y: number, desc: string;

            // Check for coordinate-based click (__xy:X:Y format)
            const xyMatch = data.selector.match(/^__xy:(\d+):(\d+)$/);
            if (xyMatch) {
                x = parseInt(xyMatch[1]);
                y = parseInt(xyMatch[2]);
                desc = `coordinates`;
            } else {
                // Selector-based: find element position via JS
                const escapedSel = data.selector.replace(/\\/g, "\\\\").replace(/'/g, "\\'");
                const posJs = `
                (() => {
                    const el = document.querySelector('${escapedSel}');
                    if (!el) return { error: 'no element found for selector: ${escapedSel}' };
                    const rect = el.getBoundingClientRect();
                    const desc = (el.tagName || '') + (el.id ? '#'+el.id : '') + (el.className ? '.'+el.className.split(' ').join('.') : '');
                    return { x: Math.round(rect.left + rect.width/2), y: Math.round(rect.top + rect.height/2), desc };
                })()`;
                const pos = await wc.executeJavaScript(posJs);
                if (pos.error) {
                    throw new Error(pos.error);
                }
                x = pos.x;
                y = pos.y;
                desc = pos.desc;
            }

            wc.sendInputEvent({ type: "mouseDown", x, y, button: "left", clickCount: 1 });
            wc.sendInputEvent({ type: "mouseUp", x, y, button: "left", clickCount: 1 });
            return [`mouse clicked ${desc} at (${x}, ${y})`];
        }

        // Sanitize opts: only allow known safe options from RPC
        const safeOpts: any = {};
        if (data.opts) {
            if (data.opts.all) safeOpts.all = true;
            if (data.opts.inner) safeOpts.inner = true;
            if (data.opts.innertext) safeOpts.innertext = true;
            if (data.opts.reload) safeOpts.reload = true;
            if (data.opts.highlight) safeOpts.highlight = true;
            // execjs: only allow from server-side (Go backend) tool calls.
            // The RPC route is trusted (server -> electron), so we allow it here
            // but validate that the value is a non-empty string to prevent injection of non-string types.
            if (typeof data.opts.execjs === "string" && data.opts.execjs.length > 0) {
                safeOpts.execjs = data.opts.execjs;
            }
        }
        const rtn = await webGetSelector(wc, data.selector, safeOpts);
        return rtn;
    }

    async handle_webcapture(rh: RpcResponseHelper, data: CommandWebCaptureData): Promise<WebCaptureRtnData> {
        if (!data.tabid || !data.blockid || !data.workspaceid) {
            throw new Error("tabid, blockid, and workspaceid are required");
        }
        const ww = getWaveWindowByWorkspaceId(data.workspaceid);
        if (ww == null) {
            throw new Error(`no window found with workspace ${data.workspaceid}`);
        }
        const wc = await getWebContentsByBlockId(ww, data.tabid, data.blockid);
        if (wc == null) {
            throw new Error(`no webcontents found with blockid ${data.blockid}`);
        }

        // Get viewport info
        const viewport: WebCaptureViewport = await wc.executeJavaScript(`({
            scrolly: Math.round(window.scrollY),
            pageheight: Math.round(document.documentElement.scrollHeight),
            width: Math.round(window.innerWidth),
            height: Math.round(window.innerHeight)
        })`);

        // Attach CDP debugger
        try {
            wc.debugger.attach("1.3");
        } catch (e) {
            throw new Error(
                "Cannot capture: another debugger is attached (DevTools open?). Close DevTools and retry."
            );
        }

        try {
            // CDP DOM snapshot - all frames, global coordinates
            const snapshot: CdpSnapshot = await wc.debugger.sendCommand("DOMSnapshot.captureSnapshot", {
                computedStyles: ["display", "visibility"],
                includeDOMRects: true,
            });

            // Process snapshot into element list
            const elements = processCdpSnapshot(snapshot, viewport.height);

            // Inject SoM markers before screenshot
            await injectSomMarkers(wc, elements);

            // CDP screenshot - JPEG scaled to 768px width
            const scale = Math.min(768 / viewport.width, 1);
            const screenshotResult = await wc.debugger.sendCommand("Page.captureScreenshot", {
                format: "jpeg",
                quality: 50,
                clip: {
                    x: 0,
                    y: 0,
                    width: viewport.width,
                    height: viewport.height,
                    scale,
                },
            });

            // Cleanup SoM markers
            await cleanupSomMarkers(wc);

            return {
                screenshot: `data:image/jpeg;base64,${screenshotResult.data}`,
                elements,
                viewport,
            };
        } finally {
            try {
                wc.debugger.detach();
            } catch {
                // ignore detach errors
            }
        }
    }

    async handle_notify(rh: RpcResponseHelper, notificationOptions: WaveNotificationOptions) {
        new Notification({
            title: notificationOptions.title,
            body: notificationOptions.body,
            silent: notificationOptions.silent,
        }).show();
    }

    async handle_getupdatechannel(rh: RpcResponseHelper): Promise<string> {
        return getResolvedUpdateChannel();
    }

    async handle_focuswindow(rh: RpcResponseHelper, windowId: string) {
        console.log(`focuswindow ${windowId}`);
        const fullConfig = await RpcApi.GetFullConfigCommand(ElectronWshClient);
        let ww = getWaveWindowById(windowId);
        if (ww == null) {
            const window = await WindowService.GetWindow(windowId);
            if (window == null) {
                throw new Error(`window ${windowId} not found`);
            }
            ww = await createBrowserWindow(window, fullConfig, {
                unamePlatform,
                isPrimaryStartupWindow: false,
            });
        }
        ww.focus();
    }

    async handle_electronencrypt(
        rh: RpcResponseHelper,
        data: CommandElectronEncryptData
    ): Promise<CommandElectronEncryptRtnData> {
        if (!safeStorage.isEncryptionAvailable()) {
            throw new Error("encryption is not available");
        }
        const encrypted = safeStorage.encryptString(data.plaintext);
        const ciphertext = encrypted.toString("base64");

        let storagebackend = "";
        if (process.platform === "linux") {
            storagebackend = safeStorage.getSelectedStorageBackend();
        }

        return {
            ciphertext,
            storagebackend,
        };
    }

    async handle_electrondecrypt(
        rh: RpcResponseHelper,
        data: CommandElectronDecryptData
    ): Promise<CommandElectronDecryptRtnData> {
        if (!safeStorage.isEncryptionAvailable()) {
            throw new Error("encryption is not available");
        }
        const encrypted = Buffer.from(data.ciphertext, "base64");
        const plaintext = safeStorage.decryptString(encrypted);

        let storagebackend = "";
        if (process.platform === "linux") {
            storagebackend = safeStorage.getSelectedStorageBackend();
        }

        return {
            plaintext,
            storagebackend,
        };
    }

    async handle_networkonline(rh: RpcResponseHelper): Promise<boolean> {
        return net.isOnline();
    }

    async handle_electronsystembell(rh: RpcResponseHelper): Promise<void> {
        shell.beep();
    }

    // async handle_workspaceupdate(rh: RpcResponseHelper) {
    //     console.log("workspaceupdate");
    //     fireAndForget(async () => {
    //         console.log("workspace menu clicked");
    //         const updatedWorkspaceMenu = await getWorkspaceMenu();
    //         const workspaceMenu = Menu.getApplicationMenu().getMenuItemById("workspace-menu");
    //         workspaceMenu.submenu = Menu.buildFromTemplate(updatedWorkspaceMenu);
    //     });
    // }
}

export let ElectronWshClient: ElectronWshClientType;

export function initElectronWshClient() {
    ElectronWshClient = new ElectronWshClientType();
}
