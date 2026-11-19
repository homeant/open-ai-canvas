import { Object3D, Quaternion, Vector3 } from "three";
import type { PrevisContactConstraint, PrevisContactEffector, PrevisVec3 } from "@/types/previs";
import type { PrevisMannequin } from "./previs-mannequin";

export function readPrevisRigState(actor: PrevisMannequin) {
    actor.root.updateMatrixWorld(true);
    return {
        boneRotationSpace: "canonical_local_xyzw",
        jointPositionSpace: "world_meters",
        boneRotations: Object.fromEntries(Object.entries(actor.bones).map(([bone, value]) => [bone, value.quaternion.toArray()])),
        jointPositions: Object.fromEntries(Object.entries(actor.joints).map(([bone, value]) => [bone, value.getWorldPosition(new Vector3()).toArray()])),
        contactMarkerPositions: Object.fromEntries(Object.entries(actor.contactMarkers).map(([effector, value]) => [effector, value.getWorldPosition(new Vector3()).toArray()])),
    };
}

export type PrevisContactResult = {
    id: string;
    effector: PrevisContactEffector;
    status: "satisfied" | "unsatisfied" | "surface_unavailable" | "unsupported_scale";
    target?: PrevisVec3;
    actual?: PrevisVec3;
    errorMeters?: number;
    tolerance: number;
    method: "standard_mannequin_position_ccd";
};

const chains = {
    leftHand: ["leftLowerArm", "leftUpperArm"],
    rightHand: ["rightLowerArm", "rightUpperArm"],
    leftFoot: ["leftLowerLeg", "leftUpperLeg"],
    rightFoot: ["rightLowerLeg", "rightUpperLeg"],
} as const;

/** Position-only limb CCD. It never moves the actor root or certifies mesh contact/balance. */
export function solvePrevisContacts(actor: PrevisMannequin, world: Object3D, constraints: PrevisContactConstraint[], time: number): PrevisContactResult[] {
    const active = constraints.filter((item) => item.startTime <= time && time <= item.endTime);
    if (!active.length) return [];
    world.updateMatrixWorld(true);
    const surfaces = new Map<string, Object3D>();
    world.traverse((node) => {
        if (node.userData.previsObjectId && node.visible) surfaces.set(node.userData.previsObjectId, node);
    });
    const actorScale = actor.root.parent?.getWorldScale(new Vector3()) || new Vector3(1, 1, 1);
    const validScale = actorScale.toArray().every((value) => value > 0) && Math.max(...actorScale.toArray()) - Math.min(...actorScale.toArray()) < 1e-5;
    const prepared = active.map((constraint) => {
        const target = new Vector3(...constraint.target);
        const surface = constraint.surfaceObjectId ? surfaces.get(constraint.surfaceObjectId) : null;
        if (surface) target.applyMatrix4(surface.matrixWorld);
        return { constraint, target, available: !constraint.surfaceObjectId || Boolean(surface) };
    });
    if (validScale) {
        for (let iteration = 0; iteration < 32; iteration++) {
            let allReached = true;
            for (const { constraint, target, available } of prepared) {
                if (!available) continue;
                const marker = actor.contactMarkers[constraint.effector];
                if (marker.getWorldPosition(new Vector3()).distanceTo(target) <= constraint.tolerance) continue;
                allReached = false;
                for (const boneName of chains[constraint.effector]) {
                    const pivot = actor.joints[boneName].getWorldPosition(new Vector3());
                    const from = marker.getWorldPosition(new Vector3()).sub(pivot);
                    const to = target.clone().sub(pivot);
                    if (from.lengthSq() < 1e-12 || to.lengthSq() < 1e-12) continue;
                    const delta = new Quaternion().setFromUnitVectors(from.normalize(), to.normalize());
                    // Bound each iteration to avoid flipping a limb around a nearly opposite direction.
                    const bounded = new Quaternion().slerp(delta, Math.min(1, 0.35 / Math.max(delta.angleTo(new Quaternion()), 1e-9)));
                    const bone = actor.bones[boneName];
                    const parent = bone.parent!.getWorldQuaternion(new Quaternion());
                    bone.quaternion.premultiply(parent.clone().invert().multiply(bounded).multiply(parent)).normalize();
                    actor.sync(false);
                    world.updateMatrixWorld(true);
                }
            }
            if (allReached) break;
        }
    }
    return prepared.map(({ constraint, target, available }) => {
        const actual = actor.contactMarkers[constraint.effector].getWorldPosition(new Vector3());
        const errorMeters = actual.distanceTo(target);
        return {
            id: constraint.id,
            effector: constraint.effector,
            status: !validScale ? "unsupported_scale" : !available ? "surface_unavailable" : errorMeters <= constraint.tolerance ? "satisfied" : "unsatisfied",
            target: target.toArray() as PrevisVec3,
            actual: actual.toArray() as PrevisVec3,
            errorMeters,
            tolerance: constraint.tolerance,
            method: "standard_mannequin_position_ccd",
        };
    });
}
