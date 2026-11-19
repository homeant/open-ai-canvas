import { expect, test } from "bun:test";
import { buildNodeGenerationContext } from "../src/components/canvas/canvas-node-generation";
import { isCanvasNodeGenerating } from "../src/lib/canvas/canvas-node-task-state";
import { canvasNodeHasUnboundRecovery, endUnboundCanvasWaiting, settleCanvasGenerationPreparation, CLIENT_PREPARATION_FAILED, UNBOUND_CANVAS_SUBMISSION } from "../src/lib/canvas/canvas-task-state";
import { CanvasNodeType, type CanvasNodeData } from "../src/types/canvas";

function pending(): CanvasNodeData {
    return { id: "target", type: CanvasNodeType.Video, position: { x: 0, y: 0 }, width: 300, height: 200, metadata: { status: "loading", taskCreatedAt: "attempt-one", taskProgress: 0, taskStage: "正在准备参考素材", composerContent: "参考 @图片1" } };
}

test("未解析引用在提交前失败，持久化重载后可修正重试", () => {
    const node = pending();
    let error: unknown;
    try {
        buildNodeGenerationContext(node.id, [node], [], "参考 @图片1", []);
    } catch (value) {
        error = value;
    }
    expect(error).toBeInstanceOf(Error);
    const failed = settleCanvasGenerationPreparation(node, "attempt-one", "参考 @图片1", error);
    const restored = JSON.parse(JSON.stringify(failed)) as CanvasNodeData;
    expect(restored.metadata).toMatchObject({ status: "error", generationErrorCode: CLIENT_PREPARATION_FAILED });
    expect(restored.metadata?.errorDetails).toContain("@图片1");
    expect(restored.metadata?.taskId).toBeUndefined();
    expect(restored.metadata?.taskStage).toBeUndefined();
    expect(isCanvasNodeGenerating(restored)).toBe(false);
    const image = { ...node, id: "image", type: CanvasNodeType.Image, metadata: { content: "data:image/png;base64,AA==" } };
    const context = buildNodeGenerationContext(node.id, [restored, image], [{ id: "link", fromNodeId: image.id, toNodeId: node.id }], "参考 @图片1", []);
    expect(context.referenceImages).toHaveLength(1);
});

test("取消准备回到空闲，不覆盖新尝试、服务端任务和已有成功结果", () => {
    const node = pending();
    expect(settleCanvasGenerationPreparation(node, "attempt-one", "x").metadata?.status).toBe("idle");
    for (const changed of [
        { ...node, metadata: { ...node.metadata, taskCreatedAt: "attempt-two" } },
        { ...node, metadata: { ...node.metadata, taskId: "real-task", taskStatus: "queued" } },
        { ...node, metadata: { ...node.metadata, status: "success" as const, content: "saved-video" } },
    ])
        expect(settleCanvasGenerationPreparation(changed, "attempt-one", "x", new Error("late failure"))).toBe(changed);
});

test("历史无任务记录只能结束本地等待，不能宣称服务端任务失败", () => {
    const node = pending();
    expect(canvasNodeHasUnboundRecovery(node)).toBe(false);
    node.metadata!.errorDetails = "最近任务中未找到对应记录";
    expect(canvasNodeHasUnboundRecovery(node)).toBe(true);
    const ended = endUnboundCanvasWaiting(node);
    expect(ended.metadata).toMatchObject({ status: "error", generationErrorCode: UNBOUND_CANVAS_SUBMISSION });
    expect(ended.metadata?.taskStatus).toBeUndefined();
    expect(isCanvasNodeGenerating(ended)).toBe(false);
    for (const metadata of [
        { ...node.metadata, taskId: "server-task" },
        { ...node.metadata, agentGenerationContinuation: { taskId: "continuation" } },
        { ...node.metadata, isBatchRoot: true },
    ]) {
        expect(canvasNodeHasUnboundRecovery({ ...node, metadata } as CanvasNodeData)).toBe(false);
    }
});

test("实际 React 提交流程在无效引用后结束，并可修正后再次提交；旧等待重置不会调用生成", async () => {
    const result = await new Promise<{ submissions: number; apiCalls: number; confirmations: number }>((resolve, reject) => {
        const worker = new Worker(new URL("./helpers/canvas-preparation-flow.worker.tsx", import.meta.url).href, { type: "module" });
        worker.onmessage = (event) => {
            worker.terminate();
            if (event.data.ok) resolve(event.data.result);
            else reject(new Error(event.data.error));
        };
        worker.onerror = (event) => {
            worker.terminate();
            reject(event.error || new Error(event.message));
        };
        worker.postMessage("run");
    });
    expect(result.submissions).toBe(1);
    expect(result.apiCalls).toBe(0);
    expect(result.confirmations).toBe(1);
}, 15000);
