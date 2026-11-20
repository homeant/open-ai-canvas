#!/usr/bin/env node
// Direct Streamable HTTP client for catalog diagnostics and local file uploads.
// Credentials stay in the environment and never become command-line arguments.
import { createHash } from "node:crypto";
import { open, readFile, stat } from "node:fs/promises";
import { basename } from "node:path";
import { pathToFileURL } from "node:url";

export async function createYingceMCPClient({ url, token }) {
    const endpoint = new URL(url);
    if (endpoint.username || endpoint.password || endpoint.search || endpoint.hash ||
        !(endpoint.protocol === "https:" || (endpoint.protocol === "http:" && ["localhost", "127.0.0.1", "[::1]"].includes(endpoint.hostname)))) {
        throw new Error("MCP 地址须为 HTTPS 或本机 HTTP，且不能包含认证、查询参数或片段");
    }
    if (!token) throw new Error("请通过 YINGCE_MCP_TOKEN 环境变量提供影策 API Key");
    let sequence = 0;
    let protocolVersion = "2025-06-18";
    let sessionId;
    async function rpc(method, params, notification = false) {
        const id = notification ? undefined : ++sequence;
        const response = await fetch(endpoint, {
            method: "POST", redirect: "error", signal: AbortSignal.timeout(120_000),
            headers: {
                Authorization: `Bearer ${token}`, "Content-Type": "application/json",
                Accept: "application/json, text/event-stream", "MCP-Protocol-Version": protocolVersion,
                ...(sessionId ? { "Mcp-Session-Id": sessionId } : {}),
            },
            body: JSON.stringify({ jsonrpc: "2.0", ...(id === undefined ? {} : { id }), method, params }),
        });
        if (!response.ok) {
            // Do not print response bodies: a proxy may echo Authorization.
            throw new Error(`MCP HTTP ${response.status}; 本次请求未确认，请保留原幂等键重试`);
        }
        sessionId = response.headers.get("mcp-session-id") || sessionId;
        if (notification || response.status === 202) { await response.body?.cancel(); return; }
        const raw = await response.text();
        const messages = response.headers.get("content-type")?.includes("text/event-stream")
            ? raw.split(/\r?\n\r?\n/).flatMap(event => {
                const data = event.split(/\r?\n/).filter(line => line.startsWith("data:")).map(line => line.slice(5).trimStart()).join("\n");
                return data ? [JSON.parse(data)] : [];
            }) : [JSON.parse(raw)];
        const message = messages.find(item => item.id === id);
        if (!message) throw new Error("MCP 没有返回对应请求的回执");
        if (message.error) throw new Error(`MCP 协议错误 ${message.error.code}`);
        return message.result;
    }
    const initialized = await rpc("initialize", { protocolVersion, capabilities: {}, clientInfo: { name: "yingce-direct-client", version: "1.0.0" } });
    protocolVersion = initialized.protocolVersion;
    await rpc("notifications/initialized", {}, true);
    async function call(name, args) {
        const result = await rpc("tools/call", { name, arguments: args });
        const value = result.structuredContent ?? (result.content?.[0]?.type === "text" ? JSON.parse(result.content[0].text) : result);
        if (result.isError) throw new Error(`工具 ${name} 返回错误 ${value.code ?? ""} ${value.reason ?? ""}；请检查输入和幂等键`);
        return value;
    }
    async function list() {
        const tools = [];
        let cursor;
        do {
            const page = await rpc("tools/list", cursor ? { cursor } : {});
            tools.push(...page.tools); cursor = page.nextCursor;
        } while (cursor);
        return tools;
    }
    return { call, list, server: initialized.serverInfo };
}

export async function uploadYingceFile(client, filePath, kind, idempotencyKey) {
    const handle = await open(filePath, "r");
    try {
        const before = await handle.stat();
        if (!before.isFile() || before.size <= 0 || before.size > 2 ** 31) throw new Error("请选择非空且不超过 2 GiB 的文件");
        const hash = createHash("sha256");
        // Stream from the same open descriptor, not a path that could be swapped.
        for await (const chunk of handle.createReadStream({ autoClose: false, start: 0 })) hash.update(chunk);
        const sha256 = hash.digest("hex");
        const session = await client.call("begin_media_upload", { fileName: basename(filePath), kind, size: before.size, sha256, idempotencyKey });
        if (session.status === "completed") return client.call("complete_media_upload", { uploadId: session.uploadId });
        if (session.status !== "receiving") throw new Error("上传会话已取消或过期，请使用新幂等键");
        for (let index = 0, position = 0; position < before.size; index++, position += session.chunkSize) {
            const chunk = Buffer.alloc(Math.min(session.chunkSize, before.size - position));
            let received = 0;
            while (received < chunk.length) {
                const { bytesRead } = await handle.read(chunk, received, chunk.length - received, position + received);
                if (!bytesRead) throw new Error("上传期间文件被截断");
                received += bytesRead;
            }
            const digest = createHash("sha256").update(chunk).digest("hex");
            if (session.receivedChunks?.[index] === digest) continue;
            await client.call("upload_media_chunk", { uploadId: session.uploadId, index, dataBase64: chunk.toString("base64"), sha256: digest });
        }
        const after = await handle.stat();
        const currentPath = await stat(filePath);
        if (after.size !== before.size || after.mtimeMs !== before.mtimeMs || currentPath.ino !== before.ino || currentPath.dev !== before.dev) throw new Error("上传期间文件已变更，未执行完成；请取消该会话后重新开始");
        return client.call("complete_media_upload", { uploadId: session.uploadId });
    } finally { await handle.close(); }
}

async function main() {
    const [command, ...args] = process.argv.slice(2);
    if (!command || command === "--help") {
        console.log("YINGCE_MCP_TOKEN=<从安全环境提供> [YINGCE_MCP_URL=http://127.0.0.1:3000/api/mcp] bun scripts/yingce-mcp.mjs list|schema <工具名>|call <工具名> <参数.json>|upload <文件> <image|video|audio> <幂等键>");
        return;
    }
    const client = await createYingceMCPClient({ url: process.env.YINGCE_MCP_URL || "http://127.0.0.1:3000/api/mcp", token: process.env.YINGCE_MCP_TOKEN });
    let result;
    if (command === "list") {
        const tools = await client.list(); result = { server: client.server, count: tools.length, tools: tools.map(tool => tool.name) };
    } else if (command === "schema" && args.length === 1) {
        result = (await client.list()).find(tool => tool.name === args[0]); if (!result) throw new Error("服务端没有该工具");
    } else if (command === "call" && args.length === 2) {
        result = await client.call(args[0], JSON.parse(await readFile(args[1], "utf8")));
    } else if (command === "upload" && args.length === 3) {
        result = await uploadYingceFile(client, ...args);
    } else { throw new Error("参数不完整，使用 --help 查看用法"); }
    console.log(JSON.stringify(result, null, 2));
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
    main().catch(error => { console.error(error.message); process.exitCode = 1; });
}
