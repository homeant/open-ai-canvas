import { useCallback, useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";

import { PrevisViewport, type PrevisViewportHandle } from "@/components/canvas/previs/previs-viewport";
import type { PrevisLoadSignal } from "@/lib/canvas/previs/previs-recovery";
import type { PrevisScene } from "@/types/previs";
import { isPrevisStandardActor, loadPrevisMannequinTemplate } from "@/lib/canvas/previs/previs-mannequin";

type RenderRequest = { scene: PrevisScene; shotId: string; kind: "frame" | "video" | "frames"; time?: number; times?: number[]; duration?: number; fps?: number; longEdge?: number };
type RenderImage = { dataBase64: string; mimeType: string; bytes: number };
type RenderResult = RenderImage | { frames: Array<RenderImage & { time: number; diagnostics: unknown }> };

declare global {
    interface Window {
        yingcePrevisRender?: (request: RenderRequest) => Promise<RenderResult>;
    }
}

const pause = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms));

async function blobResult(blob: Blob): Promise<RenderImage> {
    const dataURL = await new Promise<string>((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = () => resolve(String(reader.result));
        reader.onerror = () => reject(new Error("读取预演产物失败"));
        reader.readAsDataURL(blob);
    });
    return { dataBase64: dataURL.slice(dataURL.indexOf(",") + 1), mimeType: blob.type, bytes: blob.size };
}

function RenderHost() {
    const viewport = useRef<PrevisViewportHandle>(null);
    const [scene, setScene] = useState<PrevisScene | null>(null);
    const [playhead, setPlayhead] = useState(0);
    const loads = useRef<Map<string, PrevisLoadSignal>>(new Map());
    const busy = useRef(false);
    const onModelLoadStateChange = useCallback((id: string, signal: PrevisLoadSignal) => {
        loads.current.set(id, signal);
    }, []);

    useEffect(() => {
        window.yingcePrevisRender = async (request) => {
            if (busy.current) throw new Error("预演渲染器正忙");
            busy.current = true;
            try {
                const shot = request.scene.shots.find((item) => item.id === request.shotId);
                if (!shot) throw new Error("预演镜头不存在");
                const next = { ...request.scene, activeShotId: shot.id };
                const shotStates = new Map(shot.objectStates?.map((state) => [state.objectId, state] as const) ?? []);
                if (next.objects.some((item) => (shotStates.get(item.id)?.visible ?? item.visible) && isPrevisStandardActor(item))) await loadPrevisMannequinTemplate();
                loads.current = new Map(next.objects.filter((item) => (shotStates.get(item.id)?.visible ?? item.visible) && (item.kind === "model" || item.kind === "actor") && item.url).map((item) => [item.id, "loading" as const]));
                setScene(next);
                setPlayhead(request.time ?? 0);
                await pause(40);
                const deadline = performance.now() + 30_000;
                while (performance.now() < deadline) {
                    const ready = document.querySelector('.previs-viewport-shell[data-renderer-ready="true"]');
                    const statuses = [...loads.current.values()];
                    if (statuses.includes("error")) throw new Error("3D 模型加载失败");
                    if (ready && statuses.every((status) => status === "ready")) break;
                    await pause(40);
                }
                if (!document.querySelector('.previs-viewport-shell[data-renderer-ready="true"]') || [...loads.current.values()].some((status) => status !== "ready")) throw new Error("预演视口或模型加载超时");
                const handle = viewport.current;
                if (!handle) throw new Error("预演视口未就绪");
                await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));
                if (request.kind === "frames") {
                    if (!request.times?.length || request.times.length > 8) throw new Error("批量取样需要 1–8 个时间点");
                    const frames: Array<RenderImage & { time: number; diagnostics: unknown }> = [];
                    for (const time of request.times) {
                        if (!Number.isFinite(time) || time < 0 || time > shot.duration) throw new Error("取样时间超出镜头时长");
                        setPlayhead(time);
                        await pause(0);
                        await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));
                        const image = await blobResult(await handle.capture("clay", { longEdge: request.longEdge ?? 640 }));
                        if (!handle.inspectFrame) throw new Error("预演视口没有诊断接口");
                        frames.push({ ...image, time, diagnostics: handle.inspectFrame() });
                    }
                    return { frames };
                }
                const blob =
                    request.kind === "frame" ? await handle.capture("clay", { longEdge: request.longEdge ?? 1280 }) : await handle.recordVideo(request.duration ?? shot.duration, request.fps ?? shot.fps, "clay", { longEdge: request.longEdge ?? 720 });
                return blobResult(blob);
            } finally {
                busy.current = false;
            }
        };
        return () => {
            delete window.yingcePrevisRender;
        };
    }, []);

    return scene ? (
        <PrevisViewport
            ref={viewport}
            scene={scene}
            selectedObjectId={null}
            selectedBone={null}
            transformMode="select"
            renderMode="clay"
            playhead={playhead}
            playing={false}
            viewMode="camera"
            showNavigation={false}
            showModelLoadNotice={false}
            onSelectObject={() => {}}
            onSelectBone={() => {}}
            onObjectTransform={() => {}}
            onBoneTransform={() => {}}
            onActorRigReady={() => {}}
            onModelLoadStateChange={onModelLoadStateChange}
        />
    ) : null;
}

createRoot(document.getElementById("root")!).render(<RenderHost />);
