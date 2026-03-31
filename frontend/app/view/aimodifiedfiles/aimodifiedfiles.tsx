// Copyright 2026, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

import type { BlockNodeModel } from "@/app/block/blocktypes";
import type { TabModel } from "@/app/store/tab-model";
import { waveEventSubscribeSingle } from "@/app/store/wps";
import { TabRpcClient } from "@/app/store/wshrpcutil";
import type { WaveEnv, WaveEnvSubset } from "@/app/waveenv/waveenv";
import { globalStore } from "@/store/jotaiStore";
import * as jotai from "jotai";
import { useEffect } from "react";
import "./aimodifiedfiles.scss";

type AiModifiedFilesEnv = WaveEnvSubset<{
    rpc: {
        WaveAIGetModifiedFilesCommand: WaveEnv["rpc"]["WaveAIGetModifiedFilesCommand"];
    };
    wos: WaveEnv["wos"];
}>;

export class AiModifiedFilesViewModel implements ViewModel {
    blockId: string;
    nodeModel: BlockNodeModel;
    tabModel: TabModel;
    env: AiModifiedFilesEnv;
    viewType = "aimodifiedfiles";
    blockAtom: jotai.Atom<Block>;
    filesAtom: jotai.PrimitiveAtom<WaveAIModifiedFileEntry[]>;
    errorAtom: jotai.PrimitiveAtom<string | null>;
    viewIcon: jotai.Atom<string>;
    viewName: jotai.Atom<string>;
    viewText: jotai.Atom<string>;

    constructor({ blockId, nodeModel, tabModel, waveEnv }: ViewModelInitType) {
        this.blockId = blockId;
        this.nodeModel = nodeModel;
        this.tabModel = tabModel;
        this.env = waveEnv as AiModifiedFilesEnv;
        this.blockAtom = this.env.wos.getWaveObjectAtom<Block>(`block:${blockId}`);
        this.filesAtom = jotai.atom([]) as jotai.PrimitiveAtom<WaveAIModifiedFileEntry[]>;
        this.errorAtom = jotai.atom(null) as jotai.PrimitiveAtom<string | null>;
        this.viewIcon = jotai.atom("file-pen");
        this.viewName = jotai.atom("Modified Files");
        this.viewText = jotai.atom((get) => {
            const files = get(this.filesAtom);
            const count = new Set(files.map((f) => f.filepath)).size;
            return count > 0 ? `${count} file${count !== 1 ? "s" : ""}` : "";
        });
    }

    get viewComponent(): ViewComponent {
        return AiModifiedFilesView;
    }
}

const ACTION_ICONS: Record<string, string> = {
    write: "fa-solid fa-plus",
    edit: "fa-solid fa-pen",
    delete: "fa-solid fa-trash",
};

const ACTION_LABELS: Record<string, string> = {
    write: "Created/Written",
    edit: "Edited",
    delete: "Deleted",
};

function formatTime(ts: number): string {
    const d = new Date(ts);
    return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

function shortenPath(filepath: string): { dir: string; name: string } {
    const parts = filepath.split("/");
    const name = parts.pop() || filepath;
    const dir = parts.length > 2 ? ".../" + parts.slice(-2).join("/") : parts.join("/");
    return { dir, name };
}

type GroupedFile = {
    filepath: string;
    actions: { action: string; timestamp: number }[];
    lastTimestamp: number;
};

function groupFiles(files: WaveAIModifiedFileEntry[]): GroupedFile[] {
    const map = new Map<string, GroupedFile>();
    for (const f of files) {
        let group = map.get(f.filepath);
        if (!group) {
            group = { filepath: f.filepath, actions: [], lastTimestamp: 0 };
            map.set(f.filepath, group);
        }
        group.actions.push({ action: f.action, timestamp: f.timestamp });
        if (f.timestamp > group.lastTimestamp) {
            group.lastTimestamp = f.timestamp;
        }
    }
    const result = Array.from(map.values());
    result.sort((a, b) => b.lastTimestamp - a.lastTimestamp);
    return result;
}

function AiModifiedFilesView({ blockId, model }: ViewComponentProps<AiModifiedFilesViewModel>) {
    const blockData = jotai.useAtomValue(model.blockAtom);
    const files = jotai.useAtomValue(model.filesAtom);
    const error = jotai.useAtomValue(model.errorAtom);

    const chatId = blockData?.meta?.["aimodifiedfiles:chatid"] as string;

    useEffect(() => {
        if (!chatId) {
            globalStore.set(model.errorAtom, "No chat session linked");
            return;
        }

        // Initial fetch
        model.env.rpc
            .WaveAIGetModifiedFilesCommand(TabRpcClient, { chatid: chatId })
            .then((result) => {
                if (result?.files) {
                    globalStore.set(model.filesAtom, result.files);
                }
            })
            .catch((e: any) => {
                globalStore.set(model.errorAtom, `Failed to load: ${e.message}`);
            });

        // Subscribe to real-time updates — append new entries as they arrive
        const unsub = waveEventSubscribeSingle({
            eventType: "waveai:modifiedfile",
            scope: chatId,
            handler: (event) => {
                const entry = event.data as WaveAIModifiedFileEntry;
                if (entry) {
                    const current = globalStore.get(model.filesAtom);
                    globalStore.set(model.filesAtom, [...current, entry]);
                }
            },
        });

        return unsub;
    }, [chatId]);

    if (error) {
        return (
            <div className="aimodifiedfiles-container">
                <div className="aimodifiedfiles-error">{error}</div>
            </div>
        );
    }

    if (files.length === 0) {
        return (
            <div className="aimodifiedfiles-container">
                <div className="aimodifiedfiles-empty">No files modified yet</div>
            </div>
        );
    }

    const grouped = groupFiles(files);

    return (
        <div className="aimodifiedfiles-container">
            <div className="aimodifiedfiles-list">
                {grouped.map((group) => {
                    const { dir, name } = shortenPath(group.filepath);
                    const lastAction = group.actions[group.actions.length - 1];
                    return (
                        <div key={group.filepath} className="aimodifiedfiles-item" title={group.filepath}>
                            <div className="aimodifiedfiles-item-icon">
                                <i className={ACTION_ICONS[lastAction.action] || "fa-solid fa-file"} />
                            </div>
                            <div className="aimodifiedfiles-item-info">
                                <div className="aimodifiedfiles-item-name">{name}</div>
                                <div className="aimodifiedfiles-item-dir">{dir}</div>
                            </div>
                            <div className="aimodifiedfiles-item-meta">
                                <span className={`aimodifiedfiles-action aimodifiedfiles-action-${lastAction.action}`}>
                                    {ACTION_LABELS[lastAction.action] || lastAction.action}
                                </span>
                                {group.actions.length > 1 && (
                                    <span className="aimodifiedfiles-count">{group.actions.length}x</span>
                                )}
                                <span className="aimodifiedfiles-time">{formatTime(lastAction.timestamp)}</span>
                            </div>
                        </div>
                    );
                })}
            </div>
        </div>
    );
}

export default AiModifiedFilesView;
