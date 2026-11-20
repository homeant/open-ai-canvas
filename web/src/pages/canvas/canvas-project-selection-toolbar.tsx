import { useTranslation } from "react-i18next";
import type { RefObject } from "react";

import { CanvasSelectionToolbar } from "@/components/canvas/canvas-workspace-overlays";
import { CanvasArrangeControl } from "@/components/canvas/canvas-arrange-control";
import type { CanvasArrangeMode } from "@/lib/canvas/canvas-smart-layout";
import { FloatingDock } from "@/components/ui/aceternity/floating-dock";
import { canvasThemes } from "@/lib/canvas-theme";
import { canvasDockStyle } from "@/lib/canvas/canvas-aceternity-style";
import { defaultToolbarPrefs, readToolbarPrefs, resolveToolbarEntries, type ToolContext, type ToolbarHandlers } from "@/lib/canvas/tool-registry";
import type { CanvasAlignmentMode } from "@/lib/canvas/canvas-layout";
import { useCanvasThemeStore } from "@/stores/canvas/use-canvas-theme-store";

type CanvasProjectSelectionToolbarProps = {
    anchorRef: RefObject<HTMLDivElement | null>;
    containerRef: RefObject<HTMLDivElement | null>;
    count: number;
    selectedVideoCount: number;
    mergingVideos: boolean;
    onAlign: (mode: CanvasAlignmentMode) => void;
    onArrange: (mode: CanvasArrangeMode) => void;
    onCreateStoryboard: () => void;
    onCreateReferenceGroup: () => void;
    onBatchConnect: () => void;
    onCompareImages?: () => void;
    onSendSelectionToAgent: () => void;
};

export function CanvasProjectSelectionToolbar({
    anchorRef,
    containerRef,
    count,
    selectedVideoCount,
    mergingVideos,
    onAlign,
    onArrange,
    onCreateStoryboard,
    onCreateReferenceGroup,
    onBatchConnect,
    onCompareImages,
    onSendSelectionToAgent,
}: CanvasProjectSelectionToolbarProps) {
    const { t } = useTranslation();
    const theme = canvasThemes[useCanvasThemeStore((state) => state.theme)];

    const handlers = {
        onAlign,
        onArrange,
        onCreateStoryboard,
        onCreateReferenceGroup,
        onBatchConnect,
        onCompareImages,
        onSendSelectionToAgent,
    } as Partial<ToolbarHandlers> as ToolbarHandlers;

    const ctx: ToolContext = {
        selectedCount: count,
        selectedNodeTypes: new Set(),
        selectedVideoCount,
        canvasTool: "move",
        workspaceMode: "professional",
        isProjectLinked: false,
        canUndo: false,
        canRedo: false,
        extractingVideoFrames: false,
        extractingAudio: false,
        trimmingVideo: false,
        mergingVideos,
        addPanelOpen: false,
        appearancePanelOpen: false,
        settingsPanelOpen: false,
        handlers,
    };

    const prefs = readToolbarPrefs("selection") ?? defaultToolbarPrefs("selection");
    const items = resolveToolbarEntries("selection", ctx, prefs);

    return (
        <CanvasSelectionToolbar anchorRef={anchorRef} containerRef={containerRef} count={count}>
            <div className="flex max-w-full items-center gap-2 overflow-x-auto">
                <CanvasArrangeControl selectedCount={count} onArrange={onArrange} />
                <FloatingDock items={items} size="compact" className="canvas-floating-dock" style={canvasDockStyle(theme)} ariaLabel={t("canvasPage.selectionToolbar.ariaLabel")} />
            </div>
        </CanvasSelectionToolbar>
    );
}
