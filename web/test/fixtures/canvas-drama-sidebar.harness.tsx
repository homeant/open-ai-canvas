import { afterAll, expect, mock, test } from "bun:test";
import { Window } from "happy-dom";
import type { DramaCast, DramaSummary } from "../../src/services/api/drama";

const dom = new Window({ url: "http://localhost/canvas/current" });
for (const [key, value] of Object.entries({
    window: dom,
    document: dom.document,
    navigator: dom.navigator,
    HTMLElement: dom.HTMLElement,
    Element: dom.Element,
    Node: dom.Node,
    SVGElement: dom.SVGElement,
    Event: dom.Event,
    MouseEvent: dom.MouseEvent,
    KeyboardEvent: dom.KeyboardEvent,
    ResizeObserver: dom.ResizeObserver,
    getComputedStyle: dom.getComputedStyle.bind(dom),
    IS_REACT_ACT_ENVIRONMENT: true,
}))
    Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });

const { act } = await import("react");
const { createRoot } = await import("react-dom/client");
const { MemoryRouter } = await import("react-router");
const { CanvasDramaSidebar } = await import("../../src/components/canvas/canvas-drama-sidebar");

afterAll(() => dom.happyDOM.abort());

function cast(id: string, kind: DramaCast["kind"], name: string, position = 0): DramaCast {
    return {
        id,
        projectId: "drama",
        kind,
        name,
        aliasesJson: "[]",
        role: "",
        description: "",
        appearance: "",
        voiceInstructions: "",
        importance: 1,
        imageStatus: "draft",
        imageVersion: 0,
        locked: false,
        position,
    };
}

const summary = {
    project: { id: "drama", name: "测试短剧" },
    production: { status: "draft", spentMicrocredits: 0, budgetMicrocredits: 0, canvasSpentMicrocredits: 0 },
    episodes: [],
} as unknown as DramaSummary;

function tab(host: ParentNode, label: string) {
    const result = [...host.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find((button) => button.textContent?.replace(/\s/g, "").startsWith(label));
    expect(result, `${label}选项卡`).toBeDefined();
    return result!;
}

test("选项卡显示分类数量、过滤内容并支持键盘切换", async () => {
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    const onProjectCast = mock(() => {});
    await act(async () =>
        root.render(
            <MemoryRouter>
                <CanvasDramaSidebar
                    summary={summary}
                    cast={[cast("hero", "character", "林溪"), cast("friend", "character", "周遥", 1), cast("school", "scene", "旧校舍"), cast("letter", "prop", "未寄出的信")]}
                    presence={{ cast: new Map(), segment: new Map(), final: new Map() }}
                    busyKey={null}
                    onProjectCast={onProjectCast}
                    onProjectSegment={() => {}}
                    onProjectEpisode={() => {}}
                    onProjectFinal={() => {}}
                />
            </MemoryRouter>,
        ),
    );

    try {
        expect(tab(host, "角色").textContent?.replace(/\s/g, "")).toBe("角色2");
        expect(tab(host, "场景").textContent?.replace(/\s/g, "")).toBe("场景1");
        expect(tab(host, "道具").textContent?.replace(/\s/g, "")).toBe("道具1");
        expect(tab(host, "角色").getAttribute("aria-selected")).toBe("true");
        expect(host.textContent).toContain("林溪");
        expect(host.textContent).not.toContain("旧校舍");

        await act(async () => tab(host, "场景").click());
        expect(tab(host, "场景").getAttribute("aria-selected")).toBe("true");
        expect(host.textContent).toContain("旧校舍");
        expect(host.textContent).not.toContain("林溪");
        await act(async () => [...host.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("旧校舍"))!.click());
        expect(onProjectCast).toHaveBeenCalledWith("school");

        await act(async () => tab(host, "场景").dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "ArrowRight" })));
        expect(tab(host, "道具").getAttribute("aria-selected")).toBe("true");
        expect(document.activeElement).toBe(tab(host, "道具"));
        expect(host.textContent).toContain("未寄出的信");
    } finally {
        await act(async () => root.unmount());
        host.remove();
    }
});
