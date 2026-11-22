import { describe, expect, test } from "bun:test";
import { arrangeCanvasDocument, CANVAS_ARRANGE_OPTIONS, findCanvasInsertionPosition, placeCanvasIncomingNodes } from "@/lib/canvas/canvas-smart-layout";
import { CanvasNodeType, type CanvasConnection, type CanvasNodeData } from "@/types/canvas";

function node(id: string, patch: Partial<CanvasNodeData> = {}): CanvasNodeData {
    return { id, type: CanvasNodeType.Image, title: id, position: { x: 100, y: 100 }, width: 320, height: 180, metadata: { content: `resource:${id}` }, ...patch };
}
function expectSeparated(nodes: CanvasNodeData[], gap = 48) {
    nodes.forEach((a, i) =>
        nodes.slice(i + 1).forEach((b) => {
            const collision = a.position.x < b.position.x + b.width + gap && b.position.x < a.position.x + a.width + gap && a.position.y < b.position.y + b.height + gap && b.position.y < a.position.y + a.height + gap;
            if (collision) throw new Error(`${a.id} overlaps ${b.id}: ${JSON.stringify([a.position, b.position])}`);
        }),
    );
}

describe("smart canvas arrangement", () => {
    for (const { mode } of CANVAS_ARRANGE_OPTIONS) {
        test(`${mode}: resolves a complete stack with mixed sizes, leaves content and dimensions intact`, () => {
            const types = [CanvasNodeType.Text, CanvasNodeType.Image, CanvasNodeType.Video, CanvasNodeType.Audio];
            const nodes = Array.from({ length: 40 }, (_, i) => node(`n${i.toString().padStart(2, "0")}`, { type: types[i % 4]!, width: 200 + i * 8, height: 100 + (i % 7) * 60 }));
            const original = structuredClone(nodes);
            const connections = nodes.slice(1).map((item, i) => ({ id: `e${i}`, fromNodeId: nodes[i]!.id, toNodeId: item.id }));
            const result = arrangeCanvasDocument(nodes, connections, { mode });
            expect(result.unitCount).toBe(nodes.length);
            expectSeparated(result.nodes);
            expect(nodes).toEqual(original);
            result.nodes.forEach((item, i) => {
                expect({ ...item, position: nodes[i]!.position }).toEqual(nodes[i]);
                expect(item.metadata).toBe(nodes[i]!.metadata);
            });
            expect(arrangeCanvasDocument(result.nodes, connections, { mode }).movedCount).toBe(0);
        });
    }

    test("a selected node avoids unselected and locked obstacles, including when it is the only movable node", () => {
        const nodes = [node("selected"), node("outside"), node("locked", { position: { x: 500, y: 100 }, metadata: { locked: true } })];
        const result = arrangeCanvasDocument(nodes, [], { mode: "auto", selectedIds: new Set(["selected", "locked"]) });
        expect(result.nodes[1]).toBe(nodes[1]);
        expect(result.nodes[2]).toBe(nodes[2]);
        expect(result.lockedCount).toBe(1);
        expectSeparated(result.nodes);
    });

    test("frame selection moves all descendants by the same offset, including hidden children", () => {
        const frame = node("frame", { type: CanvasNodeType.Frame, width: 500, height: 400, metadata: { frame: { collapsed: true, expandedWidth: 800, expandedHeight: 600 } } });
        const child = node("child", { parentId: frame.id, position: { x: 130, y: 200 } });
        const hidden = node("hidden", { parentId: frame.id, position: { x: 4000, y: 5000 } });
        const outside = node("outside", { width: 800, height: 600 });
        const result = arrangeCanvasDocument([frame, child, hidden, outside], [], { mode: "auto", selectedIds: new Set([child.id]) });
        const [f, c, h] = result.nodes;
        expect(c!.position.x - f!.position.x).toBe(child.position.x - frame.position.x);
        expect(h!.position.y - f!.position.y).toBe(hidden.position.y - frame.position.y);
        expect(result.focusNodes.map((item) => item.id)).toEqual([frame.id]);
        expect(result.nodes[3]).toBe(outside);
        expectSeparated([f!, outside]);
    });

    test("smart arrangement separates overlapping storyboard contents and expands the frame before placing it", () => {
        const frame = node("shot-frame", { type: CanvasNodeType.Frame, position: { x: 100, y: 100 }, width: 520, height: 360, metadata: { frame: { collapsed: false, expandedWidth: 520, expandedHeight: 360 } } });
        const prompt = node("prompt", { type: CanvasNodeType.Text, parentId: frame.id, position: { x: 140, y: 180 }, width: 320, height: 180 });
        const image = node("reference", { parentId: frame.id, position: { x: 160, y: 190 }, width: 240, height: 180 });
        const video = node("video", { type: CanvasNodeType.Video, parentId: frame.id, position: { x: 150, y: 200 }, width: 400, height: 220 });
        const neighboringFrame = node("next-frame", { type: CanvasNodeType.Frame, position: { x: 700, y: 100 }, width: 500, height: 360 });
        const nodes = [frame, prompt, image, video, neighboringFrame];
        const edges = [{ id: "reference-video", fromNodeId: image.id, toNodeId: video.id }];
        const result = arrangeCanvasDocument(nodes, edges, { mode: "auto", selectedIds: new Set([frame.id]) });
        const [nextFrame, nextPrompt, nextImage, nextVideo, fixed] = result.nodes;

        expect(result.movedCount).toBeGreaterThan(0);
        expect(fixed).toBe(neighboringFrame);
        expectSeparated([nextPrompt!, nextImage!, nextVideo!], 24);
        expectSeparated([nextFrame!, fixed!]);
        for (const child of [nextPrompt!, nextImage!, nextVideo!]) {
            expect(child.position.x).toBeGreaterThanOrEqual(nextFrame!.position.x + 24);
            expect(child.position.y).toBeGreaterThanOrEqual(nextFrame!.position.y + 36 + 24);
            expect(child.position.x + child.width + 24).toBeLessThanOrEqual(nextFrame!.position.x + nextFrame!.width);
            expect(child.position.y + child.height + 24).toBeLessThanOrEqual(nextFrame!.position.y + nextFrame!.height);
        }
        expect(nextFrame!.metadata?.frame?.expandedWidth).toBe(nextFrame!.width);
        expect(nextFrame!.metadata?.frame?.expandedHeight).toBe(nextFrame!.height);
        expect(nextFrame!.width).toBeGreaterThan(frame.width);
        expect(arrangeCanvasDocument(result.nodes, edges, { mode: "auto", selectedIds: new Set([frame.id]) }).movedCount).toBe(0);
        expect(nodes[1]!.position).toEqual({ x: 140, y: 180 });
    });

    test("smart arrangement preserves an already spaced storyboard interior", () => {
        const frame = node("frame", { type: CanvasNodeType.Frame, position: { x: 100, y: 100 }, width: 900, height: 440, metadata: { frame: { collapsed: false, expandedWidth: 900, expandedHeight: 440 } } });
        const prompt = node("prompt", { parentId: frame.id, position: { x: 140, y: 180 } });
        const image = node("image", { parentId: frame.id, position: { x: 520, y: 180 } });
        const blocker = node("blocker", { position: { x: 100, y: 100 }, width: 900, height: 440 });
        const result = arrangeCanvasDocument([frame, prompt, image, blocker], [], { mode: "auto", selectedIds: new Set([frame.id]) });
        const [nextFrame, nextPrompt, nextImage, fixed] = result.nodes;
        expect(fixed).toBe(blocker);
        expect(nextPrompt!.position.x - nextFrame!.position.x).toBe(40);
        expect(nextImage!.position.x - nextFrame!.position.x).toBe(420);
        expect(nextFrame!.width).toBe(900);
        expect(arrangeCanvasDocument(result.nodes, [], { mode: "auto", selectedIds: new Set([frame.id]) }).movedCount).toBe(0);
    });

    test("a locked descendant protects its entire group", () => {
        const frame = node("frame", { type: CanvasNodeType.Frame });
        const child = node("child", { parentId: frame.id, metadata: { locked: true } });
        const other = node("other");
        const result = arrangeCanvasDocument([frame, child, other], [], { mode: "separate" });
        expect(result.nodes[0]).toBe(frame);
        expect(result.nodes[1]).toBe(child);
        expect(result.lockedCount).toBe(1);
        expectSeparated([frame, result.nodes[2]!]);
    });

    test("expanded batches and image layers retain their internal offsets", () => {
        const root = node("batch", { metadata: { isBatchRoot: true, imageBatchExpanded: true, batchChildIds: ["batch-child"] } });
        const child = node("batch-child", { position: { x: 500, y: 150 }, metadata: { batchRootId: root.id } });
        const layer = node("layer", { position: { x: 100, y: 100 } });
        const composite = node("composite", { metadata: { imageLayerGroup: { id: "layers", layers: [{ nodeId: layer.id, name: "layer", visible: true, opacity: 1 }], width: 320, height: 180 } } as CanvasNodeData["metadata"] });
        const result = arrangeCanvasDocument([root, child, composite, layer], [], { mode: "auto" });
        expect(result.unitCount).toBe(2);
        const byId = new Map(result.nodes.map((item) => [item.id, item]));
        expect(byId.get(child.id)!.position.x - byId.get(root.id)!.position.x).toBe(400);
        expect(byId.get(layer.id)!.position).toEqual(byId.get(composite.id)!.position);
    });

    test("shot order uses chapter numbers naturally and keeps each shot's media in one row", () => {
        const nodes = [
            node("last", { metadata: { chapterId: "c10", chapterTitle: "第10章", shotIndex: 1 } }),
            node("video", { type: CanvasNodeType.Video, metadata: { chapterId: "c2", chapterTitle: "第2章", shotIndex: 2 } }),
            node("image", { metadata: { chapterId: "c2", chapterTitle: "第2章", shotIndex: 2 } }),
            node("first", { metadata: { chapterId: "c2", chapterTitle: "第2章", shotIndex: 1 } }),
        ];
        const result = arrangeCanvasDocument(nodes, [], { mode: "shots" });
        const [last, video, image, first] = result.nodes;
        expect(first!.position.y).toBeLessThan(image!.position.y);
        expect(image!.position.y).toBe(video!.position.y);
        expect(image!.position.x).toBeLessThan(video!.position.x);
        expect(video!.position.y).toBeLessThan(last!.position.y);
        expectSeparated(result.nodes);
    });

    test("cyclic and external edges do not lose nodes or mutate the graph", () => {
        const nodes = [node("a"), node("b"), node("c")];
        const edges: CanvasConnection[] = [
            { id: "a", fromNodeId: "a", toNodeId: "b" },
            { id: "b", fromNodeId: "b", toNodeId: "a" },
            { id: "outside", fromNodeId: "outside", toNodeId: "c" },
        ];
        const before = structuredClone(edges);
        expectSeparated(arrangeCanvasDocument(nodes, edges, { mode: "auto" }).nodes);
        expect(edges).toEqual(before);
    });

    test("shot proximity never reverses a real video-to-image dependency", () => {
        const metadata = { shotIndex: 1, chapterId: "chapter" };
        const nodes = [node("image", { metadata }), node("video", { type: CanvasNodeType.Video, metadata })];
        const result = arrangeCanvasDocument(nodes, [{ id: "extract", fromNodeId: "video", toNodeId: "image" }], { mode: "auto" });
        expect(result.nodes[1]!.position.x).toBeLessThan(result.nodes[0]!.position.x);
    });

    test("storyboard bindings separate equal shot numbers across scripts and generated outputs inherit their shot", () => {
        const script = (id: string, imageNodeId: string) => node(id, { type: CanvasNodeType.Script, title: "同名分镜", metadata: { storyboard: { rows: [{ id: `${id}-row`, shotNumber: 1, imageNodeId }] } } as CanvasNodeData["metadata"] });
        const nodes = [script("script-a", "image-a"), script("script-b", "image-b"), node("image-a"), node("image-b"), node("result", { type: CanvasNodeType.Video })];
        const edges = [{ id: "result", fromNodeId: "image-a", toNodeId: "result" }];
        const before = structuredClone(nodes);
        const result = arrangeCanvasDocument(nodes, edges, { mode: "shots" });
        const byId = new Map(result.nodes.map((item) => [item.id, item]));
        expect(byId.get("result")!.position.y).toBe(byId.get("image-a")!.position.y);
        expect(byId.get("image-b")!.position.y).toBeGreaterThan(byId.get("image-a")!.position.y);
        expect(nodes).toEqual(before);
        expect(result.nodes.find((item) => item.id === "result")!.metadata).toEqual(before.find((item) => item.id === "result")!.metadata);
        expect(arrangeCanvasDocument(result.nodes, edges, { mode: "shots" }).movedCount).toBe(0);
    });

    test("empty / locked-only scopes are safe and separate does not disturb a valid layout", () => {
        expect(arrangeCanvasDocument([], [], { mode: "auto" }).movedCount).toBe(0);
        expect(arrangeCanvasDocument([node("locked", { metadata: { locked: true } })], [], { mode: "auto" }).unitCount).toBe(0);
        const nodes = [node("a"), node("b", { position: { x: 700, y: 300 } })];
        const result = arrangeCanvasDocument(nodes, [], { mode: "separate" });
        expect(result.movedCount).toBe(0);
        expect(result.nodes).toEqual(nodes);
    });

    test("a 500-node canvas is arranged without losing nodes or introducing overlaps", () => {
        const nodes = Array.from({ length: 500 }, (_, i) => node(`n${String(i).padStart(4, "0")}`, { width: 200 + (i % 11) * 20, height: 100 + (i % 9) * 30 }));
        const result = arrangeCanvasDocument(nodes, [], { mode: "auto" });
        expect(result.nodes).toHaveLength(500);
        expectSeparated(result.nodes);
        expect(arrangeCanvasDocument(result.nodes, [], { mode: "auto" }).movedCount).toBe(0);
    });

    test("all layout modes remain stable with obstacles and disconnected components", () => {
        const nodes = Array.from({ length: 30 }, (_, i) => node(`n${i}`, { position: { x: (i * 71) % 600, y: (i * 83) % 500 }, width: 200 + (i % 4) * 90, height: 160 + (i % 5) * 100, metadata: { locked: i % 8 === 0 } }));
        const edges = nodes
            .slice(1)
            .filter((_, i) => i % 3 === 0)
            .map((item, i) => ({ id: `e${i}`, fromNodeId: nodes[i]!.id, toNodeId: item.id }));
        CANVAS_ARRANGE_OPTIONS.forEach(({ mode }) => {
            const result = arrangeCanvasDocument(nodes, edges, { mode });
            expect(arrangeCanvasDocument(result.nodes, edges, { mode }).movedCount, mode).toBe(0);
            result.nodes.filter((item) => !item.metadata?.locked).forEach((item) => result.nodes.filter((other) => other.metadata?.locked).forEach((fixed) => expectSeparated([item, fixed])));
        });
    });
});

describe("canvas insertion placement", () => {
    test("repeated insertions reserve free space without moving existing nodes", () => {
        let nodes = [node("existing")];
        const existing = nodes[0];
        for (let i = 0; i < 30; i++) nodes = [...nodes, ...placeCanvasIncomingNodes(nodes, [node(`new-${i}`, { width: 240 + (i % 3) * 100 })])];
        expect(nodes[0]).toBe(existing);
        expectSeparated(nodes);
    });
    test("pasted groups retain their internal positions", () => {
        const frame = node("frame", { type: CanvasNodeType.Frame, width: 600, height: 500 });
        const child = node("child", { parentId: frame.id, position: { x: 150, y: 180 } });
        const placed = placeCanvasIncomingNodes([node("existing")], [frame, child]);
        expect(placed[1]!.position.x - placed[0]!.position.x).toBe(50);
        expect(placed[1]!.position.y - placed[0]!.position.y).toBe(80);
        expectSeparated([node("existing"), placed[0]!]);
    });
    test("hidden children do not inflate placement obstacles and a free target is retained", () => {
        const root = node("root", { metadata: { imageBatchExpanded: false } });
        const hidden = node("child", { position: { x: 5000, y: 5000 }, metadata: { batchRootId: root.id } });
        expect(findCanvasInsertionPosition([root, hidden], { x: 5000, y: 5000 }, { width: 300, height: 200 })).toEqual({ x: 5000, y: 5000 });
    });
});
