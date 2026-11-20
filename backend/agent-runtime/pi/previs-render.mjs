import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function chromePath() {
    const configured = process.env.CANVAS_PREVIS_CHROME_PATH?.trim();
    if (configured) return configured;
    if (process.platform === "darwin") return "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
    return "/usr/bin/chromium-browser";
}

async function readInput() {
    let input = "";
    for await (const chunk of process.stdin) {
        input += chunk;
        if (input.length > (48 << 20)) throw new Error("预演场景输入过大");
    }
    return JSON.parse(input);
}

async function waitForPort(profile, child) {
    for (let attempt = 0; attempt < 200; attempt++) {
        if (child.startError) throw child.startError;
        if (child.exitCode !== null) throw new Error("无头浏览器启动失败");
        try {
            const lines = (await readFile(join(profile, "DevToolsActivePort"), "utf8")).trim().split("\n");
            const port = Number(lines[0]);
            if (port > 0 && port <= 65535) return port;
        } catch { /* browser has not opened its debugging port yet */ }
        await sleep(50);
    }
    throw new Error("无头浏览器启动超时");
}

function connectCDP(url) {
    return new Promise((resolve, reject) => {
        const socket = new WebSocket(url);
        let sequence = 0;
        const pending = new Map();
        socket.onopen = () => resolve({
            async send(method, params = {}) {
                const id = ++sequence;
                return new Promise((done, fail) => {
                    pending.set(id, { done, fail });
                    socket.send(JSON.stringify({ id, method, params }));
                });
            },
            close() { socket.close(); },
        });
        socket.onmessage = (event) => {
            const message = JSON.parse(String(event.data));
            const waiter = pending.get(message.id);
            if (!waiter) return;
            pending.delete(message.id);
            if (message.error) waiter.fail(new Error(message.error.message));
            else waiter.done(message.result);
        };
        socket.onerror = () => reject(new Error("无法连接浏览器调试接口"));
        socket.onclose = () => {
            for (const waiter of pending.values()) waiter.fail(new Error("浏览器已关闭"));
            pending.clear();
        };
    });
}

async function render() {
    const input = await readInput();
    const pageURL = process.env.CANVAS_PREVIS_RENDER_URL?.trim() || "http://web:3000/previs-render.html";
    const parsed = new URL(pageURL);
    if (!/^https?:$/.test(parsed.protocol) || parsed.username || parsed.password || parsed.search || parsed.hash || !parsed.pathname.endsWith("/previs-render.html")) throw new Error("预演渲染页面地址无效");
    const profile = await mkdtemp(join(tmpdir(), "yingce-previs-"));
    const chrome = spawn(chromePath(), ["--headless=new", "--no-sandbox", "--disable-gpu-sandbox", "--disable-dev-shm-usage", "--enable-webgl", "--enable-unsafe-swiftshader", "--use-gl=angle", "--use-angle=swiftshader", "--remote-debugging-port=0", `--user-data-dir=${profile}`, "--window-size=900,1200", "--no-first-run", "about:blank"], { stdio: "ignore" });
    chrome.on("error", (error) => { chrome.startError = error; });
    let cdp;
    try {
        const port = await waitForPort(profile, chrome);
        const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
        const target = targets.find((item) => item.type === "page" && item.webSocketDebuggerUrl);
        if (!target) throw new Error("浏览器未创建页面");
        cdp = await connectCDP(target.webSocketDebuggerUrl);
        await cdp.send("Page.enable");
        await cdp.send("Runtime.enable");
        await cdp.send("Page.navigate", { url: pageURL });
        let ready = false;
        for (let attempt = 0; attempt < 300; attempt++) {
            const response = await cdp.send("Runtime.evaluate", { expression: 'typeof window.yingcePrevisRender === "function"', returnByValue: true });
            if (response.result?.value === true) { ready = true; break; }
            await sleep(50);
        }
        if (!ready) throw new Error("预演渲染页面加载超时");
        const expression = `window.yingcePrevisRender(${JSON.stringify(input)})`;
        const result = await cdp.send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
        if (result.exceptionDetails) throw new Error(result.exceptionDetails.text || result.result?.description || "浏览器渲染失败");
        if (input.kind === "frames" ? !Array.isArray(result.result?.value?.frames) : !result.result?.value?.dataBase64) throw new Error("浏览器未返回预演产物");
        process.stdout.write(JSON.stringify(result.result.value));
    } finally {
        cdp?.close();
        if (chrome.exitCode === null) {
            const closed = new Promise((resolve) => chrome.once("close", resolve));
            chrome.kill("SIGTERM");
            await Promise.race([closed, sleep(3000)]);
            if (chrome.exitCode === null) chrome.kill("SIGKILL");
        }
        await rm(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
    }
}

render().catch((error) => {
    process.stderr.write(`${error instanceof Error ? error.message : "预演渲染失败"}\n`);
    process.exitCode = 1;
});
