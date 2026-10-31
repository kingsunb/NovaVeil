// 本地鉴权 token: 启动时由 Go 注入, 每请求校验。
const sidecarToken = process.env.NOVEIL_SIDECAR_TOKEN || ""

const server = Bun.serve({
  hostname: "127.0.0.1",
  port: 0,
  // 默认 10 秒空闲会掐断思考中的流
  idleTimeout: 0,
  async fetch(req) {
    if (req.method !== "POST" || new URL(req.url).pathname !== "/forward") {
      return new Response("not found", { status: 404 })
    }
    // 鉴权头缺失或不匹配一律拒绝, 头不参与转发。
    if (!sidecarToken || req.headers.get("x-noveil-sidecar-auth") !== sidecarToken) {
      return Response.json({ error: "unauthorized" }, { status: 403 })
    }
    let input
    try {
      input = await req.json()
    } catch (e) {
      return Response.json({ error: "invalid forward payload" }, { status: 400 })
    }
    const headers = new Headers()
    for (const [name, value] of input.headers || []) {
      headers.append(name, value)
    }
    const body = input.body ? Buffer.from(input.body, "base64") : undefined
    try {
      const upstream = await fetch(input.url, {
        method: input.method,
        headers,
        body,
        signal: req.signal,
        redirect: "manual",
        proxy: input.proxy || undefined,
      })
      const outHeaders = new Headers()
      upstream.headers.forEach((value, key) => {
        if (key === "content-encoding" || key === "content-length") return
        outHeaders.set(key, value)
      })
      return new Response(upstream.body ?? null, {
        status: upstream.status,
        statusText: upstream.statusText,
        headers: outHeaders,
      })
    } catch (e) {
      const headers = new Headers({ "x-noveil-sidecar-error": "1" })
      return Response.json({ error: String(e?.message ?? e) }, { status: 502, headers })
    }
  },
})

console.log(JSON.stringify({ port: server.port }))

const parentPID = Number(process.env.NOVEIL_PARENT_PID || "0")
if (parentPID > 0) {
  setInterval(() => {
    try {
      process.kill(parentPID, 0)
    } catch {
      server.stop(true)
      process.exit(0)
    }
  }, 5000)
}
