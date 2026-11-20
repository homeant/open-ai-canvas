import { mock } from "bun:test";
import assert from "node:assert/strict";
import { Window } from "happy-dom";

self.onmessage = async () => {
    try {
        const dom = new Window({ url: "http://localhost/" });
        // Exercise menu events without Happy DOM's incomplete native animation cancellation promises.
        Object.defineProperty(dom.HTMLElement.prototype, "animate", { configurable: true, value: undefined });
        for (const [key, value] of Object.entries({
            window: dom,
            document: dom.document,
            navigator: dom.navigator,
            HTMLElement: dom.HTMLElement,
            Node: dom.Node,
            Element: dom.Element,
            SVGElement: dom.SVGElement,
            ShadowRoot: dom.ShadowRoot,
            DocumentFragment: dom.DocumentFragment,
            ResizeObserver: dom.ResizeObserver,
            MutationObserver: dom.MutationObserver,
            requestAnimationFrame: dom.requestAnimationFrame.bind(dom),
            cancelAnimationFrame: dom.cancelAnimationFrame.bind(dom),
            localStorage: dom.localStorage,
            sessionStorage: dom.sessionStorage,
            getComputedStyle: dom.getComputedStyle.bind(dom),
            IS_REACT_ACT_ENVIRONMENT: true,
        }))
            Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });
        const { act, useState } = await import("react");
        const { createRoot } = await import("react-dom/client");
        const antd = await import("antd");
        const notifications: Array<{ content?: unknown }> = [];
        const message = { info: () => {}, warning: () => {}, error: () => {}, success: (value: { content?: unknown }) => notifications.push(value), destroy: () => {} };
        mock.module("antd", () => ({ ...antd, App: { ...antd.App, useApp: () => ({ message }) } }));
        const { useCanvasHistory } = await import("../../src/pages/canvas/use-canvas-history");
        const { useCanvasNodeOperations } = await import("../../src/pages/canvas/use-canvas-node-operations");
        const { CanvasArrangeControl } = await import("../../src/components/canvas/canvas-arrange-control");
        const { CanvasNodeContextMenu } = await import("../../src/components/canvas/canvas-context-menu");
        const { CanvasNodeType } = await import("../../src/types/canvas");
        const { useCanvasThemeStore } = await import("../../src/stores/canvas/use-canvas-theme-store");
        type Node = import("../../src/types/canvas").CanvasNodeData;
        type Connection = import("../../src/types/canvas").CanvasConnection;
        type Mode = import("../../src/lib/canvas/canvas-smart-layout").CanvasArrangeMode;
        const initial: Node[] = ["a", "b", "c"].map((id) => ({ id, title: id, type: CanvasNodeType.Image, position: { x: 100, y: 100 }, width: 300, height: 200 }));
        const nodesRef = { current: initial };
        const connectionsRef = { current: [] as Connection[] };
        const selectedNodeIdsRef = { current: new Set<string>() };
        const modes: Mode[] = [];
        const focused: Node[][] = [];
        let controls: { history: ReturnType<typeof useCanvasHistory>; operations: ReturnType<typeof useCanvasNodeOperations>; setNodes: React.Dispatch<React.SetStateAction<Node[]>> };
        function Harness() {
            const [nodes, setNodes] = useState(initial);
            const [connections, setConnections] = useState<Connection[]>([]);
            const [selected, setSelectedNodeIds] = useState(new Set<string>());
            const [selectedConnectionId, setSelectedConnectionId] = useState<string | null>(null);
            const [contextMenu, setContextMenu] = useState<import("../../src/types/canvas").ContextMenuState | null>(null);
            const [dialogNodeId, setDialogNodeId] = useState<string | null>(null);
            const [chats, setChatSessions] = useState<import("../../src/types/canvas").CanvasAssistantSession[]>([]);
            const [activeChatId, setActiveChatId] = useState<string | null>(null);
            const [appearance, applyCanvasAppearance] = useState({} as import("../../src/lib/canvas/canvas-appearance").CanvasAppearance);
            const [backgroundMode, setBackgroundMode] = useState<import("../../src/lib/canvas-theme").CanvasBackgroundMode>("dots");
            const [showImageInfo, setShowImageInfo] = useState(false);
            nodesRef.current = nodes;
            connectionsRef.current = connections;
            selectedNodeIdsRef.current = selected;
            const history = useCanvasHistory({
                projectLoaded: true,
                nodes,
                connections,
                chatSessions: chats,
                activeChatId,
                canvasAppearance: appearance,
                backgroundMode,
                showImageInfo,
                setNodes,
                setConnections,
                setChatSessions,
                setActiveChatId,
                applyCanvasAppearance,
                setBackgroundMode,
                setShowImageInfo,
                setSelectedNodeIds,
                setSelectedConnectionId,
                setContextMenu,
            });
            const operations = useCanvasNodeOperations({
                projectId: "layout-fixture",
                defaultDrawingEngine: "excalidraw",
                nodesRef,
                connectionsRef,
                selectedNodeIdsRef,
                getCanvasCenter: () => ({ x: 100, y: 100 }),
                setNodes,
                setConnections,
                setSelectedNodeIds,
                setSelectedConnectionId,
                setContextMenu,
                setDialogNodeId,
                commitNodeArrangement: history.commitNodeArrangement,
                onArrangementFocus: (items) => focused.push(items),
                onUndoArrangement: history.undoCanvas,
                onNodesDeleted: () => {},
            });
            controls = { history, operations, setNodes };
            return <CanvasArrangeControl selectedCount={selected.size} onArrange={(mode) => modes.push(mode)} />;
        }
        const host = document.createElement("div");
        document.body.append(host);
        const errors: unknown[] = [];
        const root = createRoot(host, { onUncaughtError: (error) => errors.push(error) });
        await act(async () => root.render(<Harness />));
        await act(async () =>
            controls.history.resetHistory({ nodes: initial, connections: [], chatSessions: [], activeChatId: null, canvasAppearance: {} as import("../../src/lib/canvas/canvas-appearance").CanvasAppearance, backgroundMode: "dots", showImageInfo: false }),
        );
        await act(async () => controls.setNodes((nodes) => nodes.map((node) => (node.id === "a" ? { ...node, title: "edited" } : node))));
        await act(async () => controls.operations.autoArrangeCanvasNodes("auto"));
        assert.equal(focused.length, 1);
        assert.equal(notifications.length, 1);
        const arranged = structuredClone(nodesRef.current);
        assert.notDeepEqual(
            arranged.map((node) => node.position),
            initial.map((node) => node.position),
        );
        // No delay: undo must see the explicit layout transaction before the 180ms history debounce.
        await act(async () => controls.history.undoCanvas());
        assert.deepEqual(
            nodesRef.current.map((node) => node.position),
            initial.map((node) => node.position),
        );
        assert.equal(nodesRef.current[0]!.title, "edited");
        await act(async () => controls.history.redoCanvas());
        assert.deepEqual(nodesRef.current, arranged);
        const beforeCreate = nodesRef.current.length;
        await act(async () => controls.operations.createNode(CanvasNodeType.Image));
        await act(async () => controls.operations.createNode(CanvasNodeType.Image));
        await act(async () => controls.operations.duplicateNode("a", "copy"));
        assert.equal(nodesRef.current.length, beforeCreate + 3);
        for (let i = 0; i < nodesRef.current.length; i++)
            for (let j = i + 1; j < nodesRef.current.length; j++) {
                const a = nodesRef.current[i]!,
                    b = nodesRef.current[j]!;
                assert.ok(a.position.x + a.width <= b.position.x || b.position.x + b.width <= a.position.x || a.position.y + a.height <= b.position.y || b.position.y + b.height <= a.position.y);
            }
        const saved = JSON.parse(JSON.stringify(nodesRef.current)) as Node[];
        assert.deepEqual(
            saved.map((node) => ({ id: node.id, position: node.position, width: node.width, height: node.height, title: node.title })),
            nodesRef.current.map((node) => ({ id: node.id, position: node.position, width: node.width, height: node.height, title: node.title })),
        );
        const primary = host.querySelector("button")!;
        await act(async () => primary.click());
        assert.equal(modes.at(-1), "auto");
        const more = host.querySelector('button[aria-label="选择整理方式"]')!;
        await act(async () => more.dispatchEvent(new dom.MouseEvent("click", { bubbles: true })));
        const menu = document.body.querySelector('[role="menu"]');
        assert.ok(menu, "arrangement menu opens");
        assert.equal(menu.querySelectorAll('[role="menuitem"]:not([aria-disabled="true"])').length, 8);
        const shots = [...menu.querySelectorAll('[role="menuitem"]')].find((item) => item.textContent?.startsWith("按镜头顺序"))!;
        await act(async () => shots.dispatchEvent(new dom.MouseEvent("click", { bubbles: true })));
        assert.equal(modes.at(-1), "shots");
        await act(async () => more.dispatchEvent(new dom.MouseEvent("click", { bubbles: true })));
        assert.equal(more.getAttribute("aria-expanded"), "true");
        await act(async () => document.body.querySelector('[role="menu"]')!.dispatchEvent(new dom.KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
        assert.equal(more.getAttribute("aria-expanded"), "false");
        assert.equal(document.activeElement, more);
        await act(async () => useCanvasThemeStore.setState({ theme: "dark" }));
        assert.ok(host.textContent?.includes("智能整理"));
        let closed = 0;
        function ContextView() {
            const [open, setOpen] = useState(true);
            const props = { menu: { type: "node", nodeId: "a", x: 100, y: 100 }, node: initial[0], selectedCount: 2, canUndo: false, canRedo: false, canPaste: false } as React.ComponentProps<typeof CanvasNodeContextMenu>;
            return open ? (
                <CanvasNodeContextMenu
                    {...props}
                    onArrange={(mode) => modes.push(mode)}
                    onClose={() => {
                        closed++;
                        setOpen(false);
                    }}
                />
            ) : null;
        }
        await act(async () => root.render(<ContextView />));
        await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="选择整理方式"]')!.click());
        const nestedMenu = document.body.querySelector('[role="menu"]')!;
        const separate = [...nestedMenu.querySelectorAll('[role="menuitem"]')].find((item) => item.textContent?.startsWith("仅消除重叠"))!;
        await act(async () => separate.dispatchEvent(new dom.PointerEvent("pointerdown", { bubbles: true })));
        assert.equal(closed, 0, "a portaled layout menu must not close its parent context menu on pointerdown");
        await act(async () => separate.dispatchEvent(new dom.MouseEvent("click", { bubbles: true })));
        assert.equal(modes.at(-1), "separate");
        assert.equal(closed, 1);
        assert.deepEqual(errors, []);
        await act(async () => root.unmount());
        await dom.happyDOM.abort();
        self.postMessage({ ok: true, result: { undo: true, redo: true, insertion: true, menuModes: 8, darkTheme: true } });
    } catch (error) {
        self.postMessage({ ok: false, error: error instanceof Error ? error.stack : String(error) });
    }
};
