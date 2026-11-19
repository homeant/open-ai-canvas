import { expect, test } from "bun:test";
import { BoxGeometry, Group, Mesh, MeshBasicMaterial } from "three";
import { inspectPrevisFrame } from "../src/lib/canvas/previs/previs-frame-diagnostics";
import { createPrevisActor, createPrevisObject, createPrevisScene } from "../src/lib/canvas/previs/previs-scene";
import { evaluatePrevisActorPose } from "../src/lib/canvas/previs/previs-motion";

test("逐帧诊断按世界坐标报告重叠，且不把包围盒相交当成已确认穿模", () => {
    const scene = createPrevisScene();
    const actor = createPrevisActor("演员", [0, 1, 0]);
    const bed = createPrevisObject("box", "床", [0, 0.5, 0]);
    scene.objects = [actor, bed];
    const world = new Group();
    const geometry = new BoxGeometry(1, 2, 1),
        material = new MeshBasicMaterial();
    const actorRoot = new Group(),
        bedRoot = new Group();
    actorRoot.userData.previsObjectId = actor.id;
    bedRoot.userData.previsObjectId = bed.id;
    actorRoot.position.set(0, 1, 0);
    bedRoot.position.set(0, 0.5, 0);
    actorRoot.add(new Mesh(geometry, material));
    bedRoot.add(new Mesh(geometry, material));
    world.add(actorRoot, bedRoot);
    const result = inspectPrevisFrame(world, scene);
    expect(result.warnings.some((item) => item.code === "possible_object_overlap")).toBe(true);
    expect(result.collisionConfirmed).toBe(false);
    expect(result.ikConstraintsSupported).toBe(false);
    bedRoot.position.x = 10;
    expect(inspectPrevisFrame(world, scene).warnings.some((item) => item.code === "possible_object_overlap")).toBe(false);
    geometry.dispose();
    material.dispose();
});

test("录像与截图独立按目标时间求值姿态，不沿用 UI 播放头的静态预设", () => {
    const actor = createPrevisActor();
    actor.keyframes = [{ id: "pose", time: 2, transform: actor.transform, pose: "sit" }];
    expect(evaluatePrevisActorPose(actor, 0)).toEqual(evaluatePrevisActorPose({ ...actor, keyframes: [] }, 0));
    expect(evaluatePrevisActorPose(actor, 2)).toEqual(evaluatePrevisActorPose({ ...actor, pose: "sit", keyframes: [] }, 2));
    expect(actor.pose).toBe("stand");
});
