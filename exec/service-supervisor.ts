import { join } from "path"
import {
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  symlinkSync,
} from "fs"
import { readFile, writeFile } from "fs/promises"

type ServiceLifecycleState =
  | "starting"
  | "running"
  | "restarting"
  | "stopped"
  | "backoff"
  | "blocked_config"
  | "blocked_build"
  | "failed"
  | "degraded"

type RestartPolicy = "always" | "on-failure" | "never"

type RawServiceManifest = {
  version?: number
  service_id?: string
  singleton?: boolean
  required_env?: string[]
  environment?: Record<string, unknown>
  restart?: {
    policy?: RestartPolicy
    min_backoff_ms?: number
    max_backoff_ms?: number
  }
  health?: {
    heartbeat_file?: string
    heartbeat_ttl_seconds?: number
    restart_on_stale?: boolean
  }
}

type ServiceManifest = {
  path: string
  serviceID: string
  singleton: boolean
  requiredEnv: string[]
  environment: Record<string, string>
  restartPolicy: RestartPolicy
  restartMinBackoffMs: number
  restartMaxBackoffMs: number
  heartbeatFile: string
  heartbeatTTLSeconds: number
  restartOnStaleHeartbeat: boolean
}

type ServiceState = {
  name: string
  dir: string
  proc: ReturnType<typeof Bun.spawn> | null
  restartCount: number
  lastStart: number
  backoffMs: number
  restartTimer: ReturnType<typeof setTimeout> | null
  nextRestartAt: number
  logFile: string
  /** mtime of run.ts when service was last accepted */
  runTsMtime: number
  /** mtime of services/<name>/service.json when service was last accepted */
  manifestMtime: number
  /** mtime of package.json when service was last accepted */
  pkgJsonMtime: number
  generation: number
  pendingExit: Promise<void> | null
  busy: boolean
  state: ServiceLifecycleState
  stateReason: string
  lastError: string
  lastExitCode: number | null
  missingEnv: string[]
  pendingConfigError: string
  manifest: ServiceManifest | null
}

type PreflightSuccess = {
  ok: true
  manifest: ServiceManifest
}

type PreflightFailure = {
  ok: false
  retryable: boolean
  state: ServiceLifecycleState
  reason: string
  detail: string
  missingEnv?: string[]
}

type PreflightResult = PreflightSuccess | PreflightFailure

const MIN_BACKOFF = 1000
const MAX_BACKOFF = 60_000
const HEALTHY_THRESHOLD = 60_000
const SCAN_INTERVAL = 2000
const MAX_LOG_BYTES = 1_000_000
const TRUNCATE_TO_BYTES = 500_000
const DRAIN_DELAY = 2000 // ms after process exit for network cleanup
const KILL_TIMEOUT = 5000 // ms before SIGKILL escalation
const MANIFEST_FILE = "service.json"
const STATE_FILE = ".service-supervisor-state.json"
const PREFLIGHT_OUT_DIR = ".go-agents-preflight"

const services = new Map<string, ServiceState>()
let goAgentsHome = ""
let scanTimer: ReturnType<typeof setInterval> | null = null
let stateWriteInFlight: Promise<void> | null = null
let stateWriteQueued = false

export function startSupervisor(home: string): void {
  goAgentsHome = home
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
    stopService(svc, { suppressRestart: true })
  }
  services.clear()
  requestStateWrite()
}

function requestStateWrite() {
  if (stateWriteInFlight) {
    stateWriteQueued = true
    return
  }
  stateWriteInFlight = writeSupervisorState()
    .catch(() => {
      // ignore state write errors
    })
    .finally(() => {
      stateWriteInFlight = null
      if (stateWriteQueued) {
        stateWriteQueued = false
        requestStateWrite()
      }
    })
}

function stateFilePath(): string {
  return join(goAgentsHome, "services", STATE_FILE)
}

async function writeSupervisorState(): Promise<void> {
  const snapshots = Array.from(services.values())
    .map(svc => {
      const pid = svc.proc ? (svc.proc as any).pid : null
      return {
        name: svc.name,
        state: svc.state,
        reason: svc.stateReason,
        last_error: svc.lastError || undefined,
        last_exit_code: svc.lastExitCode,
        restart_count: svc.restartCount,
        backoff_ms: svc.backoffMs,
        next_restart_at: svc.nextRestartAt > 0 ? new Date(svc.nextRestartAt).toISOString() : null,
        pid,
        started_at: svc.lastStart > 0 ? new Date(svc.lastStart).toISOString() : null,
        service_id: svc.manifest?.serviceID || svc.name,
        run_ts_mtime: svc.runTsMtime,
        manifest_mtime: svc.manifestMtime,
        package_mtime: svc.pkgJsonMtime,
        singleton: svc.manifest?.singleton ?? true,
        required_env: svc.manifest?.requiredEnv || [],
        environment_keys: Object.keys(svc.manifest?.environment || {}).sort(),
        missing_env: svc.missingEnv,
        pending_config_error: svc.pendingConfigError || undefined,
        manifest_path: join(svc.dir, MANIFEST_FILE),
        heartbeat_file: svc.manifest ? join(svc.dir, svc.manifest.heartbeatFile) : null,
        heartbeat_ttl_seconds: svc.manifest?.heartbeatTTLSeconds || 0,
      }
    })
    .sort((a, b) => a.name.localeCompare(b.name))

  const payload = JSON.stringify({
    updated_at: new Date().toISOString(),
    services: snapshots,
  }, null, 2)

  const path = stateFilePath()
  await writeFile(path, payload)
}

function setServiceState(
  svc: ServiceState,
  state: ServiceLifecycleState,
  reason: string,
  lastError?: string,
): void {
  svc.state = state
  svc.stateReason = reason
  if (lastError !== undefined) {
    svc.lastError = lastError
  }
  requestStateWrite()
}

function fileMtime(path: string): number {
  try {
    return statSync(path).mtimeMs
  } catch {
    return 0
  }
}

function decodeOutput(data: unknown): string {
  if (typeof data === "string") return data
  if (data instanceof Uint8Array) return new TextDecoder().decode(data)
  if (data && typeof data === "object" && "byteLength" in (data as Record<string, unknown>)) {
    try {
      return new TextDecoder().decode(data as ArrayBuffer)
    } catch {
      return ""
    }
  }
  return ""
}

function readManifest(svc: ServiceState): { manifest: ServiceManifest | null; error?: string } {
  const path = join(svc.dir, MANIFEST_FILE)
  if (!existsSync(path)) {
    return {
      manifest: null,
      error: `missing ${MANIFEST_FILE}; create services/${svc.name}/${MANIFEST_FILE}`,
    }
  }

  let raw: RawServiceManifest
  try {
    raw = JSON.parse(readFileSync(path, "utf-8")) as RawServiceManifest
  } catch (err) {
    return {
      manifest: null,
      error: `invalid ${MANIFEST_FILE}: ${err}`,
    }
  }

  if (!raw || typeof raw !== "object") {
    return { manifest: null, error: `${MANIFEST_FILE} must be a JSON object` }
  }

  const serviceID = typeof raw.service_id === "string" ? raw.service_id.trim() : ""
  if (serviceID === "") {
    return { manifest: null, error: `${MANIFEST_FILE}: service_id is required` }
  }
  if (!isValidServiceID(serviceID)) {
    return {
      manifest: null,
      error: `${MANIFEST_FILE}: service_id must match ^[a-z](?:[a-z0-9-]{0,62}[a-z0-9])?$`,
    }
  }

  const requiredEnv = Array.isArray(raw.required_env)
    ? raw.required_env
      .map(v => (typeof v === "string" ? v.trim() : ""))
      .filter(Boolean)
    : []
  const environment = parseEnvDictionary(raw.environment)

  const policy = raw.restart?.policy || "always"
  if (policy !== "always" && policy !== "on-failure" && policy !== "never") {
    return { manifest: null, error: `${MANIFEST_FILE}: restart.policy must be always, on-failure, or never` }
  }

  const minBackoff = Math.max(
    MIN_BACKOFF,
    parseNumber(raw.restart?.min_backoff_ms, MIN_BACKOFF),
  )
  const maxBackoff = Math.max(
    minBackoff,
    parseNumber(raw.restart?.max_backoff_ms, MAX_BACKOFF),
  )

  const heartbeatFileRaw = typeof raw.health?.heartbeat_file === "string"
    ? raw.health.heartbeat_file.trim()
    : ""
  const heartbeatFile = heartbeatFileRaw || ".heartbeat"
  const heartbeatTTLSeconds = Math.max(0, parseNumber(raw.health?.heartbeat_ttl_seconds, 0))

  const manifest: ServiceManifest = {
    path,
    serviceID,
    singleton: raw.singleton !== false,
    requiredEnv,
    environment,
    restartPolicy: policy,
    restartMinBackoffMs: minBackoff,
    restartMaxBackoffMs: maxBackoff,
    heartbeatFile,
    heartbeatTTLSeconds,
    restartOnStaleHeartbeat: raw.health?.restart_on_stale !== false,
  }
  return { manifest }
}

function isValidServiceID(value: string): boolean {
  return /^[a-z](?:[a-z0-9-]{0,62}[a-z0-9])?$/.test(value.trim())
}

function parseEnvDictionary(raw: Record<string, unknown> | undefined): Record<string, string> {
  const out: Record<string, string> = {}
  if (!raw || typeof raw !== "object") return out
  for (const [keyRaw, value] of Object.entries(raw)) {
    const key = keyRaw.trim()
    if (!key || !/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) continue
    if (typeof value !== "string") continue
    out[key] = value
  }
  return out
}

function parseNumber(value: unknown, fallback: number): number {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value
  }
  if (typeof value === "string") {
    const parsed = Number(value)
    if (Number.isFinite(parsed)) return parsed
  }
  return fallback
}

function ensureCoreAndToolLinks(svc: ServiceState) {
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
      symlinkSync(coreSource, coreLink, "dir")
    }
  } catch {
    // ignore
  }
  try {
    if (!existsSync(toolsLink) && existsSync(toolsSource)) {
      symlinkSync(toolsSource, toolsLink, "dir")
    }
  } catch {
    // ignore
  }
}

function checkHeartbeatStale(svc: ServiceState): { stale: boolean; ageMs: number } {
  if (!svc.manifest || svc.manifest.heartbeatTTLSeconds <= 0) {
    return { stale: false, ageMs: 0 }
  }
  const ttlMs = svc.manifest.heartbeatTTLSeconds * 1000
  const path = join(svc.dir, svc.manifest.heartbeatFile)
  const mtime = fileMtime(path)
  const now = Date.now()
  const ageMs = mtime > 0 ? now - mtime : now - svc.lastStart
  return { stale: ageMs > ttlMs, ageMs }
}

function scheduleServiceAction(svc: ServiceState, fn: () => Promise<void>): void {
  if (svc.busy) return
  svc.busy = true
  fn()
    .catch(err => {
      const msg = String(err)
      setServiceState(svc, "failed", "service_action_failed", msg)
      console.error(`[supervisor] service ${svc.name} action failed:`, err)
    })
    .finally(() => {
      svc.busy = false
      requestStateWrite()
    })
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
    const manifestFile = join(dir, MANIFEST_FILE)
    const packageJSON = join(dir, "package.json")
    const disabledFile = join(dir, ".disabled")

    if (!existsSync(runFile)) continue

    const isDisabled = existsSync(disabledFile)
    const existing = services.get(name)

    if (isDisabled) {
      if (existing) {
        stopService(existing, { suppressRestart: true })
        services.delete(name)
        requestStateWrite()
      }
      continue
    }

    if (existing) {
      if (existing.pendingExit || existing.busy) continue

      const currentRunMtime = fileMtime(runFile)
      const currentManifestMtime = fileMtime(manifestFile)
      const currentPkgMtime = fileMtime(packageJSON)

      const filesChanged =
        currentRunMtime !== existing.runTsMtime ||
        currentManifestMtime !== existing.manifestMtime ||
        currentPkgMtime !== existing.pkgJsonMtime

      if (existing.proc) {
        if (filesChanged) {
          scheduleServiceAction(existing, async () => {
            const preflight = await preflightService(existing)
            // Mark the current file state as checked so a bad config doesn't re-trigger forever.
            existing.runTsMtime = currentRunMtime
            existing.manifestMtime = currentManifestMtime
            existing.pkgJsonMtime = currentPkgMtime

            if (!preflight.ok) {
              existing.pendingConfigError = preflight.detail
              existing.lastError = preflight.detail
              existing.stateReason = "running_with_rejected_config"
              console.error(
                `[supervisor] rejected config change for ${existing.name}; keeping current process alive: ${preflight.detail}`,
              )
              requestStateWrite()
              return
            }

            existing.pendingConfigError = ""
            await restartService(existing, "file_change", preflight)
          })
          continue
        }

        const heartbeat = checkHeartbeatStale(existing)
        if (heartbeat.stale) {
          if (existing.manifest?.restartOnStaleHeartbeat) {
            scheduleServiceAction(existing, async () => {
              setServiceState(
                existing,
                "degraded",
                "stale_heartbeat",
                `stale heartbeat (${Math.floor(heartbeat.ageMs / 1000)}s)`,
              )
              await restartService(existing, "stale_heartbeat")
            })
          } else {
            setServiceState(
              existing,
              "degraded",
              "stale_heartbeat",
              `stale heartbeat (${Math.floor(heartbeat.ageMs / 1000)}s)`,
            )
          }
        }
        continue
      }

      if (existing.restartTimer) continue

      const shouldRetryBlocked =
        existing.state === "blocked_config" || existing.state === "blocked_build"

      if (filesChanged || shouldRetryBlocked || existing.state === "failed") {
        scheduleServiceAction(existing, async () => {
          await installAndStart(existing, "reconcile")
        })
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
      nextRestartAt: 0,
      logFile: join(dir, "output.log"),
      runTsMtime: 0,
      manifestMtime: 0,
      pkgJsonMtime: 0,
      generation: 0,
      pendingExit: null,
      busy: false,
      state: "starting",
      stateReason: "discovered",
      lastError: "",
      lastExitCode: null,
      missingEnv: [],
      pendingConfigError: "",
      manifest: null,
    }
    services.set(name, svc)
    scheduleServiceAction(svc, async () => {
      await installAndStart(svc, "discovered")
    })
  }

  // Stop services whose directories were removed
  for (const [name, svc] of services) {
    if (!found.has(name)) {
      stopService(svc, { suppressRestart: true })
      services.delete(name)
    }
  }

  requestStateWrite()
}

function restartPolicy(svc: ServiceState): RestartPolicy {
  return svc.manifest?.restartPolicy || "always"
}

function minBackoff(svc: ServiceState): number {
  return svc.manifest?.restartMinBackoffMs || MIN_BACKOFF
}

function maxBackoff(svc: ServiceState): number {
  return svc.manifest?.restartMaxBackoffMs || MAX_BACKOFF
}

function scheduleRestart(svc: ServiceState, reason: string): void {
  if (svc.restartTimer || !services.has(svc.name)) return
  const delay = Math.max(svc.backoffMs, DRAIN_DELAY)
  svc.nextRestartAt = Date.now() + delay
  setServiceState(
    svc,
    "backoff",
    reason,
    svc.lastError,
  )

  console.error(
    `[supervisor] service ${svc.name} restarting in ${delay}ms (${reason})`,
  )

  svc.restartTimer = setTimeout(() => {
    svc.restartTimer = null
    svc.nextRestartAt = 0
    if (!services.has(svc.name)) return
    scheduleServiceAction(svc, async () => {
      await installAndStart(svc, "scheduled_restart")
    })
  }, delay)
}

async function installAndStart(
  svc: ServiceState,
  reason: string,
  prepared?: PreflightSuccess,
): Promise<void> {
  if (svc.pendingExit) {
    await svc.pendingExit
    svc.pendingExit = null
  }

  if (svc.proc) {
    return
  }

  setServiceState(svc, "starting", reason)

  const preflight = prepared || await preflightService(svc)
  if (!preflight.ok) {
    svc.lastError = preflight.detail
    svc.missingEnv = preflight.missingEnv || []
    svc.manifest = null

    if (preflight.retryable) {
      setServiceState(svc, "failed", preflight.reason, preflight.detail)
      svc.restartCount++
      svc.backoffMs = Math.min(Math.max(svc.backoffMs * 2, minBackoff(svc)), maxBackoff(svc))
      scheduleRestart(svc, preflight.reason)
      return
    }

    setServiceState(svc, preflight.state, preflight.reason, preflight.detail)
    return
  }

  svc.manifest = preflight.manifest
  svc.backoffMs = Math.max(svc.backoffMs, minBackoff(svc))
  svc.missingEnv = []
  svc.pendingConfigError = ""

  await spawnService(svc, preflight)
}

async function preflightService(svc: ServiceState): Promise<PreflightResult> {
  const manifestResult = readManifest(svc)
  if (!manifestResult.manifest) {
    return {
      ok: false,
      retryable: false,
      state: "blocked_config",
      reason: "manifest_invalid",
      detail: manifestResult.error || `invalid ${MANIFEST_FILE}`,
    }
  }
  const manifest = manifestResult.manifest

  ensureCoreAndToolLinks(svc)

  const pkgJson = join(svc.dir, "package.json")
  const nodeModules = join(svc.dir, "node_modules")
  const currentPkgMtime = fileMtime(pkgJson)
  const shouldInstall = existsSync(pkgJson) && (!existsSync(nodeModules) || currentPkgMtime !== svc.pkgJsonMtime)
  if (shouldInstall) {
    const result = Bun.spawnSync(["bun", "install"], {
      cwd: svc.dir,
      stdout: "pipe",
      stderr: "pipe",
    })
    if (result.exitCode !== 0) {
      const stderr = decodeOutput(result.stderr)
      const stdout = decodeOutput(result.stdout)
      return {
        ok: false,
        retryable: true,
        state: "failed",
        reason: "dependency_install_failed",
        detail: (stderr || stdout || "bun install failed").trim(),
      }
    }
  }

  const mergedEnv = { ...process.env, ...manifest.environment }
  const missingEnv = manifest.requiredEnv.filter(key => {
    const value = mergedEnv[key]
    if (typeof value !== "string") return true
    return value.trim() === ""
  })
  if (missingEnv.length > 0) {
    return {
      ok: false,
      retryable: false,
      state: "blocked_config",
      reason: "missing_required_env",
      detail: `missing required env vars: ${missingEnv.join(", ")}`,
      missingEnv,
    }
  }

  const buildOutDir = join(svc.dir, PREFLIGHT_OUT_DIR)
  rmSync(buildOutDir, { recursive: true, force: true })
  const build = Bun.spawnSync([
    "bun",
    "build",
    "run.ts",
    "--target",
    "bun",
    "--outdir",
    buildOutDir,
  ], {
    cwd: svc.dir,
    stdout: "pipe",
    stderr: "pipe",
  })
  rmSync(buildOutDir, { recursive: true, force: true })

  if (build.exitCode !== 0) {
    const stderr = decodeOutput(build.stderr)
    const stdout = decodeOutput(build.stdout)
    return {
      ok: false,
      retryable: false,
      state: "blocked_build",
      reason: "preflight_build_failed",
      detail: (stderr || stdout || "bun build run.ts failed").trim(),
    }
  }

  return {
    ok: true,
    manifest,
  }
}

async function restartService(
  svc: ServiceState,
  reason: string,
  prepared?: PreflightSuccess,
): Promise<void> {
  setServiceState(svc, "restarting", reason)
  stopService(svc, { suppressRestart: true })
  svc.restartCount = 0
  svc.backoffMs = minBackoff(svc)
  svc.nextRestartAt = 0
  await installAndStart(svc, reason, prepared)
}

async function spawnService(svc: ServiceState, prepared: PreflightSuccess): Promise<void> {
  // Wait for previous process to fully drain before starting a new one.
  if (svc.pendingExit) {
    await svc.pendingExit
    svc.pendingExit = null
  }

  if (svc.proc) {
    // Another concurrent start beat us.
    return
  }

  svc.generation++
  const gen = svc.generation

  const runFile = join(svc.dir, "run.ts")
  const pkgJson = join(svc.dir, "package.json")

  const envKeys = Object.keys(prepared.manifest.environment)
  if (envKeys.length > 0) {
    console.error(
      `[supervisor] loading ${envKeys.length} configured env var(s) for service ${svc.name}: ${envKeys.join(", ")}`,
    )
  }

  ensureCoreAndToolLinks(svc)

  // Record mtimes so we can detect new changes on a later scan.
  svc.runTsMtime = fileMtime(runFile)
  svc.manifestMtime = fileMtime(prepared.manifest.path)
  svc.pkgJsonMtime = fileMtime(pkgJson)
  svc.lastStart = Date.now()
  svc.lastError = ""
  svc.lastExitCode = null

  const cmd = ["bun", runFile]

  const proc = Bun.spawn(cmd, {
    cwd: svc.dir,
    stdout: "pipe",
    stderr: "pipe",
    env: {
      ...process.env,
      ...prepared.manifest.environment,
      GO_AGENTS_HOME: goAgentsHome,
      GO_AGENTS_SERVICE_ID: prepared.manifest.serviceID,
    },
  })

  svc.proc = proc
  setServiceState(svc, "running", "healthy")

  const startBanner = `--- service started at ${new Date().toISOString()} ---\n`
  appendLog(svc.logFile, startBanner).catch(() => {})

  // Pipe stdout and stderr to log file.
  pipeToLog(svc, proc.stdout)
  pipeToLog(svc, proc.stderr)

  proc.exited.then((exitCode) => {
    // If generation changed, this process is stale and should not mutate restart state.
    if (svc.generation !== gen) return

    svc.proc = null

    // Check if service was intentionally stopped/removed.
    if (!services.has(svc.name)) return

    const numericExit = typeof exitCode === "number" ? exitCode : -1
    svc.lastExitCode = numericExit

    const uptime = Date.now() - svc.lastStart
    if (uptime >= HEALTHY_THRESHOLD) {
      svc.backoffMs = minBackoff(svc)
      svc.restartCount = 0
    } else {
      svc.backoffMs = Math.min(
        Math.max(svc.backoffMs * 2, minBackoff(svc)),
        maxBackoff(svc),
      )
    }

    const policy = restartPolicy(svc)
    if (policy === "never") {
      setServiceState(svc, "stopped", `exit_${numericExit}`)
      return
    }
    if (policy === "on-failure" && numericExit === 0) {
      setServiceState(svc, "stopped", "clean_exit")
      return
    }

    svc.restartCount++
    setServiceState(svc, "failed", `exit_${numericExit}`, `process exited with code ${numericExit}`)
    scheduleRestart(svc, `exit_${numericExit}`)
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

function stopService(svc: ServiceState, opts?: { suppressRestart?: boolean }): void {
  if (svc.restartTimer) {
    clearTimeout(svc.restartTimer)
    svc.restartTimer = null
  }
  svc.nextRestartAt = 0

  const proc = svc.proc
  if (!proc) return

  // Bump generation for intentional stops so the old process exit callback
  // cannot schedule an extra restart. This fixes duplicate parallel instances.
  if (opts?.suppressRestart) {
    svc.generation++
  }

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
