import type { PrevisObject, PrevisScene, PrevisShot, PrevisShotObjectState } from "@/types/previs";
import { resolvePrevisKeyframeProgress } from "./previs-scene";

const scopedFields = ["visible", "transform", "pose", "keyframes", "boneOverrides", "boneTracks", "contactConstraints", "motions", "motionPath", "motionClips", "activeMotionClipId"] as const;

export function resolvePrevisKeyframePose(object: Pick<PrevisObject, "pose" | "keyframes">, time: number) {
    return object.keyframes.filter((frame) => frame.pose && frame.time <= time).toSorted((a, b) => b.time - a.time)[0]?.pose || object.pose;
}

export function resolvePrevisPoseTransition(object: Pick<PrevisObject, "pose" | "keyframes">, time: number) {
    const frames = object.keyframes.filter((frame) => frame.pose).toSorted((a, b) => a.time - b.time);
    const previous = frames.filter((frame) => frame.time <= time).at(-1);
    const next = frames.find((frame) => frame.time > time);
    const from = previous?.pose || object.pose || "stand";
    if (!next) return { from, to: from, progress: 0 };
    const start = previous?.time || 0;
    const progress = resolvePrevisKeyframeProgress(Math.max(0, Math.min(1, (time - start) / Math.max(0.001, next.time - start))), previous?.easing);
    return { from, to: next.pose!, progress };
}

export function resolvePrevisShotObject(object: PrevisObject, shot?: PrevisShot | null, time?: number): PrevisObject {
    const state = shot?.objectStates?.find((item) => item.objectId === object.id);
    const result = { ...object };
    if (state) {
        for (const field of scopedFields) {
            if (Object.hasOwn(state, field)) Object.assign(result, { [field]: state[field] ?? undefined });
        }
        // A static shot override replaces a legacy scene-wide transform animation.
        if (state.transform && state.keyframes === undefined) {
            result.keyframes = [];
            result.motionPath = undefined;
        }
    }
    if (time !== undefined) {
        const pose = resolvePrevisKeyframePose(result, time);
        if (pose) result.pose = pose;
    }
    return result;
}

export function resolvePrevisShotScene(scene: PrevisScene, time?: number): PrevisScene {
    const shot = scene.shots.find((item) => item.id === scene.activeShotId) || scene.shots[0];
    return { ...scene, objects: scene.objects.map((object) => resolvePrevisShotObject(object, shot, time)) };
}

function snapshot(object: PrevisObject): PrevisShotObjectState {
    return {
        objectId: object.id,
        visible: object.visible,
        transform: object.transform,
        pose: object.pose,
        keyframes: object.keyframes,
        boneOverrides: object.boneOverrides || {},
        boneTracks: object.boneTracks || [],
        contactConstraints: object.contactConstraints || [],
        motions: object.motions || [],
        motionPath: object.motionPath || null,
        motionClips: object.motionClips || [],
        activeMotionClipId: object.activeMotionClipId || null,
    };
}

/** Persist an edit of the displayed shot without copying its pose or tracks into shared objects. */
export function applyPrevisShotEdit(scene: PrevisScene, update: (view: PrevisScene) => PrevisScene): PrevisScene {
    const view = resolvePrevisShotScene(scene);
    const next = update(view);
    const shot = scene.shots.find((item) => item.id === scene.activeShotId) || scene.shots[0];
    if (!shot) return next;
    const states = new Map((next.shots.find((item) => item.id === shot.id)?.objectStates || []).map((state) => [state.objectId, state]));
    const objects = next.objects.map((object) => {
        const base = scene.objects.find((item) => item.id === object.id);
        const before = view.objects.find((item) => item.id === object.id);
        if (!base || !before) return object;
        if (!before.keyframes.length && object.keyframes.length && object.keyframes[0].time > 0) {
            object = { ...object, keyframes: [{ id: `shot-base:${object.id}`, time: 0, transform: before.transform, pose: before.pose }, ...object.keyframes] };
        }
        if (scopedFields.some((field) => JSON.stringify(before[field]) !== JSON.stringify(object[field]))) states.set(object.id, snapshot(object));
        const shared = { ...object };
        for (const field of scopedFields) {
            if (Object.hasOwn(base, field)) Object.assign(shared, { [field]: base[field] });
            else delete shared[field];
        }
        return shared;
    });
    const ids = new Set(objects.map((object) => object.id));
    return {
        ...next,
        objects,
        shots: next.shots.map((item) => ({ ...item, objectStates: (item.id === shot.id ? [...states.values()] : item.objectStates)?.filter((state) => ids.has(state.objectId)) })),
    };
}
