import { Button, Dropdown } from "antd";
import { useRef, useState } from "react";
import { ChevronDown, LayoutTemplate } from "lucide-react";
import { CANVAS_ARRANGE_OPTIONS, type CanvasArrangeMode } from "@/lib/canvas/canvas-smart-layout";
import { canvasThemes } from "@/lib/canvas-theme";
import { useActiveTheme } from "@/stores/canvas/use-canvas-theme-store";

export function CanvasArrangeControl({ selectedCount = 0, onArrange }: { selectedCount?: number; onArrange: (mode: CanvasArrangeMode) => void }) {
    const scope = selectedCount ? `选中 ${selectedCount} 个节点（组合整体移动）` : "整张画布（保留锁定节点）";
    const theme = canvasThemes[useActiveTheme()];
    const [open, setOpen] = useState(false);
    const triggerRef = useRef<HTMLButtonElement>(null);
    return (
        <div
            data-canvas-no-zoom
            className="inline-flex shrink-0 items-center rounded-[var(--r-lg)] border p-1"
            style={{ background: theme.spatial.elevated, borderColor: theme.toolbar.border, color: theme.node.text }}
            onPointerDown={(event) => event.stopPropagation()}
            onMouseDown={(event) => event.stopPropagation()}
            onWheel={(event) => event.stopPropagation()}
            onKeyDown={(event) => event.stopPropagation()}
        >
            <Button
                size="small"
                type="text"
                aria-label="智能整理"
                icon={<LayoutTemplate className="size-4" />}
                title={`智能整理：${scope}`}
                onClick={() => {
                    setOpen(false);
                    onArrange("auto");
                }}
            >
                智能整理
            </Button>
            <Dropdown
                trigger={["click"]}
                placement="topRight"
                open={open}
                onOpenChange={setOpen}
                menu={{
                    items: [
                        { key: "scope", label: scope, disabled: true },
                        { type: "divider" },
                        ...CANVAS_ARRANGE_OPTIONS.map((option) => ({
                            key: option.mode,
                            label: (
                                <span className="flex flex-col gap-0.5 py-1">
                                    <span>{option.label}</span>
                                    <span className="text-[var(--fs-caption)] text-[var(--text-secondary)]">{option.description}</span>
                                </span>
                            ),
                        })),
                    ],
                    onClick: ({ key }) => {
                        setOpen(false);
                        onArrange(key as CanvasArrangeMode);
                    },
                }}
                popupRender={(menu) => (
                    <div
                        data-canvas-no-zoom
                        data-canvas-wheel-scroll
                        onPointerDown={(event) => event.stopPropagation()}
                        onWheel={(event) => event.stopPropagation()}
                        onKeyDown={(event) => {
                            event.stopPropagation();
                            if (event.key === "Escape") {
                                event.preventDefault();
                                setOpen(false);
                                triggerRef.current?.focus();
                            }
                        }}
                    >
                        {menu}
                    </div>
                )}
            >
                <Button ref={triggerRef} size="small" type="text" aria-label="选择整理方式" aria-haspopup="menu" aria-expanded={open} icon={<ChevronDown className="size-3.5" />} />
            </Dropdown>
        </div>
    );
}
