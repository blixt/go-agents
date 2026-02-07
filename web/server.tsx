import index from "./index.html";

const API_BASE = (process.env.GO_AGENTS_UI_API_BASE || "http://127.0.0.1:8080").trim();

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

const server = Bun.serve({
  hostname: listen.hostname,
  port: listen.port,
  development: false,
  routes: {
    "/": index,
    "/healthz": () => Response.json({ ok: true, api_base: API_BASE }),
    "/api/*": (req) => proxyAPI(req),
  },
});

console.log(`go-agents ui server listening on http://${server.hostname}:${server.port} (api: ${API_BASE})`);
