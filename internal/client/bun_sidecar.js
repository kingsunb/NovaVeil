const server = Bun.serve({
  hostname: "127.0.0.1",
  port: 0,
  async fetch(req) {
    if (req.method !== "POST" || new URL(req.url).pathname !== "/forward") {
      return new Response("not found", { status: 404 })
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
    const body = input.body ? new Uint8Array(Buffer.from(input.body, "base64")) : undefined
    try {
      const upstream = await fetch(input.url, {
        method: input.method,
        headers,
        body,
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
