import { CanvasNodeType, type CanvasConnection, type CanvasNodeData, type Position } from "@/types/canvas";
import { canvasLayoutLane, layoutCanvasAuto, layoutCanvasFlow, layoutCanvasNodes, layoutCanvasNodesByMediaType } from "./canvas-layout";

export type CanvasArrangeMode = "auto" | "separate" | "byType" | "shots" | "flow" | "row" | "column" | "grid";
export const CANVAS_ARRANGE_OPTIONS: Array<{ mode: CanvasArrangeMode; label: string; description: string }> = [
    { mode: "auto", label: "智能整理", description: "按创作关联和连线布局，独立素材按类型归整" },
    { mode: "separate", label: "仅消除重叠", description: "尽量保留位置，只推开过近的节点" },
    { mode: "byType", label: "按类型整理", description: "按文本、图片、视频、音频分区" },
    { mode: "shots", label: "按镜头顺序", description: "按章节、镜头编号排列，同镜头素材放在一起" },
    { mode: "flow", label: "按连线整理", description: "从左到右展示来源、处理与结果" },
    { mode: "grid", label: "宫格排列", description: "适合浏览和比较一组素材" },
    { mode: "row", label: "横向排列", description: "从左到右依次展开" },
    { mode: "column", label: "纵向排列", description: "从上到下依次展开" },
];

const GAP = 56;
type Rect = Position & { width: number; height: number };
type Unit = { node: CanvasNodeData; members: CanvasNodeData[]; visible: CanvasNodeData[]; locked: boolean };

function overlaps(a: Rect, b: Rect, gap = GAP) {
    return a.x < b.x + b.width + gap && b.x < a.x + a.width + gap && a.y < b.y + b.height + gap && b.y < a.y + a.height + gap;
}

function bounds(nodes: CanvasNodeData[]): Rect {
    const x = Math.min(...nodes.map((node) => node.position.x));
    const y = Math.min(...nodes.map((node) => node.position.y));
    return { x, y, width: Math.max(...nodes.map((node) => node.position.x + node.width)) - x, height: Math.max(...nodes.map((node) => node.position.y + node.height)) - y };
}

function stableOrder(a: CanvasNodeData, b: CanvasNodeData) {
    return (a.createdAt || "").localeCompare(b.createdAt || "") || a.id.localeCompare(b.id);
}

/** Containers, batches and decomposed images are indivisible movement units, including their hidden members. */
function canvasUnits(nodes: CanvasNodeData[]): Unit[] {
    const byId = new Map(nodes.map((node) => [node.id, node]));
    const parents = new Map(nodes.map((node) => [node.id, node.id]));
    const find = (id: string): string => {
        let root = id;
        while (parents.get(root) !== root) root = parents.get(root)!;
        while (parents.get(id) !== root) {
            const next = parents.get(id)!;
            parents.set(id, root);
            id = next;
        }
        return root;
    };
    const join = (owner: string | undefined, child: string) => {
        if (owner && byId.has(owner) && byId.has(child)) parents.set(find(child), find(owner));
    };
    nodes.forEach((node) => {
        join(node.parentId, node.id);
        join(node.metadata?.batchRootId, node.id);
        node.metadata?.batchChildIds?.forEach((id) => join(node.id, id));
        node.metadata?.imageLayerGroup?.layers.forEach((layer) => join(node.id, layer.nodeId));
        node.metadata?.experimentalLayerPlan?.requests.forEach((request) => join(node.id, request.nodeId));
    });
    const visible = (node: CanvasNodeData) => {
        const batch = node.metadata?.batchRootId ? byId.get(node.metadata.batchRootId) : undefined;
        if (batch && !batch.metadata?.imageBatchExpanded) return false;
        const seen = new Set([node.id]);
        let parent = node.parentId ? byId.get(node.parentId) : undefined;
        while (parent && !seen.has(parent.id)) {
            if (parent.type === CanvasNodeType.Frame && parent.metadata?.frame?.collapsed) return false;
            seen.add(parent.id);
            parent = parent.parentId ? byId.get(parent.parentId) : undefined;
        }
        return true;
    };
    const groups = new Map<string, CanvasNodeData[]>();
    nodes.forEach((node) => {
        const root = find(node.id);
        const members = groups.get(root) || [];
        members.push(node);
        groups.set(root, members);
    });
    return [...groups]
        .map(([id, members]) => {
            const root = byId.get(id)!;
            const shown = members.filter(visible);
            const rect = bounds(shown.length ? shown : [root]);
            return {
                node: { ...root, type: root.type === CanvasNodeType.Frame ? CanvasNodeType.Text : root.type, position: { x: rect.x, y: rect.y }, width: rect.width, height: rect.height },
                members,
                visible: shown,
                locked: members.some((node) => node.metadata?.locked),
            };
        })
        .sort((a, b) => stableOrder(a.node, b.node));
}

/** Each directional scan advances beyond an obstacle; at most n collisions are possible. */
function freePosition(obstacles: Rect[], preferred: Position, size: { width: number; height: number }, gap = GAP, forwardOnly = false): Position {
    if (!obstacles.some((item) => overlaps({ ...preferred, ...size }, item, gap))) return preferred;
    const alternatives = (forwardOnly ? (["down", "right"] as const) : (["right", "down", "left", "up"] as const)).map((direction) => {
        const candidate = { ...preferred };
        for (let attempt = 0; attempt <= obstacles.length; attempt += 1) {
            const hits = obstacles.filter((item) => overlaps({ ...candidate, ...size }, item, gap));
            if (!hits.length) break;
            if (direction === "right") candidate.x = Math.max(...hits.map((item) => item.x + item.width + gap));
            if (direction === "left") candidate.x = Math.min(...hits.map((item) => item.x - size.width - gap));
            if (direction === "down") candidate.y = Math.max(...hits.map((item) => item.y + item.height + gap));
            if (direction === "up") candidate.y = Math.min(...hits.map((item) => item.y - size.height - gap));
        }
        return candidate;
    });
    return alternatives.sort((a, b) => Math.hypot(a.x - preferred.x, a.y - preferred.y) - Math.hypot(b.x - preferred.x, b.y - preferred.y))[0]!;
}

export function findCanvasInsertionPosition(nodes: CanvasNodeData[], preferred: Position, size: { width: number; height: number }, options: { gap?: number; forwardOnly?: boolean } = {}) {
    return freePosition(
        canvasUnits(nodes).map((unit) => ({ ...unit.node.position, width: unit.node.width, height: unit.node.height })),
        preferred,
        size,
        options.gap,
        options.forwardOnly,
    );
}

/** Only the incoming units move. Call inside the state updater so concurrent uploads reserve different space. */
export function placeCanvasIncomingNodes(current: CanvasNodeData[], incoming: CanvasNodeData[]) {
    if (!incoming.length) return incoming;
    const existingIds = new Set(current.map((node) => node.id));
    const obstacles = canvasUnits(current).map((unit) => ({ ...unit.node.position, width: unit.node.width, height: unit.node.height }));
    const positions = new Map<string, Position>();
    canvasUnits(incoming).forEach((unit) => {
        if (unit.members.some((node) => (node.parentId && existingIds.has(node.parentId)) || (node.metadata?.batchRootId && existingIds.has(node.metadata.batchRootId)))) return;
        const position = freePosition(obstacles, unit.node.position, unit.node);
        unit.members.forEach((node) => positions.set(node.id, { x: node.position.x + position.x - unit.node.position.x, y: node.position.y + position.y - unit.node.position.y }));
        obstacles.push({ ...position, width: unit.node.width, height: unit.node.height });
    });
    return incoming.map((node) => (positions.has(node.id) ? { ...node, position: positions.get(node.id)! } : node));
}

function shotOrder(nodes: CanvasNodeData[]) {
    const lane = ["text", "image", "video", "audio"];
    return [...nodes].sort(
        (a, b) =>
            (a.metadata?.chapterTitle || a.metadata?.chapterId || "\uffff").localeCompare(b.metadata?.chapterTitle || b.metadata?.chapterId || "\uffff", undefined, { numeric: true }) ||
            (a.metadata?.shotIndex ?? Number.MAX_SAFE_INTEGER) - (b.metadata?.shotIndex ?? Number.MAX_SAFE_INTEGER) ||
            (a.metadata?.sceneId || "").localeCompare(b.metadata?.sceneId || "", undefined, { numeric: true }) ||
            lane.indexOf(canvasLayoutLane(a)) - lane.indexOf(canvasLayoutLane(b)) ||
            stableOrder(a, b),
    );
}

export function arrangeCanvasDocument(nodes: CanvasNodeData[], connections: CanvasConnection[], options: { mode: CanvasArrangeMode; selectedIds?: Set<string> }) {
    const units = canvasUnits(nodes);
    const selected = units.filter((unit) => !options.selectedIds?.size || unit.members.some((node) => options.selectedIds!.has(node.id)));
    const moving = selected.filter((unit) => !unit.locked);
    const movingIds = new Set(moving.map((unit) => unit.node.id));
    const fixed = units.filter((unit) => !movingIds.has(unit.node.id)).map((unit) => unit.node);
    const candidates = moving.map((unit) => unit.node);
    const owner = new Map(units.flatMap((unit) => unit.members.map((node) => [node.id, unit.node.id] as const)));
    const edges = connections.flatMap((edge) => {
        const fromNodeId = owner.get(edge.fromNodeId);
        const toNodeId = owner.get(edge.toNodeId);
        return fromNodeId && toNodeId && fromNodeId !== toNodeId ? [{ ...edge, fromNodeId, toNodeId }] : [];
    });
    // Explicit chapter / shot identity is stronger than a loose media type. Virtual edges keep each shot together.
    if (options.mode === "auto") {
        const shots = new Map<string, CanvasNodeData[]>();
        candidates.forEach((node) => {
            if (node.metadata?.shotIndex == null) return;
            const key = JSON.stringify([node.metadata.chapterId || node.metadata.chapterTitle || "", node.metadata.shotIndex, node.metadata.sceneId || ""]);
            shots.set(key, [...(shots.get(key) || []), node]);
        });
        shots.forEach((members) => {
            const sorted = shotOrder(members);
            sorted.slice(1).forEach((node, index) => edges.push({ id: `layout-${node.id}`, fromNodeId: sorted[index]!.id, toNodeId: node.id }));
        });
    }
    let planned = new Map<string, Position>();
    if (candidates.length) {
        if (options.mode === "auto") planned = layoutCanvasAuto(candidates, edges, fixed);
        else if (options.mode === "flow") planned = layoutCanvasFlow(candidates, edges, fixed);
        else if (options.mode === "byType") planned = layoutCanvasNodesByMediaType(candidates, fixed);
        else if (options.mode === "shots") {
            const ordered = shotOrder(candidates);
            const left = Math.min(...ordered.map((node) => node.position.x));
            let y = Math.min(...ordered.map((node) => node.position.y));
            let x = left;
            let rowHeight = 0;
            let previousKey = "";
            ordered.forEach((node, index) => {
                const key = JSON.stringify([node.metadata?.chapterId || node.metadata?.chapterTitle || "", node.metadata?.shotIndex ?? node.id]);
                if (index && key !== previousKey) {
                    y += rowHeight + GAP;
                    x = left;
                    rowHeight = 0;
                }
                planned.set(node.id, { x, y });
                x += node.width + GAP;
                rowHeight = Math.max(rowHeight, node.height);
                previousKey = key;
            });
        } else if (options.mode !== "separate") planned = layoutCanvasNodes(candidates, options.mode);
    }
    const occupied = fixed.map((node) => ({ ...node.position, width: node.width, height: node.height }));
    if (planned.size && options.mode !== "auto" && options.mode !== "flow" && options.mode !== "byType") {
        const first = candidates[0]!;
        const delta = { x: first.position.x - planned.get(first.id)!.x, y: first.position.y - planned.get(first.id)!.y };
        const alreadyPlaced = candidates.every((node) => {
            const position = planned.get(node.id)!;
            return Math.abs(node.position.x - position.x - delta.x) < 0.001 && Math.abs(node.position.y - position.y - delta.y) < 0.001 && !occupied.some((rect) => overlaps({ ...node.position, width: node.width, height: node.height }, rect));
        });
        if (alreadyPlaced) planned = new Map(candidates.map((node) => [node.id, node.position]));
        else {
            const planNodes = candidates.map((node) => ({ ...node, position: planned.get(node.id)! }));
            const rect = bounds(planNodes);
            const position = freePosition(occupied, rect, rect);
            planned.forEach((value, id) => planned.set(id, { x: value.x + position.x - rect.x, y: value.y + position.y - rect.y }));
        }
    }
    const finalPositions = new Map<string, Position>();
    // Keep the plan's visual order while pushing collisions out of the way; already valid layouts are unchanged.
    const ordered = [...moving].sort((a, b) => {
        const ap = planned.get(a.node.id) || a.node.position;
        const bp = planned.get(b.node.id) || b.node.position;
        return ap.y - bp.y || ap.x - bp.x || stableOrder(a.node, b.node);
    });
    ordered.forEach((unit) => {
        const preferred = planned.get(unit.node.id) || unit.node.position;
        const position = freePosition(occupied, preferred, unit.node);
        occupied.push({ ...position, width: unit.node.width, height: unit.node.height });
        unit.members.forEach((node) => {
            const x = node.position.x + position.x - unit.node.position.x;
            const y = node.position.y + position.y - unit.node.position.y;
            if (Math.abs(x - node.position.x) > 0.001 || Math.abs(y - node.position.y) > 0.001) finalPositions.set(node.id, { x, y });
        });
    });
    const nextNodes = nodes.map((node) => (finalPositions.has(node.id) ? { ...node, position: finalPositions.get(node.id)! } : node));
    const visibleIds = new Set(moving.flatMap((unit) => unit.visible.map((node) => node.id)));
    return { nodes: nextNodes, movedCount: finalPositions.size, unitCount: moving.length, lockedCount: selected.filter((unit) => unit.locked).length, focusNodes: nextNodes.filter((node) => visibleIds.has(node.id)) };
}
