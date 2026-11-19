import { mock } from "bun:test";
import assert from "node:assert/strict";
import { Window } from "happy-dom";

self.onmessage = async () => {
    try {
        const dom = new Window({ url: "http://localhost/" });
        for (const [key, value] of Object.entries({
            window: dom,
            document: dom.document,
            navigator: dom.navigator,
            HTMLElement: dom.HTMLElement,
            Node: dom.Node,
            Element: dom.Element,
            SVGElement: dom.SVGElement,
            localStorage: dom.localStorage,
            sessionStorage: dom.sessionStorage,
            getComputedStyle: dom.getComputedStyle.bind(dom),
            IS_REACT_ACT_ENVIRONMENT: true,
        }))
            Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });
        const React = await import("react");
        const { act, useState } = React;
        const { createRoot } = await import("react-dom/client");
        const antd = await import("antd");
        let confirmations = 0;
        const app = {
            message: { error: () => {}, info: () => {}, warning: () => {} },
            modal: {
                confirm: (options: { onCancel: () => void }) => {
                    confirmations++;
                    options.onCancel();
                },
            },
        };
        mock.module("antd", () => ({ ...antd, App: { ...antd.App, useApp: () => app } }));
        const configuration = await import("../../src/stores/use-config-store");
        const config = { ...configuration.defaultConfig, model: "fixture-video", videoModel: "fixture-video" };
        const useConfigStore = Object.assign((selector: (state: unknown) => unknown) => selector({ config, isAiConfigReady: () => true }), configuration.useConfigStore);
        mock.module("@/stores/use-config-store", () => ({ ...configuration, useConfigStore, useEffectiveConfig: () => config }));
        const projectGeneration = await import("../../src/lib/canvas/canvas-project-generation");
        mock.module("@/lib/canvas/canvas-project-generation", () => ({ ...projectGeneration, buildGenerationConfig: () => ({ ...config, model: "fixture-video", taskWorkflowProvider: "model" }) }));
        const selection = await import("../../src/lib/model-selection");
        mock.module("@/lib/model-selection", () => ({ ...selection, modelCompatibilityError: () => "", modelGroupReferenceLimits: () => undefined, modelRequestOptions: () => ({}) }));
        const capabilities = await import("../../src/lib/model-capabilities");
        mock.module("@/lib/model-capabilities", () => ({ ...capabilities, modelCapabilityConfigFor: () => ({ video: { references: { minImages: 0 } } }) }));
        const sync = await import("../../src/services/user-data-sync");
        mock.module("@/services/user-data-sync", () => ({ ...sync, loadPromptMentionedAssets: async () => [] }));
        const contextModule = await import("../../src/components/canvas/canvas-node-generation");
        mock.module("@/components/canvas/canvas-node-generation", () => ({ ...contextModule, hydrateNodeGenerationContext: async (context: Record<string, unknown>) => ({ ...context, resolvedCharacterVoices: [], resolvedCharacterVersions: [] }) }));
        const skills = await import("../../src/services/skill-runtime");
        mock.module("@/services/skill-runtime", () => ({ ...skills, skillRuntime: { ...skills.skillRuntime, prepare: async (input: { prompt: string }) => ({ prompt: input.prompt, metadata: {} }) } }));
        let submissions = 0;
        const media = await import("../../src/pages/canvas/canvas-media-generation-executors");
        mock.module("../../src/pages/canvas/canvas-media-generation-executors", () => ({
            ...media,
            executeVideoGeneration: async (input: { nodeId: string; bindGenerationTask: (id: string, task: unknown) => void }) => {
                submissions++;
                input.bindGenerationTask(input.nodeId, { id: "fake-task", status: "queued" });
            },
        }));
        const { apiClient } = await import("../../src/services/api/request");
        let apiCalls = 0;
        apiClient.defaults.adapter = async () => {
            apiCalls++;
            throw new Error("Unexpected network request");
        };
        const { CanvasNodeType } = await import("../../src/types/canvas");
        type Node = import("../../src/types/canvas").CanvasNodeData;
        const { useCanvasGenerationExecutor } = await import("../../src/pages/canvas/use-canvas-generation-executor");
        const { useCanvasGenerationRetry } = await import("../../src/pages/canvas/use-canvas-generation-retry");
        const { LoadingContent, ErrorContent } = await import("../../src/components/canvas/canvas-node-status-content");
        const { canvasThemes } = await import("../../src/lib/canvas-theme");
        const { isCanvasNodeGenerating } = await import("../../src/lib/canvas/canvas-node-task-state");
        let initial: Node[] = [{ id: "target", type: CanvasNodeType.Video, position: { x: 0, y: 0 }, width: 300, height: 200, metadata: { status: "idle", composerContent: "参考 @图片1" } }];
        const nodesRef = { current: initial };
        const connectionsRef = { current: [] as import("../../src/types/canvas").CanvasConnection[] };
        let controls: { submit: ReturnType<typeof useCanvasGenerationExecutor>; retry: ReturnType<typeof useCanvasGenerationRetry>; setNodes: React.Dispatch<React.SetStateAction<Node[]>>; running: string | null };
        function Harness() {
            const [nodes, setNodes] = useState(initial);
            const [running, setRunningNodeId] = useState<string | null>(null);
            nodesRef.current = nodes;
            const shared = {
                projectId: "fixture-canvas",
                addedSkills: [],
                assets: [],
                nodesRef,
                connectionsRef,
                setNodes,
                setRunningNodeId,
                startGenerationRequest: () => new AbortController(),
                finishGenerationRequest: () => {},
                applyGenerationTaskResult: async () => {},
                bindGenerationTask: (id: string, task: { id: string; status: string }) =>
                    setNodes((items) => items.map((node) => (node.id === id ? { ...node, metadata: { ...node.metadata, taskId: task.id, taskStatus: task.status, status: "loading" } } : node))),
            };
            const submit = useCanvasGenerationExecutor({ ...shared, setConnections: () => {}, setSelectedNodeIds: () => {}, setSelectedConnectionId: () => {}, setDialogNodeId: () => {} });
            const retry = useCanvasGenerationRetry(shared);
            controls = { submit, retry, setNodes, running };
            const node = nodes[0];
            return node.metadata?.status === "error" ? <ErrorContent node={node} theme={canvasThemes.dark} onRetry={retry} /> : node.metadata?.status === "loading" ? <LoadingContent node={node} theme={canvasThemes.dark} onRetry={retry} /> : null;
        }
        const host = dom.document.createElement("div");
        dom.document.body.append(host);
        let root = createRoot(host as unknown as HTMLElement);
        await act(async () => root.render(<Harness />));
        await act(async () => {
            await controls.submit("target", "video", "参考 @图片1");
        });
        assert.equal(nodesRef.current[0].metadata?.status, "error");
        assert.match(nodesRef.current[0].metadata?.errorDetails || "", /@图片1/);
        assert.equal(isCanvasNodeGenerating(nodesRef.current[0]), false);
        assert.equal(controls.running, null);
        assert.equal(submissions, 0);
        assert.match(host.textContent || "", /提交前检查未通过/);
        assert.match(host.textContent || "", /检查引用后重试/);
        initial = JSON.parse(JSON.stringify(nodesRef.current));
        await act(async () => root.unmount());
        root = createRoot(host as unknown as HTMLElement);
        await act(async () => root.render(<Harness />));
        assert.equal(isCanvasNodeGenerating(nodesRef.current[0]), false);
        connectionsRef.current = [{ id: "link", fromNodeId: "image", toNodeId: "target" }];
        await act(async () => controls.setNodes((nodes) => [...nodes, { id: "image", type: CanvasNodeType.Image, position: { x: 400, y: 0 }, width: 100, height: 100, metadata: { content: "data:image/png;base64,AA==" } }]));
        await act(async () => {
            await controls.submit("target", "video", "参考 @图片1");
        });
        assert.equal(submissions, 1);
        assert.equal(nodesRef.current[0].metadata?.taskId, "fake-task");
        await act(async () => controls.setNodes((nodes) => [{ ...nodes[0], metadata: { status: "loading", composerContent: "参考 @图片1", errorDetails: "最近任务中未找到对应记录" } }]));
        assert.match(host.textContent || "", /结束等待/);
        const end = [...host.querySelectorAll("button")].find((button) => button.textContent?.includes("结束等待"));
        assert.ok(end);
        await act(async () => {
            end.click();
        });
        assert.equal(nodesRef.current[0].metadata?.status, "error");
        assert.equal(submissions, 1);
        const retry = [...host.querySelectorAll("button")].find((button) => button.textContent?.includes("核对后重新提交"));
        assert.ok(retry);
        await act(async () => {
            retry.click();
        });
        assert.equal(confirmations, 1);
        assert.equal(submissions, 1);
        assert.equal(apiCalls, 0);
        await act(async () => root.unmount());
        dom.happyDOM.abort();
        self.postMessage({ ok: true, result: { submissions, apiCalls, confirmations } });
    } catch (error) {
        self.postMessage({ ok: false, error: error instanceof Error ? error.stack || error.message : String(error) });
    }
};
