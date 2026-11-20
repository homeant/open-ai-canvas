import { describe, expect, test } from "bun:test";
import { createPrevisActor, createPrevisScene, interpolatePrevisTransform, upsertPrevisKeyframe } from "../src/lib/canvas/previs/previs-scene";
import { applyPrevisShotEdit, resolvePrevisShotScene } from "../src/lib/canvas/previs/previs-shot-state";

describe("逐镜角色状态", () => {
    test("只在末尾录制第一帧也保留零秒的静态基准", () => {
        const scene = createPrevisScene();
        scene.objects[0].transform.rotation = [-Math.PI / 2, 0, 0];
        const changed = applyPrevisShotEdit(scene, (view) => ({ ...view, objects: view.objects.map((object) => ({ ...object, keyframes: [{ id: "end", time: 5, transform: { ...object.transform, rotation: [0, 0, 0] }, pose: "sit" }] })) }));
        const view = resolvePrevisShotScene(changed, 0);
        expect(view.objects[0].keyframes.length).toBe(2);
        expect(interpolatePrevisTransform(view.objects[0].transform, view.objects[0].keyframes, 0)).toEqual(scene.objects[0].transform);
    });
    test("第二镜坐起的姿态和变换不会污染首镜静态仰卧基准", () => {
        const scene = createPrevisScene();
        const actor = createPrevisActor("演员", [0, 0.9, 0]);
        actor.transform.rotation = [-Math.PI / 2, 0, 0];
        scene.objects = [actor];
        const first = scene.shots[0];
        const second = { ...first, id: "second", objectStates: undefined };
        scene.shots.push(second);
        scene.activeShotId = second.id;
        const changed = applyPrevisShotEdit(scene, (view) => ({
            ...view,
            objects: view.objects.map((object) => ({
                ...object,
                keyframes: [
                    { id: "start", time: 0, transform: object.transform, pose: "stand" },
                    { id: "end", time: 5, transform: { ...object.transform, rotation: [0, 0, 0] }, pose: "sit" },
                ],
            })),
        }));
        expect(changed.objects).toEqual(scene.objects);
        const firstView = resolvePrevisShotScene({ ...changed, activeShotId: first.id }, 0);
        expect(firstView.objects[0].transform).toEqual(actor.transform);
        expect(firstView.objects[0].keyframes).toEqual([]);
        const secondView = resolvePrevisShotScene(changed, 5);
        expect(secondView.objects[0].pose).toBe("sit");
        expect(interpolatePrevisTransform(secondView.objects[0].transform, secondView.objects[0].keyframes, 5).rotation).toEqual([0, 0, 0]);
        expect(resolvePrevisShotScene(changed, 0).objects[0].pose).toBe("stand");
        const reloaded = JSON.parse(JSON.stringify(changed));
        expect(resolvePrevisShotScene(reloaded, 5).objects[0].pose).toBe("sit");
    });

    test("同一时间点编辑、清空和撤销保留其他镜头轨道及骨骼", () => {
        let scene = createPrevisScene();
        const actor = scene.objects[0];
        const first = scene.shots[0];
        scene = applyPrevisShotEdit(scene, (view) => ({ ...view, objects: view.objects.map((object) => ({ ...object, keyframes: upsertPrevisKeyframe([], 0, object.transform), boneOverrides: { head: [0, 0, 0, 1] } })) }));
        const firstState = structuredClone(scene.shots[0].objectStates);
        scene = { ...scene, shots: [...scene.shots, { ...first, id: "second", objectStates: undefined }], activeShotId: "second" };
        const before = structuredClone(scene);
        scene = applyPrevisShotEdit(scene, (view) => ({ ...view, objects: view.objects.map((object) => ({ ...object, pose: "sit", keyframes: upsertPrevisKeyframe(object.keyframes, 0, { ...object.transform, position: [2, 0, 0] }) })) }));
        scene = applyPrevisShotEdit(scene, (view) => ({ ...view, objects: view.objects.map((object) => ({ ...object, keyframes: [] })) }));
        expect(scene.shots[0].objectStates).toEqual(firstState);
        expect(scene.objects[0]).toEqual(actor);
        expect(resolvePrevisShotScene(before).objects[0].pose).toBe(actor.pose);
        const removed = applyPrevisShotEdit(scene, (view) => ({ ...view, objects: [] }));
        expect(removed.shots.every((shot) => !shot.objectStates?.length)).toBe(true);
    });
});
