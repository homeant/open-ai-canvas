import { beforeAll, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { Group, Object3D, Quaternion, Vector3 } from "three";
import { GLTFLoader } from "three-stdlib";
import { buildPrevisMannequin, resolvePrevisMannequinProfile } from "../src/lib/canvas/previs/previs-mannequin";
import { evaluatePrevisActorPose } from "../src/lib/canvas/previs/previs-motion";
import { solvePrevisContacts } from "../src/lib/canvas/previs/previs-contact-constraints";
import { createPrevisActor } from "../src/lib/canvas/previs/previs-scene";
import type { PrevisContactConstraint, PrevisVec3 } from "../src/types/previs";

let template: Object3D;
beforeAll(async () => {
    const file = readFileSync(join(import.meta.dir, "../public/previs/mannequin.glb"));
    template = await new Promise<Object3D>((resolve, reject) => new GLTFLoader().parse(file.buffer.slice(file.byteOffset, file.byteOffset + file.byteLength), "", (gltf) => resolve(gltf.scene), reject));
});

test("stand → reach 在中间帧逐骨骼插值，step 明确保留首姿", () => {
    const actor = createPrevisActor();
    actor.keyframes = [
        { id: "a", time: 0, transform: actor.transform, pose: "stand" },
        { id: "b", time: 1.2, transform: actor.transform, pose: "reach" },
    ];
    const first = evaluatePrevisActorPose(actor, 0).rotations.rightUpperArm;
    const middle = evaluatePrevisActorPose(actor, 0.6).rotations.rightUpperArm;
    const last = evaluatePrevisActorPose(actor, 1.2).rotations.rightUpperArm;
    expect(new Quaternion(...first).angleTo(new Quaternion(...middle))).toBeGreaterThan(0.05);
    expect(new Quaternion(...middle).angleTo(new Quaternion(...last))).toBeGreaterThan(0.05);
    expect(new Quaternion(...first).slerp(new Quaternion(...last), 0.5).angleTo(new Quaternion(...middle))).toBeLessThan(1e-6);
    actor.keyframes[0].easing = "step";
    expect(evaluatePrevisActorPose(actor, 0.6).rotations.rightUpperArm).toEqual(first);
});

test("标准人偶手掌可跟随接触面局部目标，脚底接触返回实际误差", () => {
    const actor = buildPrevisMannequin({ template, profile: resolvePrevisMannequinProfile(), color: "#2f7de1", castShadow: true, receiveShadow: true });
    const world = new Group(),
        parent = new Group(),
        bed = new Group();
    parent.userData.previsObjectId = "actor";
    bed.userData.previsObjectId = "bed";
    world.add(parent, bed);
    parent.add(actor.root);
    actor.applyPose(evaluatePrevisActorPose({ pose: "stand", keyframes: [] }, 0));
    world.updateMatrixWorld(true);
    const rootBefore = actor.root.position.clone();
    const target = actor.contactMarkers.leftHand.getWorldPosition(new Vector3()).add(new Vector3(0.08, 0.12, 0.05));
    bed.position.copy(target);
    const constraint: PrevisContactConstraint = { id: "hand", effector: "leftHand", surfaceObjectId: "bed", target: [0, 0, 0], startTime: 0, endTime: 1.2, tolerance: 0.02 };
    const result = solvePrevisContacts(actor, world, [constraint], 0.6)[0];
    expect(result.status).toBe("satisfied");
    expect(result.errorMeters).toBeLessThanOrEqual(0.02);
    expect(actor.root.position.toArray()).toEqual(rootBefore.toArray());
    actor.applyPose(evaluatePrevisActorPose({ pose: "stand", keyframes: [] }, 0));
    world.updateMatrixWorld(true);
    const sole = actor.contactMarkers.leftFoot.getWorldPosition(new Vector3());
    const foot = solvePrevisContacts(actor, world, [{ ...constraint, id: "foot", effector: "leftFoot", surfaceObjectId: undefined, target: [sole.x, 0, sole.z] }], 0.6)[0];
    expect(foot.status).toBe("satisfied");
    expect(foot.actual![1]).toBeLessThan(0.025);
    expect(solvePrevisContacts(actor, world, [constraint], 1.3)).toEqual([]);
    actor.dispose();
});

test("不可达接触和丢失接触面不会伪报成功", () => {
    const actor = buildPrevisMannequin({ template, profile: resolvePrevisMannequinProfile(), color: "#2f7de1", castShadow: true, receiveShadow: true });
    const world = new Group(),
        parent = new Group();
    world.add(parent);
    parent.add(actor.root);
    actor.applyPose(evaluatePrevisActorPose({ pose: "stand", keyframes: [] }, 0));
    const constraint: PrevisContactConstraint = { id: "far", effector: "rightHand", target: [20, 0, 0], startTime: 0, endTime: 1, tolerance: 0.02 };
    const result = solvePrevisContacts(actor, world, [constraint], 0.5)[0];
    expect(result.status).toBe("unsatisfied");
    expect(result.errorMeters).toBeGreaterThan(1);
    expect(result.actual!.every(Number.isFinite)).toBe(true);
    expect(solvePrevisContacts(actor, world, [{ ...constraint, surfaceObjectId: "missing" }], 0.5)[0].status).toBe("surface_unavailable");
    parent.scale.set(2, 1, 1);
    expect(solvePrevisContacts(actor, world, [constraint], 0.5)[0].status).toBe("unsupported_scale");
    actor.dispose();
});
