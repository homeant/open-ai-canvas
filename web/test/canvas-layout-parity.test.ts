import { expect, test } from "bun:test";
import { layoutCanvasFlow } from "@/lib/canvas/canvas-layout";
import { CanvasNodeType, type CanvasNodeData, type Position } from "@/types/canvas";
import fixtures from "../../backend/internal/canvas/layout/testdata/canvas-layout.json";

test("frontend and Agent consume the same layout coordinates and remain stable", () => {
    const project = (item: { id: string; createdAt?: string; lane?: string; x?: number; y?: number; width: number; height: number }): CanvasNodeData => ({
        id: item.id,
        title: item.id,
        createdAt: item.createdAt,
        type: (item.lane || "text") as CanvasNodeType,
        position: { x: item.x || 0, y: item.y || 0 },
        width: item.width,
        height: item.height,
    });
    fixtures.cases.forEach((fixture) => {
        const nodes = fixture.nodes.map(project);
        const obstacles = ("obstacles" in fixture ? fixture.obstacles! : []).map(project);
        const edges = fixture.edges.map((edge, i) => ({ id: String(i), fromNodeId: edge.from, toNodeId: edge.to }));
        const positions = layoutCanvasFlow(nodes, edges, obstacles);
        expect(Object.fromEntries(positions), fixture.name).toEqual(fixture.expected as Record<string, Position>);
        expect(
            layoutCanvasFlow(
                nodes.map((node) => ({ ...node, position: positions.get(node.id)! })),
                edges,
                obstacles,
            ),
            fixture.name,
        ).toEqual(positions);
    });
});
