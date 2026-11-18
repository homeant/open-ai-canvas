import { createServer } from "node:http";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

const script = fileURLToPath(new URL("./previs-render.mjs", import.meta.url));
const maxInput = 48 << 20;
let active = 0;

const server = createServer(async (request, response) => {
    if (request.method === "GET" && request.url === "/healthz") {
        response.writeHead(200).end("ok");
        return;
    }
    if (request.method !== "POST" || request.url !== "/render") {
        response.writeHead(404).end();
        return;
    }
    if (active >= 2) {
        response.writeHead(429).end("renderer busy");
        return;
    }
    active++;
    try {
        const chunks = [];
        let size = 0;
        for await (const chunk of request) {
            size += chunk.length;
            if (size > maxInput) throw new Error("scene too large");
            chunks.push(chunk);
        }
        const input = Buffer.concat(chunks);
        const child = spawn(process.execPath, [script], { stdio: ["pipe", "pipe", "pipe"], env: process.env });
        const stdout = [];
        const stderr = [];
        let outputSize = 0;
        const timer = setTimeout(() => child.kill("SIGKILL"), 90_000);
        child.stdin.on("error", () => {});
        child.stdin.end(input);
        response.on("close", () => { if (!response.writableFinished) child.kill("SIGKILL"); });
        child.stdout.on("data", (chunk) => {
            outputSize += chunk.length;
            if (outputSize > (48 << 20)) child.kill("SIGKILL");
            else stdout.push(chunk);
        });
        child.stderr.on("data", (chunk) => { if (stderr.length < 8) stderr.push(chunk); });
        let code;
        try {
            code = await new Promise((resolve, reject) => {
                child.on("error", reject);
                child.on("close", resolve);
            });
        } finally {
            clearTimeout(timer);
        }
        if (code !== 0) throw new Error(Buffer.concat(stderr).toString("utf8").slice(0, 400) || "render failed");
        response.writeHead(200, { "Content-Type": "application/json", "Cache-Control": "no-store" }).end(Buffer.concat(stdout));
    } catch (error) {
        response.writeHead(502, { "Content-Type": "text/plain", "Cache-Control": "no-store" }).end(error instanceof Error ? error.message.slice(0, 400) : "render failed");
    } finally {
        active--;
    }
});

server.listen(8090, "0.0.0.0");
