import path from "node:path";
import { mkdir } from "node:fs/promises";

const ROOT = import.meta.dir;
const SRC_DIR = path.join(ROOT, "src");
const DIST_DIR = path.join(ROOT, "dist");
const API_BASE = (process.env.GO_AGENTS_UI_API_BASE || "http://127.0.0.1:8080").trim();
const NODE_ENV = (process.env.NODE_ENV || "development").trim().toLowerCase();
const IS_PROD = NODE_ENV === "production";

type ListenConfig = { hostname: string; port: number };

function resolveListen(value: string): ListenConfig {
  const input = value.trim();
  if (!input) return { hostname: "0.0.0.0", port: 8080 };
  if (/^\d+$/.test(input)) return { hostname: "0.0.0.0", port: Number(input) };
  if (input.startsWith(":") && /^\d+$/.test(input.slice(1))) {
    return { hostname: "0.0.0.0", port: Number(input.slice(1)) };
  }
  const normalized = input.includes("://") ? input : `http://${input}`;
  try {
    const parsed = new URL(normalized);
    const port = Number(parsed.port || "8080");
    return { hostname: parsed.hostname || "0.0.0.0", port: Number.isFinite(port) ? port : 8080 };
  } catch {
    return { hostname: "0.0.0.0", port: 8080 };
  }
}

const listen = resolveListen(process.env.GO_AGENTS_UI_ADDR || ":8080");

let buildPromise: Promise<void> | null = null;

function toPublicAssetPath(rawPathname: string): string | null {
  const cleaned = rawPathname.replace(/^\/+/, "");
  if (!cleaned) return null;
  const resolved = path.resolve(DIST_DIR, cleaned);
  if (!resolved.startsWith(DIST_DIR)) return null;
  return resolved;
}

async function ensureBundle(): Promise<void> {
  if (buildPromise) {
    return buildPromise;
  }
  buildPromise = (async () => {
    await mkdir(DIST_DIR, { recursive: true });
    const result = await Bun.build({
      entrypoints: [path.join(SRC_DIR, "client.tsx")],
      outfile: path.join(DIST_DIR, "client.js"),
      target: "browser",
      format: "esm",
      splitting: false,
      sourcemap: "linked",
      minify: IS_PROD,
    });
    if (!result.success) {
      const details = result.logs.map((log) => (typeof log === "string" ? log : JSON.stringify(log))).join("\n");
      throw new Error(`bun build failed\n${details}`);
    }
  })();
  try {
    await buildPromise;
  } catch (err) {
    buildPromise = null;
    throw err;
  }
}

async function proxyAPI(req: Request): Promise<Response> {
  const source = new URL(req.url);
  const target = new URL(source.pathname + source.search, API_BASE);
  const headers = new Headers(req.headers);
  headers.delete("host");
  const method = req.method.toUpperCase();
  const init: RequestInit = {
    method,
    headers,
    body: method === "GET" || method === "HEAD" ? undefined : req.body,
    redirect: "manual",
  };
  const response = await fetch(target, init);
  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers: response.headers,
  });
}

function htmlShell(): string {
  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>go-agents Control Panel</title>
    <link rel="stylesheet" href="/styles.css" />
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/client.js"></script>
  </body>
</html>`;
}

await ensureBundle();

const server = Bun.serve({
  hostname: listen.hostname,
  port: listen.port,
  async fetch(req) {
    const url = new URL(req.url);
    if (url.pathname === "/healthz") {
      return Response.json({ ok: true, api_base: API_BASE });
    }

    if (url.pathname.startsWith("/api/")) {
      return proxyAPI(req);
    }

    if (url.pathname === "/styles.css") {
      const file = Bun.file(path.join(SRC_DIR, "styles.css"));
      if (!(await file.exists())) return new Response("styles.css not found", { status: 404 });
      return new Response(file, {
        headers: {
          "content-type": "text/css; charset=utf-8",
          "cache-control": IS_PROD ? "public, max-age=31536000, immutable" : "no-store",
        },
      });
    }

    const assetPath = toPublicAssetPath(url.pathname);
    if (assetPath) {
      const file = Bun.file(assetPath);
      if (await file.exists()) {
        return new Response(file, {
          headers: { "cache-control": IS_PROD ? "public, max-age=31536000, immutable" : "no-store" },
        });
      }
    }

    return new Response(htmlShell(), {
      headers: {
        "content-type": "text/html; charset=utf-8",
        "cache-control": "no-store",
      },
    });
  },
});

console.log(`go-agents ui server listening on http://${server.hostname}:${server.port} (api: ${API_BASE})`);
