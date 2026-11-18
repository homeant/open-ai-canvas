import assert from "node:assert/strict";
import test from "node:test";

// @ts-expect-error -- Node native TypeScript tests require the extension.
import { logStatus } from "./log-view.ts";

test("completed task overrides an old processing create log", () => {
    assert.deepEqual(logStatus({ status: "succeeded", providerStatus: "processing", taskStatus: "succeeded" }), {
        label: "成功",
        tone: "success",
    });
});

test("a failed provider request remains failed after the task succeeds", () => {
    assert.deepEqual(logStatus({ status: "failed", providerStatus: "", taskStatus: "succeeded" }), {
        label: "失败",
        tone: "error",
    });
});

test("active provider request remains processing", () => {
    assert.deepEqual(logStatus({ status: "succeeded", providerStatus: "processing", taskStatus: "running" }), {
        label: "处理中",
        tone: "warning",
    });
});
