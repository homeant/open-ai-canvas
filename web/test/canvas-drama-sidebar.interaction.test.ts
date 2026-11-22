import { expect, test } from "bun:test";
import { resolve } from "node:path";

test("精修画布设定可按角色、场景和道具切换", () => {
    const result = Bun.spawnSync([process.execPath, "test", "./test/fixtures/canvas-drama-sidebar.harness.tsx"], { cwd: resolve(import.meta.dir, ".."), timeout: 20_000 });
    if (result.exitCode !== 0) throw new Error(new TextDecoder().decode(result.stdout) + new TextDecoder().decode(result.stderr));
    expect(result.exitCode).toBe(0);
});
