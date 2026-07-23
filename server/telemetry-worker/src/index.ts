export interface Env {
  DB: D1Database;
}

interface LatencyStats {
  count?: number;
  min?: number;
  max?: number;
  mean?: number;
  p50?: number;
  p95?: number;
  p99?: number;
}

interface Payload {
  install_id: string;
  version?: string;
  window_start?: string;
  window_end?: string;
  events?: {
    captures?: number;
    searches?: number;
    gets?: number;
    errors?: number;
    models?: Record<string, number>;
    clients?: Record<string, number>;
  };
  latency?: Record<string, LatencyStats>;
}

const cors = {
  "Access-Control-Allow-Origin": "*",
  "Access-Control-Allow-Methods": "POST, OPTIONS",
  "Access-Control-Allow-Headers": "Content-Type, User-Agent",
};

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    if (request.method === "OPTIONS") {
      return new Response(null, { status: 204, headers: cors });
    }
    if (request.method !== "POST") {
      return new Response("POST only", { status: 405, headers: cors });
    }

    const url = new URL(request.url);
    if (url.pathname !== "/v1/ping") {
      return new Response("not found", { status: 404, headers: cors });
    }

    let body: Payload;
    try {
      body = await request.json<Payload>();
    } catch {
      return new Response("invalid json", { status: 400, headers: cors });
    }

    if (!body.install_id || typeof body.install_id !== "string" || body.install_id.length > 128) {
      return new Response("missing or invalid install_id", { status: 400, headers: cors });
    }

    // Basic size guard — an honest client sends 1-5 KB per ping.
    const bytes = JSON.stringify(body).length;
    if (bytes > 32 * 1024) {
      return new Response("payload too large", { status: 413, headers: cors });
    }

    const ev = body.events ?? {};
    const cap = body.latency?.capture ?? {};
    const search = body.latency?.search ?? {};
    const get = body.latency?.get ?? {};
    const cf = (request as unknown as { cf?: { country?: string } }).cf;
    const country = cf?.country ?? null;

    try {
      await env.DB.prepare(
        `INSERT INTO pings (
          install_id, version, window_start, window_end,
          captures, searches, gets, errors,
          models, clients,
          latency_capture_p50, latency_capture_p95, latency_capture_p99, latency_capture_count,
          latency_search_p50, latency_search_p95, latency_search_p99, latency_search_count,
          latency_get_p50, latency_get_p95, latency_get_p99, latency_get_count,
          ip_country
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      )
        .bind(
          body.install_id, body.version ?? "",
          body.window_start ?? null, body.window_end ?? null,
          ev.captures ?? 0, ev.searches ?? 0, ev.gets ?? 0, ev.errors ?? 0,
          JSON.stringify(ev.models ?? {}), JSON.stringify(ev.clients ?? {}),
          cap.p50 ?? null, cap.p95 ?? null, cap.p99 ?? null, cap.count ?? null,
          search.p50 ?? null, search.p95 ?? null, search.p99 ?? null, search.count ?? null,
          get.p50 ?? null, get.p95 ?? null, get.p99 ?? null, get.count ?? null,
          country,
        )
        .run();
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      return new Response(`insert failed: ${msg}`, { status: 500, headers: cors });
    }

    return new Response("ok", { status: 200, headers: cors });
  },
};
