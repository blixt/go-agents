import { join } from "path"
import { existsSync, readdirSync, mkdirSync, statSync } from "fs"
import { writeFile, readFile } from "fs/promises"

type ServiceState = {
  name: string
  dir: string
  proc: ReturnType<typeof Bun.spawn> | null
  restartCount: number
  lastStart: number
  backoffMs: number
  restartTimer: ReturnType<typeof setTimeout> | null
  logFile: string
  /** mtime of run.ts when service was last spawned */
  runTsMtime: number
  /** mtime of ~/.go-agents/.env when service was last spawned */
  dotEnvMtime: number
  generation: number
  pendingExit: Promise<void> | null
}

const MIN_BACKOFF = 1000
const MAX_BACKOFF = 60_000
const HEALTHY_THRESHOLD = 60_000
const SCAN_INTERVAL = 2000
const MAX_LOG_BYTES = 1_000_000
const TRUNCATE_TO_BYTES = 500_000
const DRAIN_DELAY = 2000    // ms after process exit for network cleanup
const KILL_TIMEOUT = 5000   // ms before SIGKILL escalation

const services = new Map<string, ServiceState>()
let goAgentsHome = ""
let dotEnvLoader: (() => Record<string, string>) | null = null
let scanTimer: ReturnType<typeof setInterval> | null = null

export function startSupervisor(
  home: string,
  loadDotEnv: () => Record<string, string>,
): void {
  goAgentsHome = home
  dotEnvLoader = loadDotEnv
  const servicesDir = join(home, "services")
  if (!existsSync(servicesDir)) {
    mkdirSync(servicesDir, { recursive: true })
  }
  scan()
  scanTimer = setInterval(scan, SCAN_INTERVAL)
}

export function stopSupervisor(): void {
  if (scanTimer) {
    clearInterval(scanTimer)
    scanTimer = null
  }
  for (const [, svc] of services) {
    stopService(svc)
  }
  services.clear()
}

function fileMtime(path: string): number {
  try {
    return statSync(path).mtimeMs
  } catch {
    return 0
  }
}

function dotEnvPath(): string {
  return join(goAgentsHome, ".env")
}

function scan(): void {
  const servicesDir = join(goAgentsHome, "services")
  if (!existsSync(servicesDir)) return

  let entries: ReturnType<typeof readdirSync>
  try {
    entries = readdirSync(servicesDir, { withFileTypes: true })
  } catch {
    return
  }

  const found = new Set<string>()

  for (const entry of entries) {
    if (!entry.isDirectory()) continue
    const name = entry.name
    found.add(name)
    const dir = join(servicesDir, name)
    const runFile = join(dir, "run.ts")
    const disabledFile = join(dir, ".disabled")

    if (!existsSync(runFile)) continue

    const isDisabled = existsSync(disabledFile)
    const existing = services.get(name)

    if (isDisabled) {
      if (existing) {
        stopService(existing)
        services.delete(name)
      }
      continue
    }

    if (existing) {
      // Skip services that are still draining
      if (existing.pendingExit) continue
      // Check if run.ts or .env changed since last spawn — if so, restart
      const currentRunMtime = fileMtime(runFile)
      const currentEnvMtime = fileMtime(dotEnvPath())
      if (
        currentRunMtime !== existing.runTsMtime ||
        currentEnvMtime !== existing.dotEnvMtime
      ) {
        console.error(
          `[supervisor] file change detected for service ${name}, restarting`,
        )
        stopService(existing)
        existing.backoffMs = MIN_BACKOFF
        existing.restartCount = 0
        installAndStart(existing).catch(err => console.error(`[supervisor] restart failed for ${name}:`, err))
      }
      continue
    }

    const svc: ServiceState = {
      name,
      dir,
      proc: null,
      restartCount: 0,
      lastStart: 0,
      backoffMs: MIN_BACKOFF,
      restartTimer: null,
      logFile: join(dir, "output.log"),
      runTsMtime: 0,
      dotEnvMtime: 0,
      generation: 0,
      pendingExit: null,
    }
    services.set(name, svc)
    installAndStart(svc).catch(err => console.error(`[supervisor] start failed for ${name}:`, err))
  }

  // Stop services whose directories were removed
  for (const [name, svc] of services) {
    if (!found.has(name)) {
      stopService(svc)
      services.delete(name)
    }
  }
}

async function installAndStart(svc: ServiceState): Promise<void> {
  const pkgJson = join(svc.dir, "package.json")
  const nodeModules = join(svc.dir, "node_modules")

  if (existsSync(pkgJson) && !existsSync(nodeModules)) {
    const result = Bun.spawnSync(["bun", "install"], {
      cwd: svc.dir,
      stdout: "inherit",
      stderr: "inherit",
    })
    if (result.exitCode !== 0) {
      console.error(`[supervisor] bun install failed for service ${svc.name}`)
      return
    }
  }

  await spawnService(svc)
}

async function spawnService(svc: ServiceState): Promise<void> {
  // Wait for previous process to fully drain before starting a new one
  if (svc.pendingExit) {
    await svc.pendingExit
    svc.pendingExit = null
  }

  svc.generation++
  const gen = svc.generation

  const dotEnvVars = dotEnvLoader ? dotEnvLoader() : {}
  const runFile = join(svc.dir, "run.ts")
  const envFile = dotEnvPath()

  const envKeys = Object.keys(dotEnvVars)
  if (envKeys.length > 0) {
    console.error(
      `[supervisor] loading ${envKeys.length} env var(s) for service ${svc.name}: ${envKeys.join(", ")}`,
    )
  }

  // Ensure node_modules symlinks for core/ and tools/
  const nodeModulesDir = join(svc.dir, "node_modules")
  if (!existsSync(nodeModulesDir)) {
    mkdirSync(nodeModulesDir, { recursive: true })
  }
  const coreLink = join(nodeModulesDir, "core")
  const toolsLink = join(nodeModulesDir, "tools")
  const coreSource = join(goAgentsHome, "core")
  const toolsSource = join(goAgentsHome, "tools")
  try {
    if (!existsSync(coreLink) && existsSync(coreSource)) {
      Bun.spawnSync(["ln", "-s", coreSource, coreLink])
    }
  } catch { /* ignore */ }
  try {
    if (!existsSync(toolsLink) && existsSync(toolsSource)) {
      Bun.spawnSync(["ln", "-s", toolsSource, toolsLink])
    }
  } catch { /* ignore */ }

  // Record mtimes so we can detect changes on next scan
  svc.runTsMtime = fileMtime(runFile)
  svc.dotEnvMtime = fileMtime(envFile)
  svc.lastStart = Date.now()

  // Use --env-file so Bun natively loads .env vars into the service process,
  // in addition to passing them via the env option (belt and suspenders).
  const cmd = existsSync(envFile)
    ? ["bun", "--env-file=" + envFile, runFile]
    : ["bun", runFile]

  const proc = Bun.spawn(cmd, {
    cwd: svc.dir,
    stdout: "pipe",
    stderr: "pipe",
    env: {
      ...process.env,
      ...dotEnvVars,
      GO_AGENTS_HOME: goAgentsHome,
    },
  })

  svc.proc = proc

  const startBanner = `--- service started at ${new Date().toISOString()} ---\n`
  appendLog(svc.logFile, startBanner).catch(() => {})

  // Pipe stdout and stderr to log file
  pipeToLog(svc, proc.stdout)
  pipeToLog(svc, proc.stderr)

  proc.exited.then((exitCode) => {
    // Bail if a newer generation has taken over
    if (svc.generation !== gen) return

    svc.proc = null

    // Check if service was intentionally stopped
    if (!services.has(svc.name)) return

    const uptime = Date.now() - svc.lastStart
    if (uptime >= HEALTHY_THRESHOLD) {
      svc.backoffMs = MIN_BACKOFF
      svc.restartCount = 0
    } else {
      svc.backoffMs = Math.min(svc.backoffMs * 2, MAX_BACKOFF)
    }
    svc.restartCount++

    const delay = Math.max(svc.backoffMs, DRAIN_DELAY)
    console.error(
      `[supervisor] service ${svc.name} exited (code ${exitCode}), restarting in ${delay}ms`,
    )

    svc.restartTimer = setTimeout(() => {
      svc.restartTimer = null
      if (services.has(svc.name)) {
        spawnService(svc)
      }
    }, delay)
  })
}

async function pipeToLog(
  svc: ServiceState,
  stream: ReadableStream<Uint8Array> | null,
): Promise<void> {
  if (!stream) return
  const reader = stream.getReader()
  const decoder = new TextDecoder()
  try {
    while (true) {
      const { value, done } = await reader.read()
      if (done) break
      const text = decoder.decode(value)
      if (!text) continue
      try {
        await appendLog(svc.logFile, text)
      } catch {
        // ignore log write failures
      }
    }
  } catch {
    // stream error, ignore
  } finally {
    reader.releaseLock()
  }
}

async function appendLog(logFile: string, text: string): Promise<void> {
  let existing = ""
  try {
    existing = await readFile(logFile, "utf-8")
  } catch {
    // file doesn't exist yet
  }
  let content = existing + text
  if (content.length > MAX_LOG_BYTES) {
    content = content.slice(-TRUNCATE_TO_BYTES)
  }
  await writeFile(logFile, content)
}

function stopService(svc: ServiceState): void {
  if (svc.restartTimer) {
    clearTimeout(svc.restartTimer)
    svc.restartTimer = null
  }
  const proc = svc.proc
  if (!proc) return
  svc.proc = null
  try {
    proc.kill()
  } catch {
    // already dead
  }
  const killTimer = setTimeout(() => {
    try { proc.kill(9) } catch { /* already dead */ }
  }, KILL_TIMEOUT)
  svc.pendingExit = proc.exited
    .then(() => Bun.sleep(DRAIN_DELAY))
    .finally(() => clearTimeout(killTimer))
}
