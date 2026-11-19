import { Box3, Object3D } from "three";
import type { PrevisScene } from "@/types/previs";
import { resolvePrevisShotScene } from "./previs-shot-state";

/** World-space bounds are a broad-phase warning, not a contact or IK solver. */
export function inspectPrevisFrame(world: Object3D, scene: PrevisScene) {
    world.updateMatrixWorld(true);
    const objects = new Map(
        resolvePrevisShotScene(scene)
            .objects.filter((object) => object.visible)
            .map((object) => [object.id, object]),
    );
    const bounds: Array<{ id: string; kind: string; box: Box3 }> = [];
    world.traverse((node) => {
        const object = objects.get(node.userData.previsObjectId);
        if (!object || !node.visible) return;
        const box = new Box3().setFromObject(node, true);
        if (!box.isEmpty() && [...box.min.toArray(), ...box.max.toArray()].every(Number.isFinite)) bounds.push({ id: object.id, kind: object.kind === "actor" || object.primitive === "character" ? "actor" : object.kind, box });
    });
    const warnings: Array<Record<string, unknown>> = [];
    for (const id of objects.keys()) {
        if (!bounds.some((item) => item.id === id)) warnings.push({ code: "bounds_unavailable", objectId: id });
    }
    for (const object of bounds) {
        if (object.box.min.y < -0.02) warnings.push({ code: "below_ground_bounds", objectId: object.id, minimumY: object.box.min.y });
    }
    for (let a = 0; a < bounds.length; a++) {
        for (let b = a + 1; b < bounds.length; b++) {
            const first = bounds[a],
                second = bounds[b];
            if (first.kind !== "actor" && second.kind !== "actor") continue;
            const overlap = first.box.clone().intersect(second.box);
            if (overlap.isEmpty() || overlap.max.toArray().some((value, axis) => value - overlap.min.toArray()[axis] <= 0.02)) continue;
            warnings.push({ code: "possible_object_overlap", objectIds: [first.id, second.id], overlapMin: overlap.min.toArray(), overlapMax: overlap.max.toArray(), collisionConfirmed: false });
            if (warnings.length >= 64) break;
        }
        if (warnings.length >= 64) break;
    }
    return {
        method: "rendered_world_aabb",
        certainty: "broad_phase",
        complete: bounds.length === objects.size,
        collisionConfirmed: false,
        ikConstraintsSupported: false,
        contactConstraints: "not_solved",
        warnings,
        objects: bounds.map(({ id, box }) => ({ id, min: box.min.toArray(), max: box.max.toArray() })),
    };
}
