import { expect, test } from "bun:test";

test("real arrangement hooks support immediate atomic undo, redo, insertion avoidance and all menu modes", async () => {
    const result = await new Promise((resolve, reject) => {
        const worker = new Worker(new URL("./helpers/canvas-arrangement-flow.worker.tsx", import.meta.url).href, { type: "module" });
        const timeout = setTimeout(() => {
            worker.terminate();
            reject(new Error("arrangement flow timed out"));
        }, 12000);
        worker.onmessage = (event) => {
            clearTimeout(timeout);
            worker.terminate();
            if (event.data.ok) resolve(event.data.result);
            else reject(new Error(event.data.error));
        };
        worker.onerror = (event) => {
            clearTimeout(timeout);
            worker.terminate();
            reject(new Error(event.message));
        };
        worker.postMessage(null);
    });
    expect(result).toEqual({ undo: true, redo: true, insertion: true, menuModes: 8, darkTheme: true });
}, 15000);
